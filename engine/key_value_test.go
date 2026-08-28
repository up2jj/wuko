package engine

import (
	"io"
	"os"
	"strings"
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

func TestRemoteActionIsDeniedTheCallerLocalStore(t *testing.T) {
	registry := newTestRegistry(t, nil)
	if err := keyvaluestep.Register(registry); err != nil {
		t.Fatal(err)
	}
	localRoot := t.TempDir()
	run := func(scope string) error {
		action := testAction(t, "store-action",
			workflow.Step{ID: "save", Type: "key_value", With: map[string]any{
				"operation": "set", "scope": scope, "store": "shared", "key": "from_remote", "value": true,
			}},
		)
		definition := testDefinition(t, "caller", workflow.Step{
			ID: "call", Uses: workflow.ActionSource{URL: "https://actions.example.test/store"}, Action: action,
		})
		_, err := New(registry).Run(t.Context(), definition, Options{
			LocalValueDir: localRoot, GlobalValueDir: t.TempDir(), Stdout: io.Discard, Stderr: io.Discard,
		})
		return err
	}
	if err := run("local"); err == nil || !strings.Contains(err.Error(), "local key-value storage is unavailable") {
		t.Fatalf("local scope error = %v", err)
	}
	if _, err := os.Stat(localRoot); err != nil {
		t.Fatalf("caller value root: %v", err)
	}
	if entries, err := os.ReadDir(localRoot); err != nil || len(entries) != 0 {
		t.Fatalf("remote action wrote %#v (%v) into the caller store", entries, err)
	}
	if err := run("global"); err != nil {
		t.Fatalf("global scope: %v", err)
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
	definition := testDefinition(t, "caller", workflow.Step{ID: "call", Uses: workflow.ActionSource{Path: "./actions/store/action.yaml"}, Action: action})
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

func TestKeyValueUpdateSurvivesConcurrentIncrements(t *testing.T) {
	registry := newTestRegistry(t, nil)
	if err := keyvaluestep.Register(registry); err != nil {
		t.Fatal(err)
	}
	definition := testDefinition(t, "concurrent-counter",
		workflow.Step{ID: "bump", Foreach: &workflow.ForeachGroup{
			Items: "[1, 2, 3, 4, 5, 6, 7, 8]", MaxConcurrency: 8,
			Steps: []workflow.Step{{ID: "increment", Type: "key_value", With: map[string]any{
				"operation": "update", "scope": "local", "store": "counter", "key": "runs",
				"expr": "found ? current + 1 : 1",
			}}},
		}},
		workflow.Step{ID: "total", Type: "key_value", With: map[string]any{
			"operation": "get", "scope": "local", "store": "counter", "key": "runs",
		}},
	)
	state, err := New(registry).Run(t.Context(), definition, Options{
		LocalValueDir: t.TempDir(), GlobalValueDir: t.TempDir(), Stdout: io.Discard, Stderr: io.Discard,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := state.Steps["total"].(map[string]any)["value"]; got != int64(8) {
		t.Fatalf("counter = %#v, want 8", got)
	}
}
