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
	if len(values) != 2 || values[0] != "frontend" || values[1] != "backend" || result.Outputs["count"] != 2 {
		t.Fatalf("values = %#v", values)
	}
}

func TestOptionalChoiceCanSelectNothing(t *testing.T) {
	runner, err := New(map[string]any{
		"variable": "item", "message": "Item", "required": false,
		"from": "steps.fetch.items",
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(t.Context(), step.Request{
		Steps: map[string]any{"fetch": map[string]any{"items": []any{"one"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outputs["selected"] != false || result.Outputs["value"] != nil || result.Outputs["label"] != "" {
		t.Fatalf("outputs = %#v", result.Outputs)
	}
	if value, exists := result.Variables["item"]; !exists || value != nil {
		t.Fatalf("variables = %#v", result.Variables)
	}

	multiple, err := New(map[string]any{
		"variable": "items", "message": "Items", "multiple": true, "required": false,
		"choices": []any{map[string]any{"label": "One", "value": "one"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err = multiple.Run(t.Context(), step.Request{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outputs["count"] != 0 || len(result.Outputs["values"].([]any)) != 0 {
		t.Fatalf("outputs = %#v", result.Outputs)
	}
}

func TestSingleNullChoiceIsDistinguishedFromNoSelection(t *testing.T) {
	runner, err := New(map[string]any{
		"variable": "item", "message": "Item", "required": false,
		"choices": []any{map[string]any{"label": "Null", "value": nil}},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(t.Context(), step.Request{Vars: map[string]any{"item": nil}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outputs["selected"] != true || result.Outputs["label"] != "Null" || result.Outputs["value"] != nil {
		t.Fatalf("outputs = %#v", result.Outputs)
	}
}

func TestDynamicChoiceDescriptions(t *testing.T) {
	runnerValue, err := New(map[string]any{
		"variable": "item", "message": "Item", "from": "steps.fetch.items",
		"label_field": "name", "value_field": "id", "description_field": "summary",
	})
	if err != nil {
		t.Fatal(err)
	}
	runner := runnerValue.(*Runner)
	options, err := runner.options(step.Request{Steps: map[string]any{"fetch": map[string]any{"items": []any{
		map[string]any{"name": "First", "id": "one", "summary": "Primary option"},
	}}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(options) != 1 || options[0].Description != "Primary option" {
		t.Fatalf("options = %#v", options)
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
