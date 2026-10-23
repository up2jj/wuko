package file

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/up2jj/wuko/internal/steptest"
	"github.com/up2jj/wuko/internal/testfs"
	"github.com/up2jj/wuko/step"
)

func TestWriteReadStatAndOverwriteModes(t *testing.T) {
	root := t.TempDir()
	result := run(t, root, map[string]any{
		"operation": "write", "path": "script.sh", "content": "echo hi\n", "mode": "0750",
	})
	if result.Outputs["created"] != true || result.Outputs["mode"] != "0750" {
		t.Fatalf("write outputs = %#v", result.Outputs)
	}
	path := filepath.Join(root, "script.sh")
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o750 {
		t.Fatalf("stat = %#v, error = %v", info, err)
	}

	runner := steptest.Build(t, New, map[string]any{"operation": "write", "path": "script.sh", "content": "changed"})
	if _, err := runner.Run(t.Context(), step.Request{RunDir: root}); err == nil || !strings.Contains(err.Error(), "overwrite") {
		t.Fatalf("error = %v", err)
	}
	run(t, root, map[string]any{"operation": "write", "path": "script.sh", "content": "changed", "overwrite": true})
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o750 {
		t.Fatalf("preserved mode = %#v, error = %v", info, err)
	}

	read := run(t, root, map[string]any{"operation": "read", "path": "script.sh"})
	if read.Outputs["content"] != "changed" || read.Outputs["size"] != int64(7) {
		t.Fatalf("read outputs = %#v", read.Outputs)
	}
	stat := run(t, root, map[string]any{"operation": "stat", "path": "script.sh"})
	if stat.Outputs["exists"] != true || stat.Outputs["type"] != "file" || stat.Outputs["mode"] != "0750" {
		t.Fatalf("stat outputs = %#v", stat.Outputs)
	}
	missing := run(t, root, map[string]any{"operation": "stat", "path": "missing"})
	if missing.Outputs["exists"] != false {
		t.Fatalf("missing outputs = %#v", missing.Outputs)
	}
}

func TestMkdirCopyMoveListAndRemove(t *testing.T) {
	root := t.TempDir()
	directory := run(t, root, map[string]any{
		"operation": "mkdir", "path": "nested/dist", "recursive": true, "mode": "0700",
	})
	if directory.Outputs["created"] != true || directory.Outputs["mode"] != "0700" {
		t.Fatalf("mkdir outputs = %#v", directory.Outputs)
	}
	testfs.WriteFile(t, filepath.Join(root, "source"), []byte("data"), 0o640)
	copyResult := run(t, root, map[string]any{
		"operation": "copy", "path": "source", "destination": "nested/dist/copied",
	})
	if copyResult.Outputs["mode"] != "0640" || copyResult.Outputs["size"] != int64(4) {
		t.Fatalf("copy outputs = %#v", copyResult.Outputs)
	}
	moveResult := run(t, root, map[string]any{
		"operation": "move", "path": "nested/dist/copied", "destination": "nested/dist/moved",
	})
	if moveResult.Outputs["destination"] != filepath.Join(root, "nested/dist/moved") {
		t.Fatalf("move outputs = %#v", moveResult.Outputs)
	}
	listed := run(t, root, map[string]any{"operation": "list", "path": "nested", "recursive": true})
	entries := listed.Outputs["entries"].([]any)
	paths := make([]string, len(entries))
	for i, entry := range entries {
		paths[i] = entry.(map[string]any)["path"].(string)
	}
	if strings.Join(paths, ",") != "dist,dist/moved" {
		t.Fatalf("paths = %#v", paths)
	}

	runner := steptest.Build(t, New, map[string]any{"operation": "remove", "path": "nested"})
	if _, err := runner.Run(t.Context(), step.Request{RunDir: root}); err == nil {
		t.Fatal("non-recursive removal succeeded")
	}
	removed := run(t, root, map[string]any{"operation": "remove", "path": "nested", "recursive": true})
	if removed.Outputs["removed"] != true {
		t.Fatalf("remove outputs = %#v", removed.Outputs)
	}
	missing := run(t, root, map[string]any{"operation": "remove", "path": "nested", "recursive": true})
	if missing.Outputs["removed"] != false {
		t.Fatalf("missing remove outputs = %#v", missing.Outputs)
	}
}

func TestAtomicWriteDoesNotOverwriteExistingDestination(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "destination")
	testfs.WriteFile(t, destination, []byte("original"), 0o600)
	err := atomicWrite(t.Context(), destination, strings.NewReader("replacement"), 0o644, false)
	if err == nil || !strings.Contains(err.Error(), "overwrite") {
		t.Fatalf("error = %v", err)
	}
	content, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "original" {
		t.Fatalf("content = %q", content)
	}
}

func TestAtomicWriteAllowsSingleConcurrentCreator(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "destination")
	start := make(chan struct{})
	errors := make(chan error, 8)
	var group sync.WaitGroup
	for range 8 {
		group.Go(func() {
			<-start
			errors <- atomicWrite(t.Context(), destination, strings.NewReader("content"), 0o600, false)
		})
	}
	close(start)
	group.Wait()
	close(errors)
	succeeded := 0
	for err := range errors {
		if err == nil {
			succeeded++
			continue
		}
		if !strings.Contains(err.Error(), "overwrite") {
			t.Fatalf("error = %v", err)
		}
	}
	if succeeded != 1 {
		t.Fatalf("successful writers = %d, want 1", succeeded)
	}
}

