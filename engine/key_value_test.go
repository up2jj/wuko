package engine

import (
	"io"
	"testing"

	"github.com/up2jj/wuko/step"
	keyvaluestep "github.com/up2jj/wuko/steps/key_value"
	"github.com/up2jj/wuko/workflow"
)

func TestKeyValueOutputIsAvailableToSubsequentSteps(t *testing.T) {
	registry := step.NewRegistry()
	if err := keyvaluestep.Register(registry); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register("capture", func(raw map[string]any) (step.Runner, error) {
		return countingRunner{value: raw["value"]}, nil
	}); err != nil {
		t.Fatal(err)
	}
	definition := &workflow.Definition{
		Version: 1, Name: "values", Dir: t.TempDir(), Vars: map[string]any{}, Env: workflow.Environment{},
		Steps: []workflow.Step{
			{ID: "save", Type: "key_value", With: map[string]any{"operation": "set", "scope": "local", "store": "prefs", "key": "theme", "value": "dark"}},
			{ID: "load", Type: "key_value", With: map[string]any{"operation": "get", "scope": "local", "store": "prefs", "key": "theme"}},
			{ID: "consume", Type: "capture", If: "steps.load.found", With: map[string]any{"value": "{{ .steps.load.value }}"}},
		},
	}
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
	registry := step.NewRegistry()
	if err := keyvaluestep.Register(registry); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register("capture", func(raw map[string]any) (step.Runner, error) {
		return countingRunner{value: raw["value"]}, nil
	}); err != nil {
		t.Fatal(err)
	}
	definition := &workflow.Definition{
		Version: 1, Name: "templated-values", Dir: t.TempDir(),
		Vars: map[string]any{"set_operation": "set", "get_operation": "get", "scope": "local", "suffix": "numbers"},
		Env:  workflow.Environment{},
		Steps: []workflow.Step{
			{ID: "save", Type: "key_value", With: map[string]any{
				"operation": "{{ .vars.set_operation }}", "scope": "{{ .vars.scope }}",
				"store": "prefs-{{ .vars.suffix }}", "key": "count", "value": 3,
			}},
			{ID: "load", Type: "key_value", With: map[string]any{
				"operation": "{{ .vars.get_operation }}", "scope": "{{ .vars.scope }}",
				"store": "prefs-{{ .vars.suffix }}", "key": "count",
			}},
			{ID: "consume", Type: "capture", If: "steps.load.value > 2", With: map[string]any{"value": "matched"}},
		},
	}
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
	registry := step.NewRegistry()
	if err := keyvaluestep.Register(registry); err != nil {
		t.Fatal(err)
	}
	action := &workflow.Action{
		Version: 1, Name: "store-action", Dir: t.TempDir(),
		Outputs: map[string]workflow.ActionOutput{"value": {Value: "steps.load.value"}},
		Steps: []workflow.Step{
			{ID: "save", Type: "key_value", With: map[string]any{"operation": "set", "scope": "local", "store": "shared", "key": "from_action", "value": true}},
			{ID: "load", Type: "key_value", With: map[string]any{"operation": "get", "scope": "local", "store": "shared", "key": "from_action"}},
		},
	}
	definition := &workflow.Definition{
		Version: 1, Name: "caller", Dir: t.TempDir(), Vars: map[string]any{}, Env: workflow.Environment{},
		Steps: []workflow.Step{{ID: "call", Uses: workflow.ActionSource{URL: "https://example.test/action"}, Action: action, With: map[string]any{}}},
	}
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
