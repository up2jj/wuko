package file

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/up2jj/wuko/internal/testfs"
	"github.com/up2jj/wuko/step"
)

func TestWriteSymlinkBehavior(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		linkKind    string
		follow      bool
		omitFollow  bool
		noOverwrite bool
		mode        string
		wantError   string
	}{
		{name: "relative", follow: true},
		{name: "absolute", linkKind: "absolute", follow: true},
		{name: "chain", linkKind: "chain", follow: true},
		{name: "explicit mode", follow: true, mode: "0640"},
		{name: "omitted replaces link", omitFollow: true},
		{name: "false replaces link"},
		{name: "overwrite required", follow: true, noOverwrite: true, wantError: "set overwrite to true"},
		{name: "dangling", linkKind: "dangling", follow: true, wantError: "resolving destination"},
		{name: "loop", linkKind: "loop", follow: true, wantError: "resolving destination"},
		{name: "directory", linkKind: "directory", follow: true, wantError: "must link to a regular file"},
		{name: "special file", linkKind: "fifo", follow: true, wantError: "must link to a regular file"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			directory := filepath.Join(root, "dots")
			testfs.MkdirAll(t, directory, 0o755)
			target := filepath.Join(directory, "config")
			testfs.WriteFile(t, target, []byte("original"), 0o600)
			before, err := os.Stat(target)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(root, ".config")
			links := map[string]string{path: "dots/config"}
			switch tt.linkKind {
			case "absolute":
				links[path] = target
			case "chain":
				links[path] = "dots/alias"
				links[filepath.Join(directory, "alias")] = "config"
			case "dangling":
				links[path] = "dots/missing"
			case "loop":
				links[path] = "dots/alias"
				links[filepath.Join(directory, "alias")] = "../.config"
			case "directory":
				links[path] = "dots"
			case "fifo":
				links[path] = "dots/pipe"
				if err := syscall.Mkfifo(filepath.Join(directory, "pipe"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			for name, destination := range links {
				if err := os.Symlink(destination, name); err != nil {
					t.Fatal(err)
				}
			}

			raw := map[string]any{
				"operation": "write", "path": ".config", "content": "replacement", "overwrite": !tt.noOverwrite,
			}
			if !tt.omitFollow {
				raw["follow_symlinks"] = tt.follow
			}
			if tt.mode != "" {
				raw["mode"] = tt.mode
			}
			runner, err := New(raw)
			if err != nil {
				t.Fatal(err)
			}
			result, err := runner.Run(t.Context(), step.Request{RunDir: root})
			if tt.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantError) {
					t.Fatalf("Run() error = %v, want %q", err, tt.wantError)
				}
				if len(result.Outputs) != 0 {
					t.Fatalf("failed write outputs = %#v", result.Outputs)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				mode := "0644"
				if tt.follow {
					mode = "0600"
				}
				if tt.mode != "" {
					mode = tt.mode
				}
				want := map[string]any{"path": path, "created": false, "size": int64(len("replacement")), "mode": mode}
				if diff := cmp.Diff(want, result.Outputs); diff != "" {
					t.Errorf("outputs (-want +got):\n%s", diff)
				}
				written, err := os.Stat(path)
				if err != nil {
					t.Fatal(err)
				}
				if formatMode(written.Mode()) != mode {
					t.Errorf("written mode = %s, want %s", formatMode(written.Mode()), mode)
				}
			}

			for name, destination := range links {
				if name == path && !tt.follow && tt.wantError == "" {
					info, err := os.Lstat(name)
					if err != nil || !info.Mode().IsRegular() {
						t.Fatalf("replaced link info = %v, error = %v", info, err)
					}
					data, err := os.ReadFile(name)
					if err != nil || string(data) != "replacement" {
						t.Fatalf("replacement content = %q, error = %v", data, err)
					}
					continue
				}
				if got, err := os.Readlink(name); err != nil || got != destination {
					t.Errorf("Readlink(%s) = %q, %v, want %q", name, got, err, destination)
				}
			}
			wantContent := "original"
			if tt.follow && tt.wantError == "" {
				wantContent = "replacement"
			}
			data, err := os.ReadFile(target)
			if err != nil || string(data) != wantContent {
				t.Fatalf("target content = %q, error = %v, want %q", data, err, wantContent)
			}
			after, err := os.Stat(target)
			if err != nil {
				t.Fatal(err)
			}
			if wantContent == "original" {
				if !os.SameFile(before, after) || after.Mode() != before.Mode() {
					t.Error("unchanged target lost its inode or mode")
				}
			} else if os.SameFile(before, after) {
				t.Error("followed target was not atomically replaced")
			}
			if tt.linkKind == "dangling" {
				if _, err := os.Lstat(filepath.Join(directory, "missing")); !os.IsNotExist(err) {
					t.Errorf("dangling target error = %v, want not exist", err)
				}
			}
			if tt.linkKind == "fifo" {
				if info, err := os.Lstat(filepath.Join(directory, "pipe")); err != nil || info.Mode()&os.ModeNamedPipe == 0 {
					t.Fatalf("FIFO info = %v, error = %v", info, err)
				}
			}
			assertNoWriteTemporaryFiles(t, root)
		})
	}
}

