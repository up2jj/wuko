package glob

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/up2jj/wuko/internal/steptest"
	"github.com/up2jj/wuko/internal/testfs"
	"github.com/up2jj/wuko/step"
)

func TestGlobDiscoversSortedUniqueMetadata(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "main.go", "package main\n", 0o640)
	writeFile(t, root, "cmd/tool/main.go", "package main\n", 0o600)
	writeFile(t, root, "cmd/tool/main_test.go", "package main\n", 0o644)
	writeFile(t, root, "scripts/build.sh", "#!/bin/sh\n", 0o750)
	writeFile(t, root, "notes.txt", "notes\n", 0o644)

	result := run(t, root, map[string]any{
		"patterns": []any{"**/*.go", "cmd/?ool/main.go", "scripts/[b]uild.sh"},
	})
	if result.Outputs["root"] != root || result.Outputs["count"] != 4 {
		t.Fatalf("outputs = %#v", result.Outputs)
	}
	files := result.Outputs["files"].([]any)
	wantPaths := []string{"cmd/tool/main.go", "cmd/tool/main_test.go", "main.go", "scripts/build.sh"}
	for index, want := range wantPaths {
		metadata := files[index].(map[string]any)
		if metadata["path"] != want || metadata["name"] != filepath.Base(want) || metadata["type"] != "file" {
			t.Fatalf("files[%d] = %#v", index, metadata)
		}
		for _, field := range []string{"size", "mode", "modified_at"} {
			if _, ok := metadata[field]; !ok {
				t.Fatalf("files[%d] missing %s: %#v", index, field, metadata)
			}
		}
	}
	if files[0].(map[string]any)["mode"] != "0600" || files[3].(map[string]any)["mode"] != "0750" {
		t.Fatalf("file modes = %#v, %#v", files[0], files[3])
	}
}

func TestGlobRootHiddenAndEmptyBehavior(t *testing.T) {
	runDir := t.TempDir()
	root := filepath.Join(runDir, "source")
	writeFile(t, root, "visible.go", "visible", 0o644)
	writeFile(t, root, ".hidden.go", "hidden", 0o644)
	writeFile(t, root, ".config/settings.yaml", "hidden directory", 0o644)

	visible := run(t, runDir, map[string]any{"root": "source", "patterns": []any{"**/*.go"}})
	if visible.Outputs["root"] != root || visible.Outputs["count"] != 1 {
		t.Fatalf("visible outputs = %#v", visible.Outputs)
	}
	explicit := run(t, runDir, map[string]any{"root": root, "patterns": []any{".*.go", ".config/**/*.yaml"}})
	if explicit.Outputs["count"] != 2 {
		t.Fatalf("explicit outputs = %#v", explicit.Outputs)
	}
	empty := run(t, runDir, map[string]any{"root": "source", "patterns": []any{"**/*.rs"}})
	if empty.Outputs["count"] != 0 || len(empty.Outputs["files"].([]any)) != 0 {
		t.Fatalf("empty outputs = %#v", empty.Outputs)
	}
}

func TestGlobExcludesDirectoriesAndSymlinks(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "real/file.go", "real", 0o644)
	outside := t.TempDir()
	writeFile(t, outside, "outside.go", "outside", 0o644)
	if err := os.Symlink(filepath.Join(root, "real/file.go"), filepath.Join(root, "file-link.go")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "dir-link")); err != nil {
		t.Fatal(err)
	}

	result := run(t, root, map[string]any{"patterns": []any{"**", "dir-link/*.go"}})
	files := result.Outputs["files"].([]any)
	if len(files) != 1 || files[0].(map[string]any)["path"] != "real/file.go" {
		t.Fatalf("files = %#v", files)
	}

	literal := run(t, root, map[string]any{"patterns": []any{"file-link.go"}})
	if literal.Outputs["count"] != 0 {
		t.Fatalf("literal symlink outputs = %#v", literal.Outputs)
	}
}

