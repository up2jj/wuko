package step_test

import (
	"context"
	"testing"

	"github.com/up2jj/wuko/step"
)

type schemaRunner struct{}

func (schemaRunner) Validate(context.Context, step.Request) error { return nil }
func (schemaRunner) Run(context.Context, step.Request) (step.Result, error) {
	return step.Result{}, nil
}

func TestRegistryOutputSchemas(t *testing.T) {
	registry := step.NewRegistry(step.WithOutputSchemaResolver(func(_ context.Context, name string) (step.OutputSchema, bool, error) {
		if name == "plugin" {
			return step.ClosedOutputs("value"), true, nil
		}
		return step.OutputSchema{}, false, nil
	}))
	if err := registry.RegisterDefinition("typed", step.Registration{
		Builder: func(map[string]any) (step.Runner, error) { return schemaRunner{}, nil },
		Outputs: step.ClosedOutputs("status", "body"),
	}); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register("legacy", func(map[string]any) (step.Runner, error) { return schemaRunner{}, nil }); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name   string
		open   bool
		fields int
	}{
		{"typed", false, 2}, {"legacy", true, 0}, {"plugin", false, 1}, {"unknown", true, 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			schema, err := registry.OutputSchema(context.Background(), test.name)
			if err != nil {
				t.Fatal(err)
			}
			if schema.Open != test.open || len(schema.Fields) != test.fields {
				t.Fatalf("schema = %+v", schema)
			}
		})
	}
}

func TestValidateOutputKeys(t *testing.T) {
	schema := step.ClosedObject(map[string]step.OutputSchema{
		"status":   step.Scalar(),
		"metadata": step.ClosedOutputs("request_id"),
	})
	if err := step.ValidateOutputKeys(schema, map[string]any{"status": 200, "metadata": map[string]any{"request_id": "abc"}}); err != nil {
		t.Fatal(err)
	}
	if err := step.ValidateOutputKeys(schema, map[string]any{"sttaus": 200}); err == nil {
		t.Fatal("expected undeclared output failure")
	}
}
