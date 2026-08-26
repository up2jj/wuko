package workflow

import (
	"reflect"
	"testing"
)

// TestCloneSharesNoMutableStateWithSource covers the invariant the engine's branch
// isolation rests on: a cloned value must be unreachable from the original. The
// fast-path shapes were always covered; the typed composites are what used to be
// aliased straight through.
func TestCloneSharesNoMutableStateWithSource(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		source any
		mutate func(any)
	}{
		"[]any": {
			source: []any{map[string]any{"k": "original"}},
			mutate: func(v any) { v.([]any)[0].(map[string]any)["k"] = "mutated" },
		},
		"map[string]any": {
			source: map[string]any{"nested": map[string]any{"k": "original"}},
			mutate: func(v any) { v.(map[string]any)["nested"].(map[string]any)["k"] = "mutated" },
		},
		"[]map[string]any": {
			source: []map[string]any{{"k": "original"}},
			mutate: func(v any) { v.([]map[string]any)[0]["k"] = "mutated" },
		},
		"[]string": {
			source: []string{"original"},
			mutate: func(v any) { v.([]string)[0] = "mutated" },
		},
		"[][]any": {
			source: [][]any{{"original"}},
			mutate: func(v any) { v.([][]any)[0][0] = "mutated" },
		},
		"map[string]string": {
			source: map[string]string{"k": "original"},
			mutate: func(v any) { v.(map[string]string)["k"] = "mutated" },
		},
		"map[string][]any": {
			source: map[string][]any{"k": {"original"}},
			mutate: func(v any) { v.(map[string][]any)["k"][0] = "mutated" },
		},
		"[]any holding []string": {
			source: []any{[]string{"original"}},
			mutate: func(v any) { v.([]any)[0].([]string)[0] = "mutated" },
		},
		"[]int": {
			source: []int{1},
			mutate: func(v any) { v.([]int)[0] = 99 },
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			cloned := Clone(test.source)
			before := reflect.DeepEqual(test.source, cloned)
			if !before {
				t.Fatalf("Clone(%#v) = %#v, want an equal value", test.source, cloned)
			}
			test.mutate(test.source)
			if reflect.DeepEqual(test.source, cloned) {
				t.Errorf("mutating the source changed the clone: source=%#v clone=%#v", test.source, cloned)
			}
		})
	}
}

func TestClonePreservesTypeAndEdgeCases(t *testing.T) {
	t.Parallel()
	tests := map[string]any{
		"nil":             nil,
		"nil slice":       []string(nil),
		"nil map":         map[string]string(nil),
		"empty slice":     []map[string]any{},
		"scalar string":   "value",
		"scalar int":      7,
		"scalar bool":     true,
		"scalar float":    1.5,
		"slice with nils": []any{nil, "x", nil},
		"named slice":     namedSlice{"a"},
	}
	for name, source := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			cloned := Clone(source)
			if !reflect.DeepEqual(source, cloned) {
				t.Fatalf("Clone(%#v) = %#v", source, cloned)
			}
			if source != nil && reflect.TypeOf(source) != reflect.TypeOf(cloned) {
				t.Errorf("Clone changed type %T -> %T", source, cloned)
			}
		})
	}
}

type namedSlice []string
