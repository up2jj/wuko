package workflow

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

type fakePluginActionLoader struct {
	calls int
}

func (loader *fakePluginActionLoader) LoadAction(_ context.Context, namespace, name string) (json.RawMessage, error) {
	loader.calls++
	return json.RawMessage(`{
		"version": 1,
		"name": "build",
		"inputs": {"target": {"type": "string", "required": true}},
		"outputs": {"artifact": {"value": "steps.package.stdout"}},
		"steps": [{"id": "package", "type": "shell", "with": {"command": "printf", "args": ["%s", "{{ .inputs.target }}"]}}]
	}`), nil
}

func TestPluginActionResolutionUsesExistingDecoderAndCache(t *testing.T) {
	provider := &fakePluginActionLoader{}
	loader := NewLoader(nil, WithPluginActions(provider))
	baseDir := t.TempDir()
	definition, err := loader.DecodeStdinContext(t.Context(), strings.NewReader(`
version: 1
name: caller
steps:
  - id: first
    uses: plugin:acme/build
    with: {target: linux}
  - id: second
    uses: plugin:acme/build
    with: {target: darwin}
`), baseDir, LoadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := loader.Prepare(t.Context(), definition, LoadOptions{RunDir: baseDir}); err != nil {
		t.Fatal(err)
	}
	if provider.calls != 1 {
		t.Fatalf("LoadAction calls = %d, want 1", provider.calls)
	}
	for _, step := range definition.Steps {
		if step.Action == nil || step.Action.Name != "build" {
			t.Fatalf("step %q did not resolve plugin action", step.ID)
		}
		if !step.Action.DirBorrowed || step.Action.Files != nil {
			t.Fatalf("plugin action must use borrowed-directory semantics: %+v", step.Action)
		}
	}
}

func TestPluginActionSourceValidation(t *testing.T) {
	for _, source := range []string{"plugin:/build", "plugin:Acme/build", "plugin:acme/not-valid", "plugin:acme/build/extra"} {
		_, err := NewLoader(nil).DecodeStdin(strings.NewReader("version: 1\nname: invalid\nsteps:\n  - id: run\n    uses: "+source+"\n"), t.TempDir(), LoadOptions{})
		if err == nil {
			t.Errorf("source %q unexpectedly decoded", source)
		}
	}
}
