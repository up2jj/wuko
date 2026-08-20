package importvars

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/up2jj/wuko/step"
)

func TestRunImportsWorkflowRelativeFiles(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "defaults.json", `{"Name":"default","nested":{"old":true}}`)
	writeTestFile(t, root, "override.toml", "name = \"override\"\n[nested]\nnew = true\n")
	runner, err := New(map[string]any{"files": []any{"defaults.json", "override.toml"}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(t.Context(), step.Request{WorkflowDir: root, Vars: map[string]any{"name": "existing"}})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"name": "override", "nested": map[string]any{"new": true}}
	if !reflect.DeepEqual(result.Variables, want) {
		t.Fatalf("variables = %#v", result.Variables)
	}
	if result.Outputs["count"] != 2 || !reflect.DeepEqual(result.Outputs["variables"], want) {
		t.Fatalf("outputs = %#v", result.Outputs)
	}
}

func TestRunReturnsNoPartialResult(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "valid.json", `{"loaded":true}`)
	runner, err := New(map[string]any{"files": []any{"valid.json", "missing.toml"}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(t.Context(), step.Request{WorkflowDir: root})
	if err == nil || len(result.Outputs) != 0 || len(result.Variables) != 0 {
		t.Fatalf("result = %#v, error = %v", result, err)
	}
}

func TestRejectsInvalidConfiguration(t *testing.T) {
	tests := []map[string]any{
		{},
		{"files": []any{}},
		{"files": []any{""}},
		{"files": []any{"values.yaml"}},
		{"files": []any{"values.json"}, "unknown": true},
	}
	for _, raw := range tests {
		if _, err := New(raw); err == nil || strings.TrimSpace(err.Error()) == "" {
			t.Fatalf("New(%#v) error = %v", raw, err)
		}
	}
	if _, err := New(map[string]any{"files": []any{`{{ .vars.file }}`}}); err != nil {
		t.Fatalf("templated path was rejected: %v", err)
	}
}

func TestRunHonorsCancellation(t *testing.T) {
	runner, err := New(map[string]any{"files": []any{"missing.json"}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	result, err := runner.Run(ctx, step.Request{WorkflowDir: t.TempDir()})
	if err != context.Canceled || len(result.Outputs) != 0 || len(result.Variables) != 0 {
		t.Fatalf("result = %#v, error = %v", result, err)
	}
}

func writeTestFile(t *testing.T, root, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
