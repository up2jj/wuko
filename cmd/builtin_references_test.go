package cmd

import (
	"strings"
	"testing"

	"github.com/up2jj/wuko/engine"
	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/workflow"
)

// Steps report the state their configuration reads through step.Registration.References; a
// reference to an undeclared variable must fail validation before anything runs.
func TestBuiltinStepReferencesAreValidated(t *testing.T) {
	registry := step.NewRegistry()
	if err := registerBuiltinSteps(registry); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		stepType string
		with     map[string]any
	}{
		{stepType: "assert", with: map[string]any{"expr": "vars.missing == 1"}},
		{stepType: "set", with: map[string]any{"expr": "vars.missing", "variable": "copy"}},
		{stepType: "shell", with: map[string]any{"argv": map[string]any{"expr": "vars.missing"}}},
		{stepType: "process", with: map[string]any{"argv": map[string]any{"expr": "vars.missing"}, "ready": map[string]any{"log": "ready"}}},
		{stepType: "extract", with: map[string]any{"from": "vars.missing", "pattern": "(?P<value>.*)"}},
		{stepType: "decode", with: map[string]any{"from": "vars.missing", "format": "json"}},
		{stepType: "jsonpath", with: map[string]any{"from": "vars.missing", "path": "$.value"}},
		{stepType: "tui_table", with: map[string]any{"from": "vars.missing"}},
	}
	for _, test := range tests {
		t.Run(test.stepType, func(t *testing.T) {
			definition := &workflow.Definition{Version: 1, Name: "references", Dir: t.TempDir(), Steps: []workflow.Step{
				{ID: "subject", Type: test.stepType, With: test.with},
			}}
			err := engine.New(registry).Validate(t.Context(), definition, engine.Options{RunDir: t.TempDir()})
			if err == nil || !strings.Contains(err.Error(), `variable "missing" is not declared`) {
				t.Fatalf("Validate() error = %v, want the undeclared variable reported", err)
			}
		})
	}
}
