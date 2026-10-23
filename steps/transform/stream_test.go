package transform

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/up2jj/wuko/internal/steptest"
	"github.com/up2jj/wuko/step"
)

// runListPipeline is the evaluation transform used before pipelines were streamed: every
// operation runs to completion over a materialized list.
func runListPipeline(ctx context.Context, runner *Runner, request step.Request) (any, error) {
	evaluation := newEvaluation(request)
	current, err := runner.resolveSource(evaluation)
	if err != nil {
		return nil, err
	}
	for position, operation := range runner.operations {
		if !operation.accepts(kindOf(current)) {
			return nil, fmt.Errorf("operation %d (%s): cannot consume %s", position+1, operation.name, kindOf(current))
		}
		current, err = runOperation(ctx, evaluation, current, operation)
		if err != nil {
			return nil, fmt.Errorf("operation %d (%s): %w", position+1, operation.name, err)
		}
	}
	return normalizeResult(current)
}

var streamedOperationCases = map[string][]any{
	"filter":         {map[string]any{"filter": "item != nil && item != 2"}, map[string]any{"filter": "item"}},
	"reject":         {map[string]any{"reject": "item == 1"}},
	"map":            {map[string]any{"map": "[item, index]"}, map[string]any{"map": "item + 1"}},
	"flat_map":       {map[string]any{"flat_map": "[item, item]"}, map[string]any{"flat_map": "item"}},
	"filter_map":     {map[string]any{"filter_map": map[string]any{"when": "item != nil", "value": "{v: item, i: index}"}}},
	"reject_map":     {map[string]any{"reject_map": map[string]any{"when": "item == 1", "value": "item"}}},
	"flatten":        {"flatten"},
	"compact":        {"compact"},
	"clone":          {"clone"},
	"take":           {map[string]any{"take": 0}, map[string]any{"take": 2}, map[string]any{"take": 50}, map[string]any{"take": -1}, map[string]any{"take": "x"}},
	"drop":           {map[string]any{"drop": 0}, map[string]any{"drop": 2}, map[string]any{"drop": 50}, map[string]any{"drop": -1}},
	"take_while":     {map[string]any{"take_while": "index < 2"}, map[string]any{"take_while": "true"}, map[string]any{"take_while": "item == 1"}},
	"drop_while":     {map[string]any{"drop_while": "index < 2"}, map[string]any{"drop_while": "true"}, map[string]any{"drop_while": "item == 1"}},
	"take_filter":    {map[string]any{"take_filter": map[string]any{"count": 1, "when": "item != nil"}}, map[string]any{"take_filter": map[string]any{"count": 0, "when": "true"}}},
	"slice":          {map[string]any{"slice": map[string]any{"start": 1, "end": 3}}, map[string]any{"slice": map[string]any{"start": -2, "end": 2}}, map[string]any{"slice": map[string]any{"start": 3, "end": 1}}, map[string]any{"slice": map[string]any{"start": 2, "end": 99}}},
	"unique":         {"unique"},
	"unique_by":      {map[string]any{"unique_by": "type(item)"}},
	"unique_map":     {map[string]any{"unique_map": "type(item)"}},
	"without":        {map[string]any{"without": []any{1, nil}}},
	"without_by":     {map[string]any{"without_by": map[string]any{"values": []any{"int"}, "by": "type(item)"}}},
	"concat":         {map[string]any{"concat": []any{[]any{9}, []any{}}}},
	"first":          {"first"},
	"first_or":       {map[string]any{"first_or": "none"}},
	"first_or_empty": {"first_or_empty"},
	"find":           {map[string]any{"find": "item == 2"}, map[string]any{"find": "false"}},
	"find_index":     {map[string]any{"find_index": "item == 2"}, map[string]any{"find_index": "false"}},
	"contains":       {map[string]any{"contains": 2}, map[string]any{"contains": "missing"}},
	"contains_by":    {map[string]any{"contains_by": "item == 2"}},
	"some_by":        {map[string]any{"some_by": "item == 2"}},
	"none_by":        {map[string]any{"none_by": "item == 2"}},
	"every_by":       {map[string]any{"every_by": "item != nil"}},
	"index_of":       {map[string]any{"index_of": 2}, map[string]any{"index_of": "missing"}},
	"count":          {map[string]any{"count": 1}, "count"},
	"count_by":       {map[string]any{"count_by": "item == 1"}},
}

var streamedSources = []any{
	[]any{},
	[]any{1, 2, 3, 1},
	[]any{json.Number("1"), json.Number("2"), json.Number("2.5")},
	[]any{nil, 0, "", false, 1, "a", true},
	[]any{[]any{1, 2}, []any{}, []any{[]any{3}}},
	[]any{map[string]any{"a": 1}, map[string]any{"a": 1}, []any{1}},
	[]any{1, "two", []any{3}},
}

func TestStreamedOperationsAreAllCovered(t *testing.T) {
	t.Parallel()
	for name := range streamStages {
		if _, covered := streamedOperationCases[name]; !covered {
			t.Errorf("stream stage %s has no equivalence case", name)
		}
	}
	for name := range streamSinks {
		if _, covered := streamedOperationCases[name]; !covered {
			t.Errorf("stream sink %s has no equivalence case", name)
		}
	}
}

