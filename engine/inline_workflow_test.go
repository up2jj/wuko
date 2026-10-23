package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/workflow"
)

type inlineEchoRunner struct{ value string }

func (runner inlineEchoRunner) Run(context.Context, step.Request) (step.Result, error) {
	return step.Result{Outputs: map[string]any{"value": runner.value}}, nil
}

func TestInlineWorkflowInheritsSnapshotAndAddsBindings(t *testing.T) {
	t.Parallel()
	registry := step.NewRegistry()
	if err := registry.RegisterDefinition("inline_echo", step.Registration{
		Builder: func(raw map[string]any) (step.Runner, error) {
			value, _ := raw["value"].(string)
			return inlineEchoRunner{value: value}, nil
		},
		Outputs: step.ClosedOutputs("value"),
	}); err != nil {
		t.Fatal(err)
	}
	definition := &workflow.Definition{Version: 1, Name: "parent", Vars: map[string]any{}, Env: workflow.Environment{}}
	state := &State{
		Inputs: map[string]any{}, Vars: map[string]any{}, Env: map[string]string{}, Steps: map[string]any{
			"before": map[string]any{"value": "inherited"},
		},
		Outputs: map[string]any{}, Dependencies: map[string]map[string]any{}, Bindings: map[string]any{}, presetVars: map[string]any{},
	}
	runner := boundInlineWorkflowRunner{engine: New(registry), definition: definition, state: state}
	call := step.InlineWorkflowCall{
		Name: "test",
		Steps: []workflow.Step{{ID: "echo", Type: "inline_echo", With: map[string]any{
			"value": "{{ .steps.before.value }}:{{ .data.name }}",
		}}},
		Outputs:  map[string]string{"rendered": "steps.echo.value"},
		Bindings: map[string]any{"data": map[string]any{"name": "bound"}},
	}

	if err := runner.ValidateInlineWorkflow(t.Context(), call); err != nil {
		t.Fatalf("ValidateInlineWorkflow() error = %v", err)
	}
	outputs, err := runner.RunInlineWorkflow(t.Context(), call)
	if err != nil {
		t.Fatalf("RunInlineWorkflow() error = %v", err)
	}
	if got, want := outputs["rendered"], "inherited:bound"; got != want {
		t.Fatalf("rendered = %#v, want %#v", got, want)
	}
	if _, mutated := state.Steps["echo"]; mutated {
		t.Fatal("inline workflow mutated its captured parent state")
	}
}

func TestInlineWorkflowValidationAcceptsParentStepsAndVariablesBeforeRun(t *testing.T) {
	t.Parallel()
	registry := step.NewRegistry()
	if err := registry.RegisterDefinition("inline_echo", step.Registration{
		Builder: func(raw map[string]any) (step.Runner, error) { return inlineEchoRunner{}, nil },
		Outputs: step.ClosedOutputs("value"),
	}); err != nil {
		t.Fatal(err)
	}
	definition := &workflow.Definition{Version: 1, Name: "parent", Vars: map[string]any{}, Env: workflow.Environment{}, Steps: []workflow.Step{
		{ID: "before", Type: "inline_echo"},
	}}
	// Validation-time state: no step has run and no variable has been written yet.
	state := &State{
		Inputs: map[string]any{}, Vars: map[string]any{}, Env: map[string]string{}, Steps: map[string]any{},
		Outputs: map[string]any{}, Dependencies: map[string]map[string]any{}, Bindings: map[string]any{}, presetVars: map[string]any{},
	}
	runner := boundInlineWorkflowRunner{engine: New(registry), definition: definition, state: state}
	call := step.InlineWorkflowCall{Steps: []workflow.Step{{ID: "echo", Type: "inline_echo", With: map[string]any{
		"value": "{{ .steps.before.value }}:{{ .vars.written_later }}",
	}}}}
	if err := runner.ValidateInlineWorkflow(t.Context(), call); err != nil {
		t.Fatalf("ValidateInlineWorkflow() error = %v", err)
	}
	if len(state.Steps) != 0 {
		t.Fatal("validation mutated the captured state")
	}
}

func TestInlineWorkflowRejectsStepIDsThatShadowParentSteps(t *testing.T) {
	t.Parallel()
	registry := step.NewRegistry()
	if err := registry.RegisterDefinition("inline_echo", step.Registration{
		Builder: func(raw map[string]any) (step.Runner, error) { return inlineEchoRunner{}, nil },
		Outputs: step.ClosedOutputs("value"),
	}); err != nil {
		t.Fatal(err)
	}
	definition := &workflow.Definition{Version: 1, Name: "parent", Vars: map[string]any{}, Env: workflow.Environment{}, Steps: []workflow.Step{
		{ID: "before", Type: "inline_echo"},
	}}
	newState := func(steps map[string]any) *State {
		return &State{
			Inputs: map[string]any{}, Vars: map[string]any{}, Env: map[string]string{}, Steps: steps,
			Outputs: map[string]any{}, Dependencies: map[string]map[string]any{}, Bindings: map[string]any{}, presetVars: map[string]any{},
		}
	}
	tests := []struct {
		name string
		call step.InlineWorkflowCall
	}{
		{"steps", step.InlineWorkflowCall{Steps: []workflow.Step{{ID: "before", Type: "inline_echo"}}}},
		{"finally", step.InlineWorkflowCall{
			Steps:   []workflow.Step{{ID: "echo", Type: "inline_echo"}},
			Finally: []workflow.Step{{ID: "before", Type: "inline_echo"}},
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// Validation runs before any parent step, so the check must use the definition.
			validating := boundInlineWorkflowRunner{engine: New(registry), definition: definition, state: newState(map[string]any{})}
			if err := validating.ValidateInlineWorkflow(t.Context(), test.call); err == nil || !strings.Contains(err.Error(), `step id "before" shadows`) {
				t.Fatalf("ValidateInlineWorkflow() error = %v, want shadow error", err)
			}
			running := boundInlineWorkflowRunner{engine: New(registry), definition: definition, state: newState(map[string]any{
				"before": map[string]any{"value": "x"},
			})}
			if _, err := running.RunInlineWorkflow(t.Context(), test.call); err == nil || !strings.Contains(err.Error(), `step id "before" shadows`) {
				t.Fatalf("RunInlineWorkflow() error = %v, want shadow error", err)
			}
		})
	}
}
