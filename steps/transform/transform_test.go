package transform

import (
	"context"
	"reflect"
	"testing"

	"github.com/up2jj/wuko/step"
)

func TestOperationManifestParses(t *testing.T) {
	for _, name := range OperationNames() {
		t.Run(name, func(t *testing.T) {
			raw := operationFixture(name)
			if _, err := parseOperation(0, raw); err != nil {
				t.Fatalf("parseOperation(%q): %v", name, err)
			}
		})
	}
}

func TestRegistrationExposesOnlyDynamicValue(t *testing.T) {
	registry := step.NewRegistry()
	if err := Register(registry); err != nil {
		t.Fatal(err)
	}
	schema, err := registry.OutputSchema(t.Context(), "transform")
	if err != nil {
		t.Fatal(err)
	}
	if schema.Kind != step.OutputObject || schema.Open || len(schema.Fields) != 1 {
		t.Fatalf("schema = %#v", schema)
	}
	value, exists := schema.Fields["value"]
	if !exists || value.Kind != step.OutputObject || !value.Open {
		t.Fatalf("value schema = %#v", value)
	}
}

func operationFixture(name string) any {
	if _, ok := zeroArgumentOperations[name]; ok {
		return name
	}
	if _, ok := itemExpressionOperations[name]; ok {
		return map[string]any{name: "item"}
	}
	if _, ok := itemPredicateOperations[name]; ok {
		return map[string]any{name: "true"}
	}
	if _, ok := comparatorOperations[name]; ok {
		return map[string]any{name: "true"}
	}
	if _, ok := objectPredicateOperations[name]; ok {
		return map[string]any{name: "true"}
	}
	if _, ok := objectExpressionOperations[name]; ok {
		return map[string]any{name: "value"}
	}
	if _, ok := operandOperations[name]; ok {
		value := any(1)
		switch name {
		case "drop_by_index", "without_nth", "every", "some", "none", "pick_keys", "pick_values", "omit_keys", "omit_values":
			value = []any{}
		case "has_key":
			value = "key"
		}
		return map[string]any{name: value}
	}
	switch name {
	case "filter_map", "reject_map":
		return map[string]any{name: map[string]any{"value": "item", "when": "true"}}
	case "group_by_map", "associate":
		return map[string]any{name: map[string]any{"key": `"key"`, "value": "item"}}
	case "filter_to_map":
		return map[string]any{name: map[string]any{"key": `"key"`, "value": "item", "when": "true"}}
	case "filter_reject":
		return map[string]any{name: "true"}
	case "take_filter":
		return map[string]any{name: map[string]any{"count": 1, "when": "true"}}
	case "sliding":
		return map[string]any{name: map[string]any{"size": 1, "step": 1}}
	case "subset":
		return map[string]any{name: map[string]any{"offset": 0, "length": 1}}
	case "slice":
		return map[string]any{name: map[string]any{"start": 0, "end": 1}}
	case "replace":
		return map[string]any{name: map[string]any{"old": 1, "new": 2, "count": 1}}
	case "replace_all":
		return map[string]any{name: map[string]any{"old": 1, "new": 2}}
	case "splice":
		return map[string]any{name: map[string]any{"index": 0, "elements": []any{1}}}
	case "trim", "trim_left", "trim_right":
		return map[string]any{name: map[string]any{"cutset": []any{}}}
	case "trim_prefix", "has_prefix", "cut_prefix":
		return map[string]any{name: map[string]any{"prefix": []any{}}}
	case "trim_suffix", "has_suffix", "cut_suffix":
		return map[string]any{name: map[string]any{"suffix": []any{}}}
	case "cut":
		return map[string]any{name: map[string]any{"separator": []any{}}}
	case "concat", "interleave", "union", "intersect", "zip", "cross_join", "assign":
		return map[string]any{name: map[string]any{"with": []any{map[string]any{"expr": "vars.other"}}}}
	case "difference", "elements_match":
		return map[string]any{name: map[string]any{"with": map[string]any{"expr": "vars.other"}}}
	case "intersect_by":
		return map[string]any{name: map[string]any{"with": []any{map[string]any{"expr": "vars.other"}}, "by": "item"}}
	case "elements_match_by":
		return map[string]any{name: map[string]any{"with": map[string]any{"expr": "vars.other"}, "by": "item"}}
	case "without":
		return map[string]any{name: map[string]any{"values": []any{}}}
	case "without_by":
		return map[string]any{name: map[string]any{"values": []any{}, "by": "item"}}
	case "find_or_else":
		return map[string]any{name: map[string]any{"when": "true", "fallback": nil}}
	case "nth_or":
		return map[string]any{name: map[string]any{"nth": 0, "fallback": nil}}
	case "sort":
		return name
	case "sort_by":
		return map[string]any{name: "item"}
	case "reduce", "reduce_right":
		return map[string]any{name: map[string]any{"initial": 0, "expr": "acc"}}
	case "value_or":
		return map[string]any{name: map[string]any{"key": "key", "fallback": nil}}
	case "map_keys":
		return map[string]any{name: `"key"`}
	case "map_entries":
		return map[string]any{name: map[string]any{"key": "key", "value": "value"}}
	case "filter_map_to_list":
		return map[string]any{name: map[string]any{"value": "value", "when": "true"}}
	case "zip_with", "cross_join_with":
		return map[string]any{name: map[string]any{"with": []any{map[string]any{"expr": "vars.other"}}, "expr": "items"}}
	default:
		panic("missing fixture for " + name)
	}
}

