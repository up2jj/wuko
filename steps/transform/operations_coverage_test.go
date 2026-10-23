package transform

import (
	"reflect"
	"testing"

	"github.com/up2jj/wuko/step"
)

// TestOperationsProduceExpectedValues drives operations that previously had no
// direct coverage through the real runner, so parsing, kind checks, execution,
// and result normalization are all exercised together.
func TestOperationsProduceExpectedValues(t *testing.T) {
	objectSource := map[string]any{"b": 2, "a": 1, "c": 3}
	tests := []struct {
		name      string
		operation any
		source    any
		want      any
		wantErr   string
	}{
		{name: "chunk splits with a short tail", operation: map[string]any{"chunk": 2}, source: []any{1, 2, 3, 4, 5}, want: []any{[]any{1, 2}, []any{3, 4}, []any{5}}},
		{name: "chunk of empty list", operation: map[string]any{"chunk": 2}, source: []any{}, want: []any{}},
		{name: "chunk rejects zero size", operation: map[string]any{"chunk": 0}, source: []any{1}, wantErr: "size must be a positive integer"},

		{name: "chunk_entries groups sorted keys", operation: map[string]any{"chunk_entries": 2}, source: objectSource, want: []any{map[string]any{"a": 1, "b": 2}, map[string]any{"c": 3}}},
		{name: "chunk_entries of empty object", operation: map[string]any{"chunk_entries": 2}, source: map[string]any{}, want: []any{}},
		{name: "chunk_entries rejects non-integer size", operation: map[string]any{"chunk_entries": "two"}, source: objectSource, wantErr: "size must be a positive integer"},

		{name: "compact removes zero values", operation: "compact", source: []any{0, 1, "", "a", nil, false, true, 0.0, 2.5}, want: []any{1, "a", true, 2.5}},
		{name: "compact keeps empty collections", operation: "compact", source: []any{[]any{}, map[string]any{}}, want: []any{[]any{}, map[string]any{}}},

		{name: "contains finds structural match", operation: map[string]any{"contains": map[string]any{"literal": map[string]any{"id": 2}}}, source: []any{map[string]any{"id": 1}, map[string]any{"id": 2}}, want: true},
		{name: "contains reports missing value", operation: map[string]any{"contains": "z"}, source: []any{"a", "b"}, want: false},
		{name: "contains on empty list", operation: map[string]any{"contains": 1}, source: []any{}, want: false},

		{name: "contains_by matches predicate", operation: map[string]any{"contains_by": "item > 2"}, source: []any{1, 2, 3}, want: true},
		{name: "contains_by on empty list", operation: map[string]any{"contains_by": "item > 2"}, source: []any{}, want: false},

		{name: "count_by counts matches", operation: map[string]any{"count_by": "item % 2 == 0"}, source: []any{1, 2, 3, 4, 6}, want: 3},
		{name: "count_by on empty list", operation: map[string]any{"count_by": "item > 0"}, source: []any{}, want: 0},

		{name: "count_values tallies strings", operation: "count_values", source: []any{"a", "b", "a", "a"}, want: map[string]any{"a": 3, "b": 1}},
		{name: "count_values of empty list", operation: "count_values", source: []any{}, want: map[string]any{}},
		{name: "count_values rejects non-string items", operation: "count_values", source: []any{"a", 1}, wantErr: "must return a string object key"},

		{name: "drop removes leading items", operation: map[string]any{"drop": 2}, source: []any{1, 2, 3, 4}, want: []any{3, 4}},
		{name: "drop rejects negative count", operation: map[string]any{"drop": -1}, source: []any{1}, wantErr: "drop must be a non-negative integer"},

		{name: "drop_while stops at first mismatch", operation: map[string]any{"drop_while": "item < 3"}, source: []any{1, 2, 3, 1, 4}, want: []any{3, 1, 4}},

		// Operations that copy a range of the list must still return [] rather than null
		// when the range is empty.
		{name: "drop past the end is empty", operation: map[string]any{"drop": 10}, source: []any{1, 2}, want: []any{}},
		{name: "drop_while dropping everything is empty", operation: map[string]any{"drop_while": "true"}, source: []any{1, 2}, want: []any{}},
		{name: "take zero is empty", operation: map[string]any{"take": 0}, source: []any{1, 2}, want: []any{}},
		{name: "drop_right everything is empty", operation: map[string]any{"drop_right": 5}, source: []any{1, 2}, want: []any{}},
		{name: "take_while matching nothing is empty", operation: map[string]any{"take_while": "false"}, source: []any{1, 2}, want: []any{}},
		{name: "clone of empty list", operation: "clone", source: []any{}, want: []any{}},

		{name: "every_by all match", operation: map[string]any{"every_by": "item > 0"}, source: []any{1, 2, 3}, want: true},
		{name: "every_by one mismatch", operation: map[string]any{"every_by": "item > 1"}, source: []any{1, 2, 3}, want: false},
		{name: "every_by is vacuously true on empty list", operation: map[string]any{"every_by": "false"}, source: []any{}, want: true},

		{name: "fill replaces every item", operation: map[string]any{"fill": map[string]any{"literal": map[string]any{"x": 0}}}, source: []any{1, 2}, want: []any{map[string]any{"x": 0}, map[string]any{"x": 0}}},
		{name: "fill of empty list", operation: map[string]any{"fill": 7}, source: []any{}, want: []any{}},

		{name: "filter_keys returns sorted matching keys", operation: map[string]any{"filter_keys": "value >= 2"}, source: objectSource, want: []any{"b", "c"}},
		{name: "filter_keys can match by key", operation: map[string]any{"filter_keys": `key == "a"`}, source: objectSource, want: []any{"a"}},
		{name: "filter_keys of empty object", operation: map[string]any{"filter_keys": "true"}, source: map[string]any{}, want: []any{}},

		{name: "find returns first match", operation: map[string]any{"find": "item > 1"}, source: []any{1, 2, 3}, want: map[string]any{"value": 2, "found": true}},
		{name: "find without match", operation: map[string]any{"find": "item > 5"}, source: []any{1, 2, 3}, want: map[string]any{"value": nil, "found": false}},

		{name: "find_duplicates keeps first occurrence order", operation: "find_duplicates", source: []any{1, 2, 1, 3, 2, 1}, want: []any{1, 2}},
		{name: "find_duplicates without duplicates", operation: "find_duplicates", source: []any{1, 2, 3}, want: []any{}},

		{name: "find_key returns first sorted key with value", operation: map[string]any{"find_key": 2}, source: map[string]any{"z": 2, "y": 2, "x": 1}, want: map[string]any{"key": "y", "found": true}},
		{name: "find_key missing value", operation: map[string]any{"find_key": 9}, source: objectSource, want: map[string]any{"key": "", "found": false}},

		{name: "find_key_by matches predicate", operation: map[string]any{"find_key_by": "value > 1"}, source: objectSource, want: map[string]any{"key": "b", "found": true}},
		{name: "find_key_by without match", operation: map[string]any{"find_key_by": "value > 10"}, source: objectSource, want: map[string]any{"key": "", "found": false}},

		{name: "find_uniques keeps singletons", operation: "find_uniques", source: []any{1, 2, 1, 3, "a"}, want: []any{2, 3, "a"}},
		{name: "find_uniques of empty list", operation: "find_uniques", source: []any{}, want: []any{}},

		{name: "first_or returns first item", operation: map[string]any{"first_or": "fallback"}, source: []any{"x", "y"}, want: "x"},
		{name: "first_or falls back on empty list", operation: map[string]any{"first_or": "fallback"}, source: []any{}, want: "fallback"},

		{name: "first_or_empty returns first item", operation: "first_or_empty", source: []any{"x", "y"}, want: "x"},
		{name: "first_or_empty returns null on empty list", operation: "first_or_empty", source: []any{}, want: nil},

		{name: "flat_map flattens returned lists", operation: map[string]any{"flat_map": "[item, item * 10]"}, source: []any{1, 2}, want: []any{1, 10, 2, 20}},
		{name: "flat_map rejects non-list result", operation: map[string]any{"flat_map": "item"}, source: []any{1}, wantErr: "expression must return a list"},

		{name: "flatten one level", operation: "flatten", source: []any{[]any{1, 2}, []any{}, []any{[]any{3}}}, want: []any{1, 2, []any{3}}},
		{name: "flatten rejects scalar items", operation: "flatten", source: []any{[]any{1}, 2}, wantErr: "item 1: expected list"},

		{name: "index_of finds first position", operation: map[string]any{"index_of": "b"}, source: []any{"a", "b", "b"}, want: 1},
		{name: "index_of missing value", operation: map[string]any{"index_of": "z"}, source: []any{"a", "b"}, want: -1},

		{name: "invert swaps keys and values", operation: "invert", source: map[string]any{"a": "x", "b": "y"}, want: map[string]any{"x": "a", "y": "b"}},
		{name: "invert keeps last sorted key on collision", operation: "invert", source: map[string]any{"a": "x", "b": "x"}, want: map[string]any{"x": "b"}},
		{name: "invert rejects non-string values", operation: "invert", source: map[string]any{"a": 1}, wantErr: "must return a string object key"},

		{name: "is_sorted ascending", operation: "is_sorted", source: []any{1, 2, 2, 3}, want: true},
		{name: "is_sorted unsorted", operation: "is_sorted", source: []any{"b", "a"}, want: false},
		{name: "is_sorted empty list", operation: "is_sorted", source: []any{}, want: true},
		{name: "is_sorted rejects mixed types", operation: "is_sorted", source: []any{1, "a"}, wantErr: "cannot compare"},

		{name: "keyify builds a set object", operation: "keyify", source: []any{"a", "b", "a"}, want: map[string]any{"a": map[string]any{}, "b": map[string]any{}}},
		{name: "keyify rejects non-string items", operation: "keyify", source: []any{"a", 1}, wantErr: "must return a string object key"},

		{name: "map_to_list maps in sorted key order", operation: map[string]any{"map_to_list": `key + "=" + string(value)`}, source: objectSource, want: []any{"a=1", "b=2", "c=3"}},
		{name: "map_to_list exposes index", operation: map[string]any{"map_to_list": "index"}, source: objectSource, want: []any{0, 1, 2}},
		{name: "map_to_list of empty object", operation: map[string]any{"map_to_list": "value"}, source: map[string]any{}, want: []any{}},

		{name: "min of integers", operation: "min", source: []any{3, 1, 2}, want: 1},
		{name: "min of strings", operation: "min", source: []any{"b", "a", "c"}, want: "a"},
		{name: "min of empty list is null", operation: "min", source: []any{}, want: nil},
		{name: "min rejects mixed types", operation: "min", source: []any{1, "a"}, wantErr: "cannot compare"},

		{name: "pick keeps matching entries", operation: map[string]any{"pick": "value != 2"}, source: objectSource, want: map[string]any{"a": 1, "c": 3}},
		{name: "pick with no matches", operation: map[string]any{"pick": "false"}, source: objectSource, want: map[string]any{}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner := buildRunner(t, map[string]any{"from": "vars.values", "operations": []any{test.operation}})
			result, err := runner.Run(t.Context(), step.Request{Vars: map[string]any{"values": test.source}})
			if test.wantErr != "" {
				if err == nil || !contains(err.Error(), test.wantErr) {
					t.Fatalf("error = %v, want %q", err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(result.Outputs["value"], test.want) {
				t.Fatalf("value = %#v, want %#v", result.Outputs["value"], test.want)
			}
		})
	}
}

// An operation that consumes one collection kind must refuse the other rather
// than silently coercing it.
func TestOperationsRejectWrongInputKind(t *testing.T) {
	tests := []struct {
		name      string
		operation any
		source    any
	}{
		{name: "chunk on object", operation: map[string]any{"chunk": 1}, source: map[string]any{"a": 1}},
		{name: "min on object", operation: "min", source: map[string]any{"a": 1}},
		{name: "pick on list", operation: map[string]any{"pick": "true"}, source: []any{1}},
		{name: "invert on list", operation: "invert", source: []any{"a"}},
		{name: "compact on scalar", operation: "compact", source: "text"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner := buildRunner(t, map[string]any{"from": "vars.values", "operations": []any{test.operation}})
			if _, err := runner.Run(t.Context(), step.Request{Vars: map[string]any{"values": test.source}}); err == nil || !contains(err.Error(), "cannot consume") {
				t.Fatalf("error = %v, want cannot consume", err)
			}
		})
	}
}
