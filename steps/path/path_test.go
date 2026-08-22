package path

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/up2jj/wuko/step"
)

func TestConfigDefaultsAndValidation(t *testing.T) {
	runnerValue, err := New(map[string]any{"variable": "source", "message": "Select a file"})
	if err != nil {
		t.Fatal(err)
	}
	runner := runnerValue.(*Runner)
	if runner.config.Root != "." || runner.config.Kind != kindFile || !runner.required() {
		t.Fatalf("config = %#v", runner.config)
	}

	tests := []map[string]any{
		{"message": "Select"},
		{"variable": "source", "message": ""},
		{"variable": "source", "message": "Select", "kind": "socket"},
		{"variable": "source", "message": "Select", "kind": "directory", "patterns": []any{"**"}},
		{"variable": "source", "message": "Select", "patterns": []any{""}},
		{"variable": "source", "message": "Select", "patterns": []any{"../*.go"}},
		{"variable": "source", "message": "Select", "patterns": []any{"["}},
		{"variable": "source", "message": "Select", "unknown": true},
	}
	for _, raw := range tests {
		if _, err := New(raw); err == nil {
			t.Fatalf("New(%#v) succeeded", raw)
		}
	}
}

func TestPreSuppliedSingleAndMultiplePaths(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "main.go"))
	mustWrite(t, filepath.Join(root, "notes.txt"))

	single := newRunner(t, map[string]any{
		"variable": "source", "message": "Select", "patterns": []any{"**/*.go"},
	})
	result, err := single.Run(t.Context(), step.Request{RunDir: root, Vars: map[string]any{"source": "main.go"}})
	if err != nil {
		t.Fatal(err)
	}
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outputs["value"] != "main.go" || result.Outputs["root"] != canonicalRoot || result.Variables["source"] != "main.go" {
		t.Fatalf("result = %#v", result)
	}

	multiple := newRunner(t, map[string]any{
		"variable": "sources", "message": "Select", "multiple": true,
	})
	result, err = multiple.Run(t.Context(), step.Request{
		RunDir: root, Vars: map[string]any{"sources": []any{"notes.txt", "main.go", "notes.txt"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []any{"notes.txt", "main.go"}
	if !reflect.DeepEqual(result.Outputs["values"], want) || !reflect.DeepEqual(result.Variables["sources"], want) || result.Outputs["count"] != 2 {
		t.Fatalf("result = %#v", result)
	}
}

func TestRootAndKindValidation(t *testing.T) {
	runDir := t.TempDir()
	root := filepath.Join(runDir, "workspace")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(root, "file.txt"))

	directory := newRunner(t, map[string]any{
		"variable": "target", "message": "Select", "root": "workspace", "kind": "directory",
	})
	result, err := directory.Run(t.Context(), step.Request{RunDir: runDir, Vars: map[string]any{"target": "."}})
	if err != nil {
		t.Fatal(err)
	}
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outputs["value"] != "." || result.Outputs["root"] != canonicalRoot {
		t.Fatalf("result = %#v", result)
	}

	_, err = directory.Run(t.Context(), step.Request{RunDir: runDir, Vars: map[string]any{"target": "file.txt"}})
	if err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("error = %v", err)
	}
	file := newRunner(t, map[string]any{"variable": "target", "message": "Select", "root": root})
	_, err = file.Run(t.Context(), step.Request{Vars: map[string]any{"target": "."}})
	if err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("error = %v", err)
	}
}

func TestOptionalAndNonInteractiveValues(t *testing.T) {
	root := t.TempDir()
	optionalSingle := newRunner(t, map[string]any{
		"variable": "source", "message": "Select", "required": false,
	})
	result, err := optionalSingle.Run(t.Context(), step.Request{RunDir: root, Vars: map[string]any{"source": ""}})
	if err != nil || result.Outputs["value"] != "" {
		t.Fatalf("result = %#v, error = %v", result, err)
	}
	optionalMultiple := newRunner(t, map[string]any{
		"variable": "sources", "message": "Select", "multiple": true, "required": false,
	})
	result, err = optionalMultiple.Run(t.Context(), step.Request{RunDir: root, Vars: map[string]any{"sources": []any{}}})
	if err != nil || result.Outputs["count"] != 0 {
		t.Fatalf("result = %#v, error = %v", result, err)
	}

	required := newRunner(t, map[string]any{"variable": "source", "message": "Select"})
	_, err = required.Run(t.Context(), step.Request{RunDir: root, Vars: map[string]any{}, Interactive: false})
	if err == nil || !strings.Contains(err.Error(), "supply it with --var") {
		t.Fatalf("error = %v", err)
	}
	_, err = required.Run(t.Context(), step.Request{RunDir: root, Vars: map[string]any{"source": []any{"file"}}})
	if err == nil || !strings.Contains(err.Error(), "must be a string") {
		t.Fatalf("error = %v", err)
	}
}

func TestPreSuppliedPathFailures(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "notes.txt"))
	runner := newRunner(t, map[string]any{
		"variable": "source", "message": "Select", "patterns": []any{"**/*.go"},
	})
	for _, value := range []string{"notes.txt", "missing.go", "../outside.go", filepath.Join(root, "notes.txt"), `C:\\notes.txt`} {
		if _, err := runner.Run(t.Context(), step.Request{RunDir: root, Vars: map[string]any{"source": value}}); err == nil {
			t.Fatalf("value %q succeeded", value)
		}
	}
}

func TestSymlinkBoundary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation commonly requires additional privileges on Windows")
	}
	root := t.TempDir()
	inside := filepath.Join(root, "inside.txt")
	mustWrite(t, inside)
	outsideDir := t.TempDir()
	outside := filepath.Join(outsideDir, "outside.txt")
	mustWrite(t, outside)
	if err := os.Symlink(inside, filepath.Join(root, "inside-link.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "outside-link.txt")); err != nil {
		t.Fatal(err)
	}
	runner := newRunner(t, map[string]any{"variable": "source", "message": "Select"})
	if _, err := runner.Run(t.Context(), step.Request{RunDir: root, Vars: map[string]any{"source": "inside-link.txt"}}); err != nil {
		t.Fatalf("internal symlink: %v", err)
	}
	_, err := runner.Run(t.Context(), step.Request{RunDir: root, Vars: map[string]any{"source": "outside-link.txt"}})
	if err == nil || !strings.Contains(err.Error(), "outside root") {
		t.Fatalf("error = %v", err)
	}
}

func newRunner(t *testing.T, raw map[string]any) *Runner {
	t.Helper()
	runner, err := New(raw)
	if err != nil {
		t.Fatal(err)
	}
	return runner.(*Runner)
}

func mustWrite(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("test"), 0o644); err != nil {
		t.Fatal(err)
	}
}
