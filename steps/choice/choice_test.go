package choice

import (
	"io"
	"reflect"
	"strings"
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
			map[string]any{"name": "Backend", "id": "backend", "namespace": "services"},
			map[string]any{"name": "Frontend", "id": "frontend", "namespace": "web"},
		}}}, Stdout: io.Discard, Stderr: io.Discard,
	})
	if err != nil {
		t.Fatal(err)
	}
	values := result.Outputs["values"].([]any)
	if len(values) != 2 || values[0] != "frontend" || values[1] != "backend" || result.Outputs["count"] != 2 {
		t.Fatalf("values = %#v", values)
	}
	items := result.Outputs["items"].([]any)
	if len(items) != 2 || items[0].(map[string]any)["namespace"] != "web" || items[1].(map[string]any)["namespace"] != "services" {
		t.Fatalf("items = %#v", items)
	}
}

func TestDynamicObjectSelectionPreservesClonedItem(t *testing.T) {
	source := map[string]any{
		"label": "API", "value": "default/api", "namespace": "default",
		"commands": map[string]any{"shell": "/bin/bash"},
	}
	runnerValue, err := New(map[string]any{
		"variable": "service", "message": "Service", "from": "steps.fetch.services",
	})
	if err != nil {
		t.Fatal(err)
	}
	runner := runnerValue.(*Runner)
	options, err := runner.options(step.Request{Steps: map[string]any{"fetch": map[string]any{
		"services": []any{source},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	result := runner.selected([]int{0}, options)
	if result.Outputs["value"] != "default/api" || result.Outputs["label"] != "API" {
		t.Fatalf("outputs = %#v", result.Outputs)
	}
	item := result.Outputs["item"].(map[string]any)
	if !reflect.DeepEqual(item, source) {
		t.Fatalf("item = %#v, want %#v", item, source)
	}
	item["commands"].(map[string]any)["shell"] = "/bin/sh"
	if source["commands"].(map[string]any)["shell"] != "/bin/bash" {
		t.Fatalf("source was mutated through retained item: %#v", source)
	}
}

func TestDynamicMixedSelectionAlignsItemsWithValues(t *testing.T) {
	runner, err := New(map[string]any{
		"variable": "services", "message": "Services", "multiple": true,
		"from": "steps.fetch.services",
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(t.Context(), step.Request{
		Vars: map[string]any{"services": []any{"manual", "default/api"}},
		Steps: map[string]any{"fetch": map[string]any{"services": []any{
			"manual",
			map[string]any{"label": "API", "value": "default/api", "namespace": "default"},
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	items := result.Outputs["items"].([]any)
	if len(items) != 2 || items[0] != nil || items[1].(map[string]any)["namespace"] != "default" {
		t.Fatalf("items = %#v", items)
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
	if _, exists := result.Outputs["item"]; exists {
		t.Fatalf("scalar source unexpectedly exposed item: %#v", result.Outputs)
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
	if _, exists := result.Outputs["items"]; exists {
		t.Fatalf("static choices unexpectedly exposed items: %#v", result.Outputs)
	}
}

func TestOptionalObjectChoicesExposeEmptyItemOutputs(t *testing.T) {
	source := []any{map[string]any{"label": "One", "value": "one"}}
	tests := []struct {
		name     string
		multiple bool
	}{
		{name: "single"},
		{name: "multiple", multiple: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner, err := New(map[string]any{
				"variable": "selection", "message": "Selection", "required": false,
				"multiple": tt.multiple, "from": "steps.fetch.items",
			})
			if err != nil {
				t.Fatal(err)
			}
			result, err := runner.Run(t.Context(), step.Request{Steps: map[string]any{
				"fetch": map[string]any{"items": source},
			}})
			if err != nil {
				t.Fatal(err)
			}
			if tt.multiple {
				items, exists := result.Outputs["items"].([]any)
				if !exists || len(items) != 0 {
					t.Fatalf("items = %#v", result.Outputs["items"])
				}
				return
			}
			if item, exists := result.Outputs["item"]; !exists || item != nil {
				t.Fatalf("item = %#v, exists = %v", item, exists)
			}
		})
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

func TestDynamicChoiceRejectsDuplicateMappedValues(t *testing.T) {
	runner, err := New(map[string]any{
		"variable": "item", "message": "Item", "from": "steps.fetch.items",
		"label_field": "name", "value_field": "id",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.Run(t.Context(), step.Request{
		Vars: map[string]any{"item": "same"},
		Steps: map[string]any{"fetch": map[string]any{"items": []any{
			map[string]any{"name": "A", "id": "same"},
			map[string]any{"name": "B", "id": "same"},
		}}},
	})
	if err == nil || !strings.Contains(err.Error(), `duplicate choice value "same"`) {
		t.Fatalf("error = %v", err)
	}
}

func TestDynamicChoiceMetadata(t *testing.T) {
	runnerValue, err := New(map[string]any{
		"variable": "item", "message": "Item", "from": "steps.fetch.items",
		"label_field": "name", "value_field": "id", "disabled_field": "state.disabled",
		"reason_field": "state.reason", "default_field": "preferred",
	})
	if err != nil {
		t.Fatal(err)
	}
	runner := runnerValue.(*Runner)
	options, err := runner.options(step.Request{Steps: map[string]any{"fetch": map[string]any{"items": []any{
		map[string]any{"name": "First", "id": "one", "preferred": false, "state": map[string]any{"disabled": true, "reason": "archived"}},
		map[string]any{"name": "Second", "id": "two", "preferred": true, "state": map[string]any{"disabled": false}},
	}}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(options) != 2 || !options[0].Disabled || options[0].DisabledReason != "archived" || !options[1].Default {
		t.Fatalf("options = %#v", options)
	}
}

func TestDynamicChoiceMetadataValidation(t *testing.T) {
	tests := []struct {
		name string
		raw  map[string]any
		item map[string]any
		want string
	}{
		{
			name: "disabled must be boolean",
			raw:  map[string]any{"disabled_field": "disabled"},
			item: map[string]any{"name": "A", "id": "a", "disabled": "yes"},
			want: "must be a boolean",
		},
		{
			name: "default must be boolean",
			raw:  map[string]any{"default_field": "default"},
			item: map[string]any{"name": "A", "id": "a", "default": 1},
			want: "must be a boolean",
		},
		{
			name: "disabled requires reason field",
			raw:  map[string]any{"disabled_field": "disabled"},
			item: map[string]any{"name": "A", "id": "a", "disabled": true},
			want: "without a reason field",
		},
		{
			name: "reason must be non-empty string",
			raw:  map[string]any{"disabled_field": "disabled", "reason_field": "reason"},
			item: map[string]any{"name": "A", "id": "a", "disabled": true, "reason": "  "},
			want: "reason must be a non-empty string",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := map[string]any{
				"variable": "item", "message": "Item", "from": "steps.fetch.items",
				"label_field": "name", "value_field": "id",
			}
			for key, value := range tt.raw {
				raw[key] = value
			}
			runnerValue, err := New(raw)
			if err != nil {
				t.Fatal(err)
			}
			runner := runnerValue.(*Runner)
			_, err = runner.options(step.Request{Steps: map[string]any{"fetch": map[string]any{"items": []any{tt.item}}}})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestChoiceConfigurationValidation(t *testing.T) {
	tests := []struct {
		name string
		raw  map[string]any
	}{
		{
			name: "bounds require multiple",
			raw: map[string]any{"variable": "item", "message": "Item", "min_selected": 1,
				"choices": []any{map[string]any{"label": "A", "value": "a"}}},
		},
		{
			name: "negative maximum",
			raw: map[string]any{"variable": "item", "message": "Item", "multiple": true, "max_selected": -1,
				"choices": []any{map[string]any{"label": "A", "value": "a"}}},
		},
		{
			name: "inverted bounds",
			raw: map[string]any{"variable": "item", "message": "Item", "multiple": true, "min_selected": 2, "max_selected": 1,
				"choices": []any{map[string]any{"label": "A", "value": "a"}}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := New(tt.raw); err == nil {
				t.Fatal("expected configuration error")
			}
		})
	}
}

func TestChoiceOptionValidation(t *testing.T) {
	tests := []struct {
		name string
		raw  map[string]any
		want string
	}{
		{
			name: "disabled without reason",
			raw: map[string]any{"variable": "item", "message": "Item", "choices": []any{
				map[string]any{"label": "A", "value": "a", "disabled": true},
			}},
			want: "disabled without a reason",
		},
		{
			name: "disabled default",
			raw: map[string]any{"variable": "item", "message": "Item", "choices": []any{
				map[string]any{"label": "A", "value": "a", "disabled": true, "reason": "no", "default": true},
			}},
			want: "both disabled and default",
		},
		{
			name: "multiple defaults in single mode",
			raw: map[string]any{"variable": "item", "message": "Item", "choices": []any{
				map[string]any{"label": "A", "value": "a", "default": true},
				map[string]any{"label": "B", "value": "b", "default": true},
			}},
			want: "at most one default",
		},
		{
			name: "minimum exceeds enabled",
			raw: map[string]any{"variable": "item", "message": "Item", "multiple": true, "min_selected": 1, "choices": []any{
				map[string]any{"label": "A", "value": "a", "disabled": true, "reason": "no"},
			}},
			want: "exceeds 0 enabled choices",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner, err := New(tt.raw)
			if err != nil {
				t.Fatal(err)
			}
			_, err = runner.Run(t.Context(), step.Request{Vars: map[string]any{"item": []any{}}})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestChoiceRejectsDisabledPreSuppliedValue(t *testing.T) {
	runner, err := New(map[string]any{
		"variable": "item", "message": "Item",
		"choices": []any{
			map[string]any{"label": "A", "value": "a", "disabled": true, "reason": "retired"},
			map[string]any{"label": "B", "value": "b"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.Run(t.Context(), step.Request{Vars: map[string]any{"item": "a"}})
	if err == nil || !strings.Contains(err.Error(), "disabled choice") || !strings.Contains(err.Error(), "retired") {
		t.Fatalf("error = %v", err)
	}
}

func TestChoiceBoundsApplyAfterDeduplication(t *testing.T) {
	runner, err := New(map[string]any{
		"variable": "items", "message": "Items", "multiple": true, "min_selected": 2, "max_selected": 2,
		"choices": []any{
			map[string]any{"label": "A", "value": "a"},
			map[string]any{"label": "B", "value": "b"},
			map[string]any{"label": "C", "value": "c"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.Run(t.Context(), step.Request{Vars: map[string]any{"items": []any{"a", "a"}}})
	if err == nil || !strings.Contains(err.Error(), "at least 2") {
		t.Fatalf("deduplicated minimum error = %v", err)
	}
	_, err = runner.Run(t.Context(), step.Request{Vars: map[string]any{"items": []any{"a", "b", "c"}}})
	if err == nil || !strings.Contains(err.Error(), "at most 2") {
		t.Fatalf("maximum error = %v", err)
	}
}

func TestChoiceDefaultsAreInteractiveOnly(t *testing.T) {
	runner, err := New(map[string]any{
		"variable": "items", "message": "Items", "multiple": true, "required": false,
		"choices": []any{map[string]any{"label": "A", "value": "a", "default": true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(t.Context(), step.Request{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outputs["count"] != 0 || len(result.Outputs["values"].([]any)) != 0 {
		t.Fatalf("outputs = %#v", result.Outputs)
	}
}

func TestChoiceSelectAllIsInteractiveOnly(t *testing.T) {
	runner, err := New(map[string]any{
		"variable": "items", "message": "Items", "multiple": true, "required": false, "select_all": true,
		"choices": []any{
			map[string]any{"label": "A", "value": "a"},
			map[string]any{"label": "B", "value": "b"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := runner.Run(t.Context(), step.Request{Vars: map[string]any{"items": []any{"b"}}})
	if err != nil {
		t.Fatal(err)
	}
	if values := result.Outputs["values"].([]any); len(values) != 1 || values[0] != "b" {
		t.Fatalf("pre-supplied values = %#v", values)
	}

	result, err = runner.Run(t.Context(), step.Request{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outputs["count"] != 0 || len(result.Outputs["values"].([]any)) != 0 {
		t.Fatalf("non-interactive values = %#v", result.Outputs)
	}
}

func TestChoiceSelectAllRequiresMultiple(t *testing.T) {
	_, err := New(map[string]any{
		"variable": "item", "message": "Item", "select_all": true,
		"choices": []any{map[string]any{"label": "A", "value": "a"}},
	})
	if err == nil || !strings.Contains(err.Error(), "select_all requires multiple") {
		t.Fatalf("error = %v", err)
	}
}

func TestChoiceSelectAllRejectsSmallMaximum(t *testing.T) {
	runner, err := New(map[string]any{
		"variable": "items", "message": "Items", "multiple": true, "select_all": true, "max_selected": 1,
		"choices": []any{
			map[string]any{"label": "A", "value": "a"},
			map[string]any{"label": "B", "value": "b"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.Run(t.Context(), step.Request{})
	if err == nil || !strings.Contains(err.Error(), "select all would select 2 choices") {
		t.Fatalf("error = %v", err)
	}
}

func TestChoiceExplicitBoundsSupersedeRequired(t *testing.T) {
	runner, err := New(map[string]any{
		"variable": "items", "message": "Items", "multiple": true, "max_selected": 0,
		"choices": []any{map[string]any{"label": "A", "value": "a"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(t.Context(), step.Request{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outputs["count"] != 0 {
		t.Fatalf("outputs = %#v", result.Outputs)
	}
}
