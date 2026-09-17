package cmd

import (
	"testing"

	"github.com/up2jj/wuko/step"
)

func TestEveryBuiltinStepRegistersAnOutputSchema(t *testing.T) {
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
		})
	}
}
