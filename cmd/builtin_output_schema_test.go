package cmd

import (
	"testing"

	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/workflow"
)

func TestEveryBuiltinStepRegistersAnOutputSchema(t *testing.T) {
	t.Parallel()
	registry := step.NewRegistry()
	if err := registerBuiltinSteps(registry); err != nil {
		t.Fatal(err)
	}
	definitions := registry.Definitions()
	if len(definitions) == 0 {
		t.Fatal("no built-in steps registered")
	}
	for name, definition := range definitions {
		t.Run(name, func(t *testing.T) {
			if definition.Outputs.Kind == step.OutputUnknown {
				t.Fatal("output schema is missing")
			}
			if definition.Config == nil {
				t.Fatal("typed binding configuration is missing")
			}
			if !workflow.BuiltinStepBindings(name) {
				t.Fatal("dependency binding grammar is missing")
			}
		})
	}
}
