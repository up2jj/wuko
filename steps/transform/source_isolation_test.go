package transform

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/up2jj/wuko/internal/steptest"
	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/workflow"
)

// The runner hands operations the source without copying it, so no operation may write into
// a value it consumes. Every operation runs here against nested lists and objects, and the
// source must come back exactly as it went in, whether the operation succeeded or not.
func TestOperationsLeaveTheirSourceUntouched(t *testing.T) {
	t.Parallel()
	listSource := func() any {
		return []any{
			map[string]any{"name": "b", "tags": []any{"y", "x"}, "count": json.Number("2")},
			map[string]any{"name": "a", "tags": []any{"x"}, "count": json.Number("1")},
			[]any{json.Number("3"), json.Number("1"), json.Number("2")},
			"text",
			json.Number("7"),
		}
	}
	objectSource := func() any {
		return map[string]any{
			"b": map[string]any{"name": "b", "tags": []any{"y", "x"}},
			"a": []any{json.Number("2"), json.Number("1")},
			"c": "text",
		}
	}
	for _, name := range OperationNames() {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			parsed, err := parseOperation(0, operationFixture(name))
			if err != nil {
				t.Fatal(err)
			}
			for _, source := range []any{listSource(), objectSource()} {
				if !parsed.accepts(kindOf(source)) {
					continue
				}
				want := workflow.Clone(source)
				runner := steptest.BuildAs[*Runner](t, New, map[string]any{"from": "vars.source", "operations": []any{operationFixture(name)}})
				_, _ = runner.Run(t.Context(), step.Request{Vars: map[string]any{"source": source}})
				if !reflect.DeepEqual(source, want) {
					t.Fatalf("%s changed its source:\n got %#v\nwant %#v", name, source, want)
				}
			}
		})
	}
}
