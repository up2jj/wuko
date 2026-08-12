package engine

import (
	"context"
	"io"
	"testing"

	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/workflow"
)

type captureRunner struct {
	value any
	seen  *step.Request
}

func (r captureRunner) Run(_ context.Context, request step.Request) (step.Result, error) {
	*r.seen = request
	return step.Result{Outputs: map[string]any{"value": r.value}, Variables: map[string]any{"result": r.value}}, nil
}

func TestRunRendersStateAndEnvironment(t *testing.T) {
	t.Setenv("WUKO_ENGINE_HOST", "host-value")
	t.Setenv("WUKO_ENGINE_PRIORITY", "host")
	registry := step.NewRegistry()
	var seen step.Request
	if err := registry.Register("capture", func(raw map[string]any) (step.Runner, error) {
		return captureRunner{value: raw["value"], seen: &seen}, nil
	}); err != nil {
		t.Fatal(err)
	}
	definition := &workflow.Definition{
		Version: 1, Name: "test", Dir: t.TempDir(), Vars: map[string]any{"name": "workflow"},
		Env: map[string]string{
			"DERIVED":              "{{ .env.WUKO_ENGINE_HOST }}",
			"WUKO_ENGINE_PRIORITY": "workflow",
		},
		Steps: []workflow.Step{{ID: "capture", Type: "capture", With: map[string]any{"value": "{{ .vars.name }}:{{ .env.DERIVED }}"}}},
	}
	state, err := New(registry).Run(t.Context(), definition, Options{
		Vars: map[string]any{"name": "cli"}, Env: map[string]string{"CLI": "yes", "WUKO_ENGINE_PRIORITY": "cli"}, RunDir: t.TempDir(), Stdout: io.Discard, Stderr: io.Discard,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := state.Vars["result"]; got != "cli:host-value" {
		t.Fatalf("result = %v", got)
	}
	if seen.Env["DERIVED"] != "host-value" || seen.Env["CLI"] != "yes" {
		t.Fatalf("environment = %#v", seen.Env)
	}
	if seen.Env["WUKO_ENGINE_PRIORITY"] != "cli" {
		t.Fatalf("priority = %q", seen.Env["WUKO_ENGINE_PRIORITY"])
	}
}
