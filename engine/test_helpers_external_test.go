package engine_test

import (
	"testing"

	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/workflow"
)

func testDefinition(t *testing.T, name string, steps ...workflow.Step) *workflow.Definition {
	t.Helper()
	return &workflow.Definition{
		Version: 1,
		Name:    name,
		Dir:     t.TempDir(),
		Steps:   steps,
	}
}

func newTestRegistry(t *testing.T, builders map[string]step.Builder) *step.Registry {
	t.Helper()
	registry := step.NewRegistry()
	for name, builder := range builders {
		if err := registry.Register(name, builder); err != nil {
			t.Fatal(err)
		}
	}
	return registry
}
