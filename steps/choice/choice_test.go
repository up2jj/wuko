package choice

import (
	"io"
	"testing"

	"github.com/up2jj/wuko/step"
)

func TestDynamicMultiplePreSupplied(t *testing.T) {
	runner, err := New(map[string]any{
		"variable": "projects", "message": "Projects", "multiple": true,
		"from": "steps.fetch.projects", "label_field": "name", "value_field": "id",
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(t.Context(), step.Request{
		Vars: map[string]any{"projects": []any{"frontend", "backend"}},
		Steps: map[string]any{"fetch": map[string]any{"projects": []any{
			map[string]any{"name": "Backend", "id": "backend"},
			map[string]any{"name": "Frontend", "id": "frontend"},
		}}}, Stdout: io.Discard, Stderr: io.Discard,
	})
	if err != nil {
		t.Fatal(err)
	}
	values := result.Outputs["values"].([]any)
	if len(values) != 2 || values[0] != "frontend" || values[1] != "backend" {
		t.Fatalf("values = %#v", values)
	}
}

func TestChoiceRejectsDuplicateValues(t *testing.T) {
	runner, err := New(map[string]any{
		"variable": "item", "message": "Item",
		"choices": []any{map[string]any{"label": "A", "value": "same"}, map[string]any{"label": "B", "value": "same"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(t.Context(), step.Request{Vars: map[string]any{"item": "same"}}); err == nil {
		t.Fatal("expected duplicate value error")
	}
}
