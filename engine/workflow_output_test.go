package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/workflow"
)

func TestWorkflowDependencyOutputsReachTemplatesExpressionsAndDeclaredOutputs(t *testing.T) {
	registry := newTestRegistry(t, map[string]step.Builder{
		"dependency_capture": func(raw map[string]any) (step.Runner, error) {
			return runnerFunc(func(_ context.Context, request step.Request) (step.Result, error) {
				return step.Result{Outputs: map[string]any{
					"rendered": raw["value"],
					"request":  request.Dependencies["build"]["artifact"],
				}}, nil
			}), nil
		},
	})
	definition := testDefinition(t, "release", workflow.Step{
		ID: "consume", Type: "dependency_capture", If: `dependencies.build.publishable`,
		With: map[string]any{"value": "{{ .dependencies.build.artifact }}"},
	})
	definition.Outputs = map[string]workflow.WorkflowOutput{
		"artifact": {Type: "string", Value: "steps.consume.rendered"},
	}
	state, err := New(registry).Run(t.Context(), definition, Options{Dependencies: map[string]map[string]any{
		"build": {"artifact": "dist/app.tar.gz", "publishable": true},
	}})
	if err != nil {
		t.Fatal(err)
	}
	outputs := state.Steps["consume"].(map[string]any)
	if outputs["rendered"] != "dist/app.tar.gz" || outputs["request"] != "dist/app.tar.gz" || state.Outputs["artifact"] != "dist/app.tar.gz" {
		t.Fatalf("state = %#v", state)
	}
}

func TestWorkflowOutputContractRejectsEarlyReturnTypeMismatch(t *testing.T) {
	definition := testDefinition(t, "typed", workflow.Step{Return: &workflow.ReturnControl{Outputs: map[string]string{"count": `"three"`}}})
	definition.Outputs = map[string]workflow.WorkflowOutput{"count": {Type: "number", Value: "3"}}
	_, err := New(newTestRegistry(t, nil)).Run(t.Context(), definition, Options{})
	if err == nil || !strings.Contains(err.Error(), `output "count" value does not match type number`) {
		t.Fatalf("error = %v", err)
	}
}

func TestWorkflowDeclaredOutputsProduceDryRunPlaceholders(t *testing.T) {
	definition := testDefinition(t, "dry", workflow.Step{Return: &workflow.ReturnControl{Outputs: map[string]string{"ready": "true"}}})
	definition.Outputs = map[string]workflow.WorkflowOutput{"ready": {Type: "boolean", Value: "true"}}
	state, err := New(newTestRegistry(t, nil)).Run(t.Context(), definition, Options{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if state.Outputs["ready"] != false {
		t.Fatalf("outputs = %#v", state.Outputs)
	}
}

func TestRunStepsDoesNotEvaluateWorkflowOutputs(t *testing.T) {
	registry := newTestRegistry(t, map[string]step.Builder{
		"capture": func(raw map[string]any) (step.Runner, error) {
			return runnerFunc(func(context.Context, step.Request) (step.Result, error) {
				return step.Result{Outputs: map[string]any{"value": raw["value"]}}, nil
			}), nil
		},
	})
	definition := testDefinition(t, "lifecycle", workflow.Step{ID: "main", Type: "capture", With: map[string]any{"value": "main"}})
	definition.Outputs = map[string]workflow.WorkflowOutput{
		"result": {Type: "string", Value: "steps.main.value"},
	}

	if _, err := New(registry).RunSteps(t.Context(), definition, []workflow.Step{{
		ID: "install", Type: "capture", With: map[string]any{"value": "installed"},
	}}, Options{}); err != nil {
		t.Fatalf("RunSteps() error = %v", err)
	}
}
