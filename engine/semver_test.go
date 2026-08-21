package engine_test

import (
	"io"
	"testing"

	"github.com/up2jj/wuko/engine"
	"github.com/up2jj/wuko/step"
	semverstep "github.com/up2jj/wuko/steps/semver"
	"github.com/up2jj/wuko/workflow"
)

func TestSemVerTemplatesAndCommitsTypedResults(t *testing.T) {
	definition := &workflow.Definition{
		Version: 1, Name: "release", Dir: t.TempDir(),
		Vars: map[string]any{"version": "v1.4.2", "operation": "constrain", "constraint": "^1.4"},
		Steps: []workflow.Step{{ID: "supported", Type: "semver", With: map[string]any{
			"operation": "{{ .vars.operation }}", "version": "{{ .vars.version }}",
			"constraint": "{{ .vars.constraint }}", "variable": "is_supported",
		}}},
	}
	registry := step.NewRegistry()
	if err := semverstep.Register(registry); err != nil {
		t.Fatal(err)
	}
	state, err := engine.New(registry).Run(t.Context(), definition, engine.Options{
		RunDir: t.TempDir(), Stdout: io.Discard, Stderr: io.Discard,
	})
	if err != nil {
		t.Fatal(err)
	}
	if state.Vars["is_supported"] != true {
		t.Fatalf("variables = %#v", state.Vars)
	}
	outputs := state.Steps["supported"].(map[string]any)
	if outputs["value"] != true || outputs["matched"] != true || outputs["version"] != "1.4.2" {
		t.Fatalf("outputs = %#v", outputs)
	}
}