func TestListPipelineAndVariable(t *testing.T) {
	runner := buildRunner(t, map[string]any{
		"from": "vars.apps",
		"operations": []any{
			map[string]any{"filter": "item.live_tag != item.release_tag"},
			map[string]any{"map": `{name: item.name, release_tag: item.release_tag}`},
			map[string]any{"unique_by": "item.name"},
			map[string]any{"sort_by": "item.name"},
		},
		"variable": "changed_apps",
	})
	apps := []any{
		map[string]any{"name": "web", "live_tag": "v1", "release_tag": "v2"},
		map[string]any{"name": "api", "live_tag": "v2", "release_tag": "v2"},
		map[string]any{"name": "web", "live_tag": "v1", "release_tag": "v2"},
		map[string]any{"name": "worker", "live_tag": "v1", "release_tag": "v3"},
	}
	result, err := runner.Run(t.Context(), step.Request{Vars: map[string]any{"apps": apps}})
	if err != nil {
		t.Fatal(err)
	}
	want := []any{
		map[string]any{"name": "web", "release_tag": "v2"},
		map[string]any{"name": "worker", "release_tag": "v3"},
	}
	if !reflect.DeepEqual(result.Outputs["value"], want) {
		t.Fatalf("value = %#v, want %#v", result.Outputs["value"], want)
	}
	if !reflect.DeepEqual(result.Variables["changed_apps"], want) {
		t.Fatalf("variable = %#v, want %#v", result.Variables["changed_apps"], want)
	}
}