func TestMoveDoesNotOverwriteExistingDestination(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	destination := filepath.Join(root, "destination")
	testfs.WriteFile(t, source, []byte("source"), 0o600)
	testfs.WriteFile(t, destination, []byte("destination"), 0o600)
	runner := steptest.Build(t, New, map[string]any{"operation": "move", "path": "source", "destination": "destination"})
	if _, err := runner.Run(t.Context(), step.Request{RunDir: root}); err == nil || !strings.Contains(err.Error(), "overwrite") {
		t.Fatalf("error = %v", err)
	}
	for path, want := range map[string]string{source: "source", destination: "destination"} {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(content) != want {
			t.Fatalf("content of %s = %q", path, content)
		}
	}
}

func TestMoveAcrossFilesystemsPreservesDirectoryTree(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	destination := filepath.Join(root, "destination")
	if err := os.Mkdir(source, 0o750); err != nil {
		t.Fatal(err)
	}
	filePath := filepath.Join(source, "file")
	testfs.WriteFile(t, filePath, []byte("content"), 0o640)
	if err := os.Chmod(filePath, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("file", filepath.Join(source, "link")); err != nil {
		t.Fatal(err)
	}
	if err := moveAcrossFilesystems(t.Context(), source, destination, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(source); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("source error = %v", err)
	}
	if info, err := os.Stat(destination); err != nil || info.Mode().Perm() != 0o750 {
		t.Fatalf("destination info = %#v, error = %v", info, err)
	}
	if info, err := os.Stat(filepath.Join(destination, "file")); err != nil || info.Mode().Perm() != 0o640 {
		t.Fatalf("file info = %#v, error = %v", info, err)
	}
	if target, err := os.Readlink(filepath.Join(destination, "link")); err != nil || target != "file" {
		t.Fatalf("link target = %q, error = %v", target, err)
	}
}

func TestChmodAndSymlinkRejection(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "script")
	testfs.WriteFile(t, path, []byte("x"), 0o600)
	result := run(t, root, map[string]any{"operation": "chmod", "path": "script", "mode": "0755"})
	if result.Outputs["mode"] != "0755" {
		t.Fatalf("outputs = %#v", result.Outputs)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("mode = %#v, error = %v", info, err)
	}

	link := filepath.Join(root, "link")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	runner := steptest.Build(t, New, map[string]any{"operation": "chmod", "path": "link", "mode": "0644"})
	if _, err := runner.Run(t.Context(), step.Request{RunDir: root}); err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("error = %v", err)
	}
}

func TestRemoveRefusesTheRunDirectoryAndItsAncestors(t *testing.T) {
	parent := t.TempDir()
	runDir := filepath.Join(parent, "project")
	testfs.MkdirAll(t, runDir, 0o755)
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(runDir, alias); err != nil {
		t.Fatal(err)
	}
	tests := []struct{ path, runDir string }{
		{path: ".", runDir: runDir},
		{path: "..", runDir: runDir},
		{path: runDir, runDir: runDir},
		{path: parent, runDir: runDir},
		// The run directory reached through a symlink is still the run directory.
		{path: runDir, runDir: alias},
	}
	for _, test := range tests {
		t.Run(test.path+" from "+test.runDir, func(t *testing.T) {
			runner := steptest.Build(t, New, map[string]any{"operation": "remove", "path": test.path, "recursive": true})
			if _, err := runner.Run(t.Context(), step.Request{RunDir: test.runDir}); err == nil || !strings.Contains(err.Error(), "run directory") {
				t.Errorf("error = %v, want the run directory protected", err)
			}
			if _, err := os.Stat(runDir); err != nil {
				t.Fatalf("run directory was removed: %v", err)
			}
		})
	}
}

func TestRejectsUnsafeRemovalAndCanceledOperation(t *testing.T) {
	runner := steptest.Build(t, New, map[string]any{"operation": "remove", "path": string(filepath.Separator), "recursive": true})
	if _, err := runner.Run(t.Context(), step.Request{RunDir: t.TempDir()}); err == nil || !strings.Contains(err.Error(), "filesystem root") {
		t.Fatalf("error = %v", err)
	}

	runner = steptest.Build(t, New, map[string]any{"operation": "stat", "path": "anything"})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := runner.Run(ctx, step.Request{RunDir: t.TempDir()}); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
}

func TestRejectsInvalidConfiguration(t *testing.T) {
	tests := []map[string]any{
		{},
		{"operation": "read"},
		{"operation": "write", "path": "x"},
		{"operation": "copy", "path": "x"},
		{"operation": "chmod", "path": "x"},
		{"operation": "chmod", "path": "x", "mode": "4755"},
		{"operation": "chmod", "path": "x", "mode": 755},
		{"operation": "read", "path": "x", "recursive": true},
		{"operation": "unknown", "path": "x"},
		{"operation": "read", "path": "x", "unknown": true},
	}
	for _, raw := range tests {
		if _, err := New(raw); err == nil {
			t.Fatalf("New(%#v) succeeded", raw)
		}
	}
}

func run(t *testing.T, root string, raw map[string]any) step.Result {
	t.Helper()
	runner := steptest.Build(t, New, raw)
	result, err := runner.Run(t.Context(), step.Request{RunDir: root})
	if err != nil {
		t.Fatal(err)
	}
	return result
}