func TestGlobRejectsInvalidConfiguration(t *testing.T) {
	tests := []struct {
		name string
		raw  map[string]any
		want string
	}{
		{name: "missing patterns", raw: map[string]any{}, want: "at least one"},
		{name: "empty patterns", raw: map[string]any{"patterns": []any{}}, want: "at least one"},
		{name: "empty pattern", raw: map[string]any{"patterns": []any{""}}, want: "non-empty"},
		{name: "patterns scalar", raw: map[string]any{"patterns": "*.go"}, want: "must be a list"},
		{name: "patterns expr extra key", raw: map[string]any{"patterns": map[string]any{"expr": "vars.files", "x": 1}}, want: "exactly a non-empty expr"},
		{name: "patterns expr syntax", raw: map[string]any{"patterns": map[string]any{"expr": "vars.("}}, want: "compiling patterns expr"},
		{name: "invalid missing root", raw: map[string]any{"patterns": []any{"*.go"}, "missing_root": "skip"}, want: "missing_root must be fail or empty"},
		{name: "malformed pattern", raw: map[string]any{"patterns": []any{"[abc"}}, want: "invalid pattern"},
		{name: "absolute pattern", raw: map[string]any{"patterns": []any{"/tmp/*.go"}}, want: "relative to root"},
		{name: "windows absolute pattern", raw: map[string]any{"patterns": []any{`C:\tmp\*.go`}}, want: "relative to root"},
		{name: "parent pattern", raw: map[string]any{"patterns": []any{"../*.go"}}, want: "parent directory"},
		{name: "unknown field", raw: map[string]any{"patterns": []any{"*.go"}, "unknown": true}, want: "field unknown"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := New(test.raw)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestGlobRejectsInvalidRootAndHonorsCancellation(t *testing.T) {
	runner := steptest.Build(t, New, map[string]any{"root": "missing", "patterns": []any{"**"}})
	if _, err := runner.Run(t.Context(), step.Request{RunDir: t.TempDir()}); err == nil || !strings.Contains(err.Error(), "inspecting glob root") {
		t.Fatalf("missing root error = %v", err)
	}

	runDir := t.TempDir()
	writeFile(t, runDir, "not-directory", "file", 0o644)
	runner = steptest.Build(t, New, map[string]any{"root": "not-directory", "patterns": []any{"**"}})
	if _, err := runner.Run(t.Context(), step.Request{RunDir: runDir}); err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("file root error = %v", err)
	}

	runner, err := New(map[string]any{"patterns": []any{"**"}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := runner.Run(ctx, step.Request{RunDir: runDir}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error = %v", err)
	}
}

func TestGlobMissingRootEmpty(t *testing.T) {
	t.Parallel()
	runDir := t.TempDir()
	result := run(t, runDir, map[string]any{"root": "absent", "patterns": []any{"**"}, "missing_root": "empty"})
	want := map[string]any{"root": filepath.Join(runDir, "absent"), "count": 0, "files": []any{}}
	if diff := cmp.Diff(want, result.Outputs); diff != "" {
		t.Fatalf("outputs mismatch (-want +got):\n%s", diff)
	}

	writeFile(t, runDir, "not-directory", "file", 0o644)
	runner := steptest.Build(t, New, map[string]any{"root": "not-directory", "patterns": []any{"**"}, "missing_root": "empty"})
	if _, err := runner.Run(t.Context(), step.Request{RunDir: runDir}); err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("file root error = %v", err)
	}
}

func TestGlobPatternsExpression(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeFile(t, root, ".zshrc", "", 0o644)
	writeFile(t, root, ".zprofile", "", 0o644)
	writeFile(t, root, ".bashrc", "", 0o644)
	raw := map[string]any{"patterns": map[string]any{"expr": "vars.files"}}

	tests := []struct {
		name  string
		files any
		want  []string
		err   string
	}{
		{name: "selected patterns", files: []any{".zshrc", ".z*", ".missing"}, want: []string{".zprofile", ".zshrc"}},
		{name: "empty list", files: []any{}, want: []string{}},
		{name: "not a list", files: ".zshrc", err: "must return a list"},
		{name: "empty item", files: []any{""}, err: "non-empty path"},
		{name: "parent pattern", files: []any{"../*"}, err: "patterns[0]: pattern must not contain a parent directory"},
		{name: "absolute pattern", files: []any{"/etc/*"}, err: "relative to root"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			runner := steptest.Build(t, New, raw)
			result, err := runner.Run(t.Context(), step.Request{RunDir: root, Vars: map[string]any{"files": test.files}})
			if test.err != "" {
				if err == nil || !strings.Contains(err.Error(), test.err) {
					t.Fatalf("error = %v, want substring %q", err, test.err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			got := []string{}
			for _, file := range result.Outputs["files"].([]any) {
				got = append(got, file.(map[string]any)["path"].(string))
			}
			if diff := cmp.Diff(test.want, got); diff != "" {
				t.Fatalf("paths mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestGlobReferencesPatternsExpression(t *testing.T) {
	t.Parallel()
	got, err := references(map[string]any{"patterns": map[string]any{"expr": "vars.files"}})
	if err != nil {
		t.Fatal(err)
	}
	want := step.References{Expressions: []step.ExpressionReference{{Label: "patterns expr", Source: "vars.files"}}}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("references mismatch (-want +got):\n%s", diff)
	}
}

func TestGlobReturnsFilesystemErrors(t *testing.T) {
	runner := steptest.Build(t, New, map[string]any{"patterns": []any{"**"}})
	runner.(*Runner).fsys = errorFS{}
	if _, err := runner.Run(t.Context(), step.Request{RunDir: t.TempDir()}); !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("filesystem error = %v", err)
	}
}

func TestGlobCancelsDuringTraversal(t *testing.T) {
	runDir := t.TempDir()
	writeFile(t, runDir, "nested/file.go", "content", 0o644)
	runner := steptest.Build(t, New, map[string]any{"patterns": []any{"**/*.txt"}})
	ctx, cancel := context.WithCancel(t.Context())
	runner.(*Runner).fsys = cancelOnOpenFS{FS: os.DirFS(runDir), cancel: cancel}
	if _, err := runner.Run(ctx, step.Request{RunDir: runDir}); !errors.Is(err, context.Canceled) {
		t.Fatalf("traversal cancellation error = %v", err)
	}
}

type errorFS struct{}

func (errorFS) Open(string) (fs.File, error) { return nil, fs.ErrPermission }

type cancelOnOpenFS struct {
	fs.FS
	cancel context.CancelFunc
}

func (f cancelOnOpenFS) Open(name string) (fs.File, error) {
	file, err := f.FS.Open(name)
	f.cancel()
	return file, err
}

func run(t *testing.T, runDir string, raw map[string]any) step.Result {
	t.Helper()
	runner := steptest.Build(t, New, raw)
	result, err := runner.Run(t.Context(), step.Request{RunDir: runDir})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func writeFile(t *testing.T, root, relative, content string, mode os.FileMode) {
	t.Helper()
	filePath := filepath.Join(root, filepath.FromSlash(relative))
	testfs.MkdirAll(t, filepath.Dir(filePath), 0o755)
	testfs.WriteFile(t, filePath, []byte(content), mode)
	if err := os.Chmod(filePath, mode); err != nil {
		t.Fatal(err)
	}
}