func TestObjectPipelineUsesSortedKeys(t *testing.T) {
	runner := buildRunner(t, map[string]any{
		"from": "vars.services",
		"operations": []any{
			map[string]any{"filter_map_to_list": map[string]any{"when": "value.enabled", "value": `{name: key, index: index}`}},
		},
	})
	result, err := runner.Run(t.Context(), step.Request{Vars: map[string]any{"services": map[string]any{
		"worker": map[string]any{"enabled": false},
		"web":    map[string]any{"enabled": true},
		"api":    map[string]any{"enabled": true},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	want := []any{map[string]any{"name": "api", "index": 0}, map[string]any{"name": "web", "index": 1}}
	if !reflect.DeepEqual(result.Outputs["value"], want) {
		t.Fatalf("value = %#v, want %#v", result.Outputs["value"], want)
	}
}

func TestNumericAndTupleOperations(t *testing.T) {
	t.Run("integer mean", func(t *testing.T) {
		runner := buildRunner(t, map[string]any{"from": "vars.values", "operations": []any{"mean"}})
		result, err := runner.Run(t.Context(), step.Request{Vars: map[string]any{"values": []any{1, 2, 5}}})
		if err != nil {
			t.Fatal(err)
		}
		if result.Outputs["value"] != 2 {
			t.Fatalf("mean = %#v, want 2", result.Outputs["value"])
		}
	})

	t.Run("zip with", func(t *testing.T) {
		runner := buildRunner(t, map[string]any{
			"from": "vars.apps",
			"operations": []any{map[string]any{"zip_with": map[string]any{
				"with": []any{map[string]any{"expr": "vars.tags"}},
				"expr": `{app: items[0], tag: items[1], index: index}`,
			}}},
		})
		result, err := runner.Run(t.Context(), step.Request{Vars: map[string]any{"apps": []any{"api", "web"}, "tags": []any{"v1"}}})
		if err != nil {
			t.Fatal(err)
		}
		want := []any{
			map[string]any{"app": "api", "tag": "v1", "index": 0},
			map[string]any{"app": "web", "tag": nil, "index": 1},
		}
		if !reflect.DeepEqual(result.Outputs["value"], want) {
			t.Fatalf("value = %#v, want %#v", result.Outputs["value"], want)
		}
	})
}

func TestStructuralEqualityAndTypedChaining(t *testing.T) {
	runner := buildRunner(t, map[string]any{
		"from": "vars.apps",
		"operations": []any{
			map[string]any{"unique_by": `{region: item.region, platform: item.platform}`},
			map[string]any{"group_by": "item.region"},
			"keys",
		},
	})
	result, err := runner.Run(t.Context(), step.Request{Vars: map[string]any{"apps": []any{
		map[string]any{"name": "api", "region": "eu", "platform": []any{"linux", "amd64"}},
		map[string]any{"name": "worker", "region": "eu", "platform": []any{"linux", "amd64"}},
		map[string]any{"name": "web", "region": "us", "platform": []any{"linux", "amd64"}},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if want := []any{"eu", "us"}; !reflect.DeepEqual(result.Outputs["value"], want) {
		t.Fatalf("value = %#v, want %#v", result.Outputs["value"], want)
	}
}

func TestRejectsOperationAfterTerminalResult(t *testing.T) {
	_, err := New(map[string]any{
		"from":       "vars.values",
		"operations": []any{"sum", map[string]any{"map": "item * 2"}},
	})
	if err == nil || !contains(err.Error(), "follows terminal operation") {
		t.Fatalf("error = %v", err)
	}
}

func TestReferencesExposeCallbackLocals(t *testing.T) {
	path, references, err := References(map[string]any{
		"from": "steps.release.results",
		"operations": []any{
			map[string]any{"filter": "item.enabled && vars.ready"},
			map[string]any{"take": map[string]any{"expr": "vars.limit"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if path != "steps.release.results" || len(references) != 2 {
		t.Fatalf("path = %q, references = %#v", path, references)
	}
	if !reflect.DeepEqual(references[0].Locals, []string{"index", "item"}) || len(references[1].Locals) != 0 {
		t.Fatalf("locals = %#v, %#v", references[0].Locals, references[1].Locals)
	}
}

func TestAggregationSemantics(t *testing.T) {
	tests := []struct {
		name      string
		operation string
		values    []any
		want      any
	}{
		{name: "empty sum", operation: "sum", values: []any{}, want: 0},
		{name: "empty mean", operation: "mean", values: []any{}, want: 0},
		{name: "empty product", operation: "product", values: []any{}, want: 1},
		{name: "empty mode", operation: "mode", values: []any{}, want: []any{}},
		{name: "floating mean", operation: "mean", values: []any{1.0, 2.0}, want: 1.5},
		{name: "integer product", operation: "product", values: []any{2, 3, 4}, want: 24},
		{name: "mode order", operation: "mode", values: []any{2, 1, 2, 1}, want: []any{2, 1}},
		{name: "mode follows frequency milestones", operation: "mode", values: []any{2, 1, 1, 2}, want: []any{1, 2}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner := buildRunner(t, map[string]any{"from": "vars.values", "operations": []any{test.operation}})
			result, err := runner.Run(t.Context(), step.Request{Vars: map[string]any{"values": test.values}})
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(result.Outputs["value"], test.want) {
				t.Fatalf("value = %#v, want %#v", result.Outputs["value"], test.want)
			}
		})
	}

	runner := buildRunner(t, map[string]any{"from": "vars.values", "operations": []any{"sum"}})
	if _, err := runner.Run(t.Context(), step.Request{Vars: map[string]any{"values": []any{1, 2.5}}}); err == nil || !contains(err.Error(), "mixes integer and floating-point") {
		t.Fatalf("mixed numeric error = %v", err)
	}
}

func TestStructuralEqualityTreatsEquivalentNumbersEqually(t *testing.T) {
	runner := buildRunner(t, map[string]any{"from": "vars.values", "operations": []any{"unique"}})
	result, err := runner.Run(t.Context(), step.Request{Vars: map[string]any{"values": []any{
		map[string]any{"nested": []any{1}},
		map[string]any{"nested": []any{1.0}},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if values := result.Outputs["value"].([]any); len(values) != 1 {
		t.Fatalf("value = %#v, want one structurally unique value", values)
	}
}

func TestStableSortAndStageReindexing(t *testing.T) {
	runner := buildRunner(t, map[string]any{
		"from": "vars.values",
		"operations": []any{
			map[string]any{"filter": "item.keep"},
			map[string]any{"map": `{id: item.id, stage_index: index, rank: item.rank}`},
			map[string]any{"sort_by": "item.rank"},
		},
	})
	result, err := runner.Run(t.Context(), step.Request{Vars: map[string]any{"values": []any{
		map[string]any{"id": "skip", "keep": false, "rank": 0},
		map[string]any{"id": "a", "keep": true, "rank": 1},
		map[string]any{"id": "b", "keep": true, "rank": 1},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	want := []any{
		map[string]any{"id": "a", "stage_index": 0, "rank": 1},
		map[string]any{"id": "b", "stage_index": 1, "rank": 1},
	}
	if !reflect.DeepEqual(result.Outputs["value"], want) {
		t.Fatalf("value = %#v, want %#v", result.Outputs["value"], want)
	}
}

func TestReduceRightUsesOriginalIndexes(t *testing.T) {
	runner := buildRunner(t, map[string]any{
		"from": "vars.values",
		"operations": []any{map[string]any{"reduce_right": map[string]any{
			"initial": "",
			"expr":    `acc + string(index) + ":" + item`,
		}}},
	})
	result, err := runner.Run(t.Context(), step.Request{Vars: map[string]any{"values": []any{"a", "b", "c"}}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outputs["value"] != "2:c1:b0:a" {
		t.Fatalf("value = %#v", result.Outputs["value"])
	}
}

func TestObjectCombinationCollisionAndRoundTrip(t *testing.T) {
	runner := buildRunner(t, map[string]any{
		"from": "vars.base",
		"operations": []any{
			map[string]any{"assign": map[string]any{"with": []any{map[string]any{"expr": "vars.overlay"}}}},
			map[string]any{"map_entries": map[string]any{"key": `"same"`, "value": `{source: key, value: value}`}},
			"entries",
			"from_entries",
		},
	})
	result, err := runner.Run(t.Context(), step.Request{Vars: map[string]any{
		"base":    map[string]any{"b": 2, "a": 1},
		"overlay": map[string]any{"b": 20, "c": 3},
	}})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"same": map[string]any{"source": "c", "value": 3}}
	if !reflect.DeepEqual(result.Outputs["value"], want) {
		t.Fatalf("value = %#v, want %#v", result.Outputs["value"], want)
	}
}

func TestMultiObjectExtractionOrder(t *testing.T) {
	runner := buildRunner(t, map[string]any{
		"from": "vars.first",
		"operations": []any{map[string]any{"keys": map[string]any{"with": []any{
			map[string]any{"expr": "vars.second"},
		}}}},
	})
	result, err := runner.Run(t.Context(), step.Request{Vars: map[string]any{
		"first":  map[string]any{"b": 2, "a": 1},
		"second": map[string]any{"d": 4, "c": 3},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if want := []any{"a", "b", "c", "d"}; !reflect.DeepEqual(result.Outputs["value"], want) {
		t.Fatalf("value = %#v, want %#v", result.Outputs["value"], want)
	}
}

func TestTupleOperations(t *testing.T) {
	t.Run("unzip validation", func(t *testing.T) {
		runner := buildRunner(t, map[string]any{"from": "vars.rows", "operations": []any{"unzip"}})
		_, err := runner.Run(t.Context(), step.Request{Vars: map[string]any{"rows": []any{[]any{1, 2}, []any{3}}}})
		if err == nil || !contains(err.Error(), "tuple width") {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("cartesian order", func(t *testing.T) {
		runner := buildRunner(t, map[string]any{
			"from": "vars.left",
			"operations": []any{map[string]any{"cross_join": map[string]any{"with": []any{
				map[string]any{"expr": "vars.right"},
			}}}},
		})
		result, err := runner.Run(t.Context(), step.Request{Vars: map[string]any{"left": []any{"a", "b"}, "right": []any{1, 2}}})
		if err != nil {
			t.Fatal(err)
		}
		want := []any{[]any{"a", 1}, []any{"a", 2}, []any{"b", 1}, []any{"b", 2}}
		if !reflect.DeepEqual(result.Outputs["value"], want) {
			t.Fatalf("value = %#v, want %#v", result.Outputs["value"], want)
		}
	})
}

func TestNamedTerminalRecordsAndOnlyValueOutput(t *testing.T) {
	runner := buildRunner(t, map[string]any{
		"from":       "vars.values",
		"operations": []any{map[string]any{"find_index": "item == 2"}},
	})
	result, err := runner.Run(t.Context(), step.Request{Vars: map[string]any{"values": []any{1, 2, 3}}})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"value": 2, "index": 1, "found": true}
	if !reflect.DeepEqual(result.Outputs, map[string]any{"value": want}) {
		t.Fatalf("outputs = %#v", result.Outputs)
	}
}

func TestSourceExpressionAndRuntimeKindValidation(t *testing.T) {
	runner := buildRunner(t, map[string]any{
		"from":       map[string]any{"expr": "vars.values"},
		"operations": []any{map[string]any{"map": "item * 2"}},
	})
	result, err := runner.Run(t.Context(), step.Request{Vars: map[string]any{"values": []any{2, 3}}})
	if err != nil {
		t.Fatal(err)
	}
	if want := []any{4, 6}; !reflect.DeepEqual(result.Outputs["value"], want) {
		t.Fatalf("value = %#v, want %#v", result.Outputs["value"], want)
	}

	wrongKind := buildRunner(t, map[string]any{"from": "vars.value", "operations": []any{"unique"}})
	if _, err := wrongKind.Run(t.Context(), step.Request{Vars: map[string]any{"value": "scalar"}}); err == nil || !contains(err.Error(), "cannot consume scalar") {
		t.Fatalf("error = %v", err)
	}
}

func TestResultNormalizesNestedTypedCollections(t *testing.T) {
	runner := buildRunner(t, map[string]any{"from": "vars.values", "operations": []any{"clone"}})
	result, err := runner.Run(t.Context(), step.Request{Vars: map[string]any{
		"values": []map[string][]int{{"ports": {80, 443}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	want := []any{map[string]any{"ports": []any{80, 443}}}
	if !reflect.DeepEqual(result.Outputs["value"], want) {
		t.Fatalf("value = %#v, want %#v", result.Outputs["value"], want)
	}
}

func TestConfigurationAndCancellationErrors(t *testing.T) {
	tests := []struct {
		name string
		raw  map[string]any
		want string
	}{
		{name: "invalid source root", raw: map[string]any{"from": "inputs.values", "operations": []any{"unique"}}, want: "steps. or vars."},
		{name: "empty operations", raw: map[string]any{"from": "vars.values", "operations": []any{}}, want: "at least one"},
		{name: "unknown operation", raw: map[string]any{"from": "vars.values", "operations": []any{"mop"}}, want: "unknown operation"},
		{name: "unknown field", raw: map[string]any{"from": "vars.values", "operations": []any{map[string]any{"slice": map[string]any{"start": 0, "end": 1, "extra": true}}}}, want: "unknown argument"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := New(test.raw); err == nil || !contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}

	runner := buildRunner(t, map[string]any{"from": "vars.values", "operations": []any{"unique"}})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := runner.Run(ctx, step.Request{Vars: map[string]any{"values": []any{1}}}); err != context.Canceled {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

// A when guard exists so the value expression never sees the items it excludes.
func TestFilteringOperationsGuardTheirValueExpressions(t *testing.T) {
	list := []any{map[string]any{"name": "a", "tag": nil}, map[string]any{"name": "b", "tag": map[string]any{"id": "ok"}}}
	tests := []struct {
		name      string
		operation any
		source    any
		want      any
	}{
		{
			name:      "filter_map",
			operation: map[string]any{"filter_map": map[string]any{"value": "item.tag.id", "when": "item.tag != nil"}},
			source:    list,
			want:      []any{"ok"},
		},
		{
			name:      "reject_map",
			operation: map[string]any{"reject_map": map[string]any{"value": "item.tag.id", "when": "item.tag == nil"}},
			source:    list,
			want:      []any{"ok"},
		},
		{
			name:      "filter_to_map",
			operation: map[string]any{"filter_to_map": map[string]any{"key": "item.name", "value": "item.tag.id", "when": "item.tag != nil"}},
			source:    list,
			want:      map[string]any{"b": "ok"},
		},
		{
			name:      "filter_map_to_list",
			operation: map[string]any{"filter_map_to_list": map[string]any{"value": "value.tag.id", "when": "value.tag != nil"}},
			source:    map[string]any{"a": map[string]any{"tag": nil}, "b": map[string]any{"tag": map[string]any{"id": "ok"}}},
			want:      []any{"ok"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner := buildRunner(t, map[string]any{"from": "vars.values", "operations": []any{test.operation}})
			result, err := runner.Run(t.Context(), step.Request{Vars: map[string]any{"values": test.source}})
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(result.Outputs["value"], test.want) {
				t.Fatalf("value = %#v, want %#v", result.Outputs["value"], test.want)
			}
		})
	}
}

// The Cartesian product is the product of its input lengths, so it has to stay
// cancellable while it builds rather than only between operations.
func TestCrossJoinStopsOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	lists := [][]any{{1, 2}, {3, 4}}
	if _, err := crossJoinLists(ctx, lists); err != context.Canceled {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	rows, err := crossJoinLists(t.Context(), lists)
	if err != nil || len(rows) != 4 {
		t.Fatalf("rows = %v, err = %v", rows, err)
	}
}

// The expression environment is shared across a run, so one operation's callback
// locals must not still be bound while the next operation's callbacks evaluate.
func TestCallbackLocalsDoNotLeakBetweenOperations(t *testing.T) {
	runner := buildRunner(t, map[string]any{"from": "vars.values", "operations": []any{
		map[string]any{"group_by": "item.region"},
		map[string]any{"map_values": "item"},
	}})
	result, err := runner.Run(t.Context(), step.Request{Vars: map[string]any{"values": []any{map[string]any{"region": "eu"}}}})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result.Outputs["value"], map[string]any{"eu": nil}) {
		t.Fatalf("value = %#v, want the list local to be unbound", result.Outputs["value"])
	}
}

func buildRunner(t *testing.T, raw map[string]any) *Runner {
	t.Helper()
	runner, err := New(raw)
	if err != nil {
		t.Fatal(err)
	}
	return runner.(*Runner)
}

func contains(value, substring string) bool {
	for index := 0; index+len(substring) <= len(value); index++ {
		if value[index:index+len(substring)] == substring {
			return true
		}
	}
	return false
}