func TestWriteFollowSymlinksWithOrdinaryPaths(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"create", "overwrite", "missing parent", "directory"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			path := filepath.Join(root, "file")
			switch scenario {
			case "overwrite":
				testfs.WriteFile(t, path, []byte("original"), 0o600)
			case "missing parent":
				path = filepath.Join(root, "missing", "file")
			case "directory":
				testfs.MkdirAll(t, path, 0o755)
			}
			runner, err := New(map[string]any{
				"operation": "write", "path": path, "content": "new", "overwrite": true, "follow_symlinks": true,
			})
			if err != nil {
				t.Fatal(err)
			}
			result, err := runner.Run(t.Context(), step.Request{RunDir: root})
			if scenario == "missing parent" || scenario == "directory" {
				if err == nil {
					t.Fatal("write succeeded")
				}
				assertNoWriteTemporaryFiles(t, root)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			mode := "0644"
			if scenario == "overwrite" {
				mode = "0600"
			}
			want := map[string]any{"path": path, "size": int64(3), "mode": mode, "created": scenario == "create"}
			if diff := cmp.Diff(want, result.Outputs); diff != "" {
				t.Errorf("outputs (-want +got):\n%s", diff)
			}
			if data, err := os.ReadFile(path); err != nil || string(data) != "new" {
				t.Fatalf("content = %q, error = %v", data, err)
			}
			assertNoWriteTemporaryFiles(t, root)
		})
	}
}

func TestFollowSymlinksRejectsOtherOperations(t *testing.T) {
	t.Parallel()
	operations := []string{
		operationRead, operationCopy, operationMove, operationRemove, operationMkdir, operationList,
		operationStat, operationChmod, operationFind, operationLink, operationTruncate, operationTail,
		operationDiskUsage, operationAtomicSwap, operationPermissions, operationTouch,
		operationAppend, operationEnsureLine, operationEnsureBlock,
	}
	for _, operation := range operations {
		t.Run(operation, func(t *testing.T) {
			t.Parallel()
			raw := map[string]any{"operation": operation, "path": "file", "follow_symlinks": false}
			switch operation {
			case operationCopy, operationMove, operationAtomicSwap:
				raw["destination"] = "destination"
			case operationChmod, operationPermissions:
				raw["mode"] = "0644"
			case operationFind:
				raw["patterns"] = []string{"**"}
			case operationLink:
				raw["destination"], raw["link_type"] = "destination", "symbolic"
			case operationAppend, operationEnsureBlock:
				raw["content"] = "text"
			case operationEnsureLine:
				raw["line"] = "text"
			}
			if _, err := New(raw); err == nil || !strings.Contains(err.Error(), "follow_symlinks is not allowed") {
				t.Fatalf("New() error = %v, want field rejection", err)
			}
		})
	}
}

func assertNoWriteTemporaryFiles(t *testing.T, root string) {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if strings.HasPrefix(entry.Name(), ".wuko-file-") {
			t.Errorf("temporary file left behind: %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