// A single streamed operation must give exactly the list implementation's value or error.
func TestStreamingMatchesListOperations(t *testing.T) {
	t.Parallel()
	for _, operations := range streamedOperationCases {
		for _, operation := range operations {
			for _, source := range streamedSources {
				assertStreamingMatches(t, []any{operation}, source, true)
			}
		}
	}
}

// Chained, each pair evaluates lazily but must still produce the same value whenever neither
// implementation fails.
func TestStreamingChainsMatchListOperations(t *testing.T) {
	t.Parallel()
	for firstName, firsts := range streamedOperationCases {
		if _, stage := streamStages[firstName]; !stage {
			continue
		}
		for _, first := range firsts {
			for _, seconds := range streamedOperationCases {
				for _, second := range seconds {
					for _, source := range streamedSources {
						assertStreamingMatches(t, []any{first, second}, source, false)
					}
				}
			}
		}
	}
}

func assertStreamingMatches(t *testing.T, operations []any, source any, sameErrors bool) {
	t.Helper()
	runner, err := New(map[string]any{"from": "vars.source", "operations": operations})
	if err != nil {
		return
	}
	request := step.Request{Vars: map[string]any{"source": source}}
	want, wantErr := runListPipeline(t.Context(), runner.(*Runner), request)
	result, gotErr := runner.Run(t.Context(), request)
	if gotErr != nil || wantErr != nil {
		if !sameErrors {
			return
		}
		if (gotErr == nil) != (wantErr == nil) || gotErr.Error() != wantErr.Error() {
			t.Errorf("%v over %#v: error %v, want %v", operations, source, gotErr, wantErr)
		}
		return
	}
	if got := result.Outputs["value"]; !reflect.DeepEqual(got, want) {
		t.Errorf("%v over %#v = %#v, want %#v", operations, source, got, want)
	}
}

// Streaming stops evaluating as soon as the result is decided, so an item past that point
// that would fail is never evaluated.
func TestStreamingStopsEvaluatingOnceTheResultIsKnown(t *testing.T) {
	t.Parallel()
	source := []any{1, 2, 3, "boom"}
	tests := map[string][]any{
		"take":       {map[string]any{"map": "item + 1"}, map[string]any{"take": 3}},
		"take_while": {map[string]any{"map": "item + 1"}, map[string]any{"take_while": "item < 4"}},
		"first":      {map[string]any{"map": "item * 2"}, "first"},
		"find":       {map[string]any{"filter": "item > 0"}, map[string]any{"find": "item == 2"}},
		"some_by":    {map[string]any{"map": "item - 1"}, map[string]any{"some_by": "item == 0"}},
		"contains":   {map[string]any{"map": "item * 10"}, map[string]any{"contains": 20}},
	}
	for name, operations := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			runner := steptest.BuildAs[*Runner](t, New, map[string]any{"from": "vars.source", "operations": operations})
			if _, err := runner.Run(t.Context(), step.Request{Vars: map[string]any{"source": source}}); err != nil {
				t.Fatalf("Run() evaluated the failing item past the result: %v", err)
			}
		})
	}
}

func TestStreamingReportsTheFailingOperationAndItem(t *testing.T) {
	t.Parallel()
	runner := steptest.BuildAs[*Runner](t, New, map[string]any{"from": "vars.source", "operations": []any{
		map[string]any{"filter": "true"},
		map[string]any{"map": "item + 1"},
		map[string]any{"take": 10},
	}})
	_, err := runner.Run(t.Context(), step.Request{Vars: map[string]any{"source": []any{1, "x"}}})
	if err == nil || !strings.HasPrefix(err.Error(), "operation 2 (map): item 1: ") {
		t.Fatalf("error = %v, want it attributed to operation 2, item 1", err)
	}
}

func TestStreamingKeepsStageLocalIndexes(t *testing.T) {
	t.Parallel()
	runner := steptest.BuildAs[*Runner](t, New, map[string]any{"from": "vars.source", "operations": []any{
		map[string]any{"filter": "item % 2 == 0"},
		map[string]any{"map": "index"},
	}})
	result, err := runner.Run(t.Context(), step.Request{Vars: map[string]any{"source": []any{1, 2, 3, 4, 5, 6}}})
	if err != nil {
		t.Fatal(err)
	}
	if want := []any{0, 1, 2}; !reflect.DeepEqual(result.Outputs["value"], want) {
		t.Fatalf("value = %#v, want %#v", result.Outputs["value"], want)
	}
}

func TestStreamingStopsWhenCanceled(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	source := make([]any, 1000)
	for index := range source {
		source[index] = index
	}
	evaluated := 0
	runner := steptest.BuildAs[*Runner](t, New, map[string]any{"from": "vars.source", "operations": []any{
		map[string]any{"filter": "tick()"},
	}})
	request := step.Request{Vars: map[string]any{"source": source}}
	request.Bindings = map[string]any{"tick": func() bool {
		if evaluated++; evaluated == 10 {
			cancel()
		}
		return true
	}}
	_, err := runner.Run(ctx, request)
	if !errors.Is(err, context.Canceled) || evaluated > 11 {
		t.Fatalf("error = %v after %d evaluations, want cancellation right away", err, evaluated)
	}
}
