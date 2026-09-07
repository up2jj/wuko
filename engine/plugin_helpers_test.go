package engine

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/up2jj/wuko/helper"
	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/workflow"
)

type fakePluginHelpers struct{}

func (fakePluginHelpers) LoadHelpers(context.Context, map[string]workflow.PluginSource) (helper.Set, error) {
	return helper.Set{"acme_slug": func(_ context.Context, args []any) (any, error) {
		return strings.ToLower(strings.ReplaceAll(args[0].(string), " ", "-")), nil
	}}, nil
}

func TestCompositeActionInheritsPluginHelpersAtRuntime(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "actions", "echo"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "actions", "echo", "action.yaml"), []byte(`version: 1
name: echo
inputs:
  value: {type: string, required: true}
steps:
  - id: run
    type: capture
    with: {value: '{{ acme_slug .inputs.value }}'}
`), 0o644); err != nil {
		t.Fatal(err)
	}
	workflowPath := filepath.Join(dir, "workflow.yaml")
	if err := os.WriteFile(workflowPath, []byte(`version: 1
name: caller
plugins:
  acme:
    source: https://example.test/plugin.json
    sha256: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
steps:
  - id: local
    uses: ./actions/echo
    with: {value: Hello World}
`), 0o644); err != nil {
		t.Fatal(err)
	}
	loader := workflow.NewLoader(nil, workflow.WithPluginHelpers(fakePluginHelpers{}))
	definition, err := loader.Load(t.Context(), workflowPath, workflow.LoadOptions{RunDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	var runs int
	var captured any
	registry := newTestRegistry(t, map[string]step.Builder{"capture": func(raw map[string]any) (step.Runner, error) {
		captured = raw["value"]
		return countingRunner{value: raw["value"], runs: &runs}, nil
	}})
	if err := New(registry).Validate(t.Context(), definition, Options{RunDir: dir}); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if _, err := New(registry).Run(t.Context(), definition, Options{RunDir: dir, Stdout: io.Discard, Stderr: io.Discard}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if runs != 1 || captured != "hello-world" {
		t.Fatalf("action runs = %d, rendered helper value = %#v", runs, captured)
	}
}
