package workflow

import (
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func TestObserveControlLoadsDefaults(t *testing.T) {
	var definition Definition
	err := yaml.Unmarshal([]byte(`
version: 1
name: observe-test
steps:
  - id: dev
    observe:
      source:
        type: filesystem
        with:
          paths: ["**/*.go"]
      steps:
        - id: body
          type: shell
          with: {command: go}
`), &definition)
	if err != nil {
		t.Fatal(err)
	}
	group := definition.Steps[0].Observe
	if group.EffectiveOnError() != ObserveFail {
		t.Fatalf("on_error default = %q", group.EffectiveOnError())
	}
	if group.Source.Type != "filesystem" || group.EffectiveDebounce() != 300*time.Millisecond || group.EffectiveOnChange() != ObserveRestart {
		t.Fatalf("observe = %#v", group)
	}
	if group.Source.With["paths"] == nil {
		t.Fatalf("source config = %#v", group.Source.With)
	}
}

func TestObserveControlRejectsInvalidDeclarations(t *testing.T) {
	validBody := []Step{{ID: "body", Type: "shell", With: map[string]any{"command": "true"}}}
	validSource := ObserveSource{Type: "filesystem", With: map[string]any{"paths": []any{"**"}}}
	tests := []struct {
		name string
		step Step
		want string
	}{
		{"missing id", Step{Observe: &ObserveGroup{Source: validSource, Steps: validBody}}, "valid id"},
		{"missing source", Step{ID: "observe", Observe: &ObserveGroup{Steps: validBody}}, "source requires"},
		{"missing body", Step{ID: "observe", Observe: &ObserveGroup{Source: validSource}}, "at least one step"},
		{"policy", Step{ID: "observe", Observe: &ObserveGroup{Source: validSource, Steps: validBody, OnChange: "parallel"}}, "restart, queue, or skip"},
		{"error policy", Step{ID: "observe", Observe: &ObserveGroup{Source: validSource, Steps: validBody, OnError: "retry"}}, "fail or continue"},
		{"nested", Step{Concurrent: &ConcurrentGroup{MaxConcurrency: 2, Steps: []Step{
			{ID: "observe", Observe: &ObserveGroup{Source: validSource, Steps: validBody}},
			{ID: "other", Type: "shell", With: map[string]any{"command": "true"}},
		}}}, "main sequential"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			definition := testDefinitionForObserve(test.step)
			err := definition.ValidateStructure()
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func testDefinitionForObserve(step Step) *Definition {
	return &Definition{Version: 1, Name: "observe-test", Steps: []Step{step}}
}

func TestObserveControlLoadsErrorPolicy(t *testing.T) {
	var definition Definition
	err := yaml.Unmarshal([]byte(`
version: 1
name: observe-test
steps:
  - id: dev
    observe:
      source:
        type: shell
        with: {command: status}
      on_error: continue
      steps:
        - id: body
          type: shell
          with: {command: go}
`), &definition)
	if err != nil {
		t.Fatal(err)
	}
	group := definition.Steps[0].Observe
	if group.EffectiveOnError() != ObserveContinue {
		t.Fatalf("on_error = %q", group.EffectiveOnError())
	}
	if description := definition.Steps[0].BackgroundControlDescription(); !strings.Contains(description, "on error continue") {
		t.Fatalf("description = %q", description)
	}
}
