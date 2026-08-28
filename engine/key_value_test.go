package engine

import (
	"io"
	"testing"

	"github.com/up2jj/wuko/step"
	keyvaluestep "github.com/up2jj/wuko/steps/key_value"
	"github.com/up2jj/wuko/workflow"
)

func TestKeyValueOutputIsAvailableToSubsequentSteps(t *testing.T) {
	registry := newTestRegistry(t, map[string]step.Builder{"capture": func(raw map[string]any) (step.Runner, error) {
		return countingRunner{value: raw["value"]}, nil
	}})
	if err := keyvaluestep.Register(registry); err != nil {
		t.Fatal(err)
	}
	definition := testDefinition(t, "values",
		workflow.Step{ID: "save", Type: "key_value", With: map[string]any{"operation": "set", "scope": "local", "store": "prefs", "key": "theme", "value": "dark"}},
		workflow.Step{ID: "load", Type: "key_value", With: map[string]any{"operation": "get", "scope": "local", "store": "prefs", "key": "theme"}},
		workflow.Step{ID: "consume", Type: "capture", If: "steps.load.found", With: map[string]any{"value": "{{ .steps.load.value }}"}},
	)
	state, err := New(registry).Run(t.Context(), definition, Options{
		LocalValueDir: t.TempDir(), GlobalValueDir: t.TempDir(), Stdout: io.Discard, Stderr: io.Discard,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := state.Steps["consume"].(map[string]any)["value"]; got != "dark" {
		t.Fatalf("consumed value = %#v", got)
	}
}

func TestKeyValueTemplatesAndNumericConditions(t *testing.T) {
	registry := newTestRegistry(t, map[string]step.Builder{"capture": func(raw map[string]any) (step.Runner, error) {
		return countingRunner{value: raw["value"]}, nil
	}})
	if err := keyvaluestep.Register(registry); err != nil {
		t.Fatal(err)
	}
	definition := testDefinition(t, "templated-values",
		workflow.Step{ID: "save", Type: "key_value", With: map[string]any{
			"operation": "{{ .vars.set_operation }}", "scope": "{{ .vars.scope }}",
			"store": "prefs-{{ .vars.suffix }}", "key": "count", "value": 3,
		}},
		workflow.Step{ID: "load", Type: "key_value", With: map[string]any{
			"operation": "{{ .vars.get_operation }}", "scope": "{{ .vars.scope }}",
			"store": "prefs-{{ .vars.suffix }}", "key": "count",
		}},
		workflow.Step{ID: "consume", Type: "capture", If: "steps.load.value > 2", With: map[string]any{"value": "matched"}},
	)
	definition.Vars = map[string]any{"set_operation": "set", "get_operation": "get", "scope": "local", "suffix": "numbers"}
	state, err := New(registry).Run(t.Context(), definition, Options{
		LocalValueDir: t.TempDir(), GlobalValueDir: t.TempDir(), Stdout: io.Discard, Stderr: io.Discard,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := state.Steps["consume"].(map[string]any)["value"]; got != "matched" {
		t.Fatalf("consumed value = %#v", got)
	}
}

func TestCompositeActionInheritsCallerValueRoots(t *testing.T) {
	registry := newTestRegistry(t, nil)
	if err := keyvaluestep.Register(registry); err != nil {
		t.Fatal(err)
	}
	action := testAction(t, "store-action",
		workflow.Step{ID: "save", Type: "key_value", With: map[string]any{"operation": "set", "scope": "local", "store": "shared", "key": "from_action", "value": true}},
		workflow.Step{ID: "load", Type: "key_value", With: map[string]any{"operation": "get", "scope": "local", "store": "shared", "key": "from_action"}},
	)
	action.Outputs = map[string]workflow.ActionOutput{"value": {Value: "steps.load.value"}}
	definition := testDefinition(t, "caller", workflow.Step{ID: "call", Uses: workflow.ActionSource{URL: "https://example.test/action"}, Action: action})
	localRoot := t.TempDir()
	state, err := New(registry).Run(t.Context(), definition, Options{
		LocalValueDir: localRoot, GlobalValueDir: t.TempDir(), Stdout: io.Discard, Stderr: io.Discard,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := state.Steps["call"].(map[string]any)["value"]; got != true {
		t.Fatalf("action output = %#v", got)
	}
	reader, err := keyvaluestep.New(map[string]any{"operation": "get", "scope": "local", "store": "shared", "key": "from_action"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := reader.Run(t.Context(), step.Request{LocalValueDir: localRoot})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outputs["found"] != true {
		t.Fatalf("caller store outputs = %#v", result.Outputs)
	}
}

func TestKeyValueExprPersistsTypesAcrossRuns(t *testing.T) {
	registry := newTestRegistry(t, nil)
	if err := keyvaluestep.Register(registry); err != nil {
		t.Fatal(err)
	}
	localRoot := t.TempDir()
	counter := func() *workflow.Definition {
		return testDefinition(t, "counter",
			workflow.Step{ID: "load", Type: "key_value", With: map[string]any{
				"operation": "get", "scope": "local", "store": "counter", "key": "runs",
			}},
			workflow.Step{ID: "save", Type: "key_value", With: map[string]any{
				"operation": "set", "scope": "local", "store": "counter", "key": "runs",
				"expr": "steps.load.found ? steps.load.value + 1 : 1",
			}},
		)
	}
	for run := 1; run <= 3; run++ {
		state, err := New(registry).Run(t.Context(), counter(), Options{
			LocalValueDir: localRoot, GlobalValueDir: t.TempDir(), Stdout: io.Discard, Stderr: io.Discard,
		})
		if err != nil {
			t.Fatalf("run %d: %v", run, err)
		}
		if got := state.Steps["save"].(map[string]any)["value"]; got != int64(run) {
			t.Fatalf("run %d stored %#v, want %d", run, got, run)
		}
	}
}
