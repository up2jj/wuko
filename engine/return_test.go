package engine

import (
	"bytes"
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/workflow"
)

func TestReturnFinishesWorkflowWithTypedOutputsAndSkippedSteps(t *testing.T) {
	registry := step.NewRegistry()
	var runs int
	if err := registry.Register("capture_return", func(raw map[string]any) (step.Runner, error) {
		return countingRunner{value: raw["value"], runs: &runs}, nil
	}); err != nil {
		t.Fatal(err)
	}
	definition := &workflow.Definition{
		Version: 1, Name: "early", Dir: t.TempDir(), Vars: map[string]any{"enabled": true},
		Steps: []workflow.Step{
			{ID: "prepare", Type: "capture_return", With: map[string]any{"value": "artifact.tar.gz"}},
			{Return: &workflow.ReturnControl{Outputs: map[string]string{
				"artifact": "steps.prepare.value", "cached": "true", "count": "2",
				"items": `[steps.prepare.value, "checksum"]`, "metadata": `{"source": "cache"}`,
			}}, If: "vars.enabled"},
			{ID: "after", Type: "capture_return", With: map[string]any{"value": "not-run"}},
		},
	}
	state, err := New(registry).Run(t.Context(), definition, Options{Stdout: io.Discard, Stderr: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"artifact": "artifact.tar.gz", "cached": true, "count": 2,
		"items": []any{"artifact.tar.gz", "checksum"}, "metadata": map[string]any{"source": "cache"},
	}
	if !reflect.DeepEqual(state.Outputs, want) {
		t.Fatalf("outputs = %#v, want %#v", state.Outputs, want)
	}
	if runs != 1 || state.Stats.Succeeded != 1 || state.Stats.Skipped != 1 || len(state.Stats.Steps) != 2 {
		t.Fatalf("runs = %d, stats = %#v", runs, state.Stats)
	}
	if _, exists := state.Steps["after"]; exists {
		t.Fatalf("later step committed: %#v", state.Steps)
	}
}

func TestReturnConditionFalseContinues(t *testing.T) {
	registry := step.NewRegistry()
	var runs int
	if err := registry.Register("capture_return", func(raw map[string]any) (step.Runner, error) {
		return countingRunner{value: raw["value"], runs: &runs}, nil
	}); err != nil {
		t.Fatal(err)
	}
	definition := &workflow.Definition{Version: 1, Name: "continue", Dir: t.TempDir(), Steps: []workflow.Step{
		{Return: &workflow.ReturnControl{Outputs: map[string]string{}}, If: "false"},
		{ID: "after", Type: "capture_return", With: map[string]any{"value": "ran"}},
	}}
	state, err := New(registry).Run(t.Context(), definition, Options{Stdout: io.Discard, Stderr: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if runs != 1 || len(state.Outputs) != 0 || state.Steps["after"].(map[string]any)["value"] != "ran" {
		t.Fatalf("runs = %d, state = %#v", runs, state)
	}
}

func TestReturnSkipsLaterFanoutWithoutExpansion(t *testing.T) {
	registry := step.NewRegistry()
	if err := registry.Register("capture_return", func(map[string]any) (step.Runner, error) { return countingRunner{}, nil }); err != nil {
		t.Fatal(err)
	}
	definition := &workflow.Definition{Version: 1, Name: "skip-fanout", Dir: t.TempDir(), Steps: []workflow.Step{
		{Return: &workflow.ReturnControl{Outputs: map[string]string{}}},
		{ID: "later", Foreach: &workflow.ForeachGroup{
			Items: "vars.missing", MaxConcurrency: 1, MaxIterations: 10, FailFast: true,
			Steps: []workflow.Step{{ID: "child", Type: "capture_return", With: map[string]any{}}},
		}},
	}}
	state, err := New(registry).Run(t.Context(), definition, Options{Stdout: io.Discard, Stderr: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if state.Stats.Total != 1 || state.Stats.Skipped != 1 || len(state.Stats.Steps) != 1 || state.Stats.Steps[0].Type != "foreach" {
		t.Fatalf("stats = %#v", state.Stats)
	}
}

func TestReturnPropagatesThroughSequentialBlocksAndRunsFinally(t *testing.T) {
	registry := step.NewRegistry()
	cleanupRan := false
	if err := registry.Register("return_cleanup", func(map[string]any) (step.Runner, error) {
		return runnerFunc(func(_ context.Context, request step.Request) (step.Result, error) {
			cleanupRan = true
			if request.Bindings["finally"].(map[string]any)["status"] != "succeeded" {
				t.Fatalf("finally = %#v", request.Bindings["finally"])
			}
			return step.Result{}, nil
		}), nil
	}); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	definition := &workflow.Definition{Version: 1, Name: "nested", Dir: dir,
		Steps: []workflow.Step{
			{WorkingDirectory: dir, Steps: []workflow.Step{{If: "true", Steps: []workflow.Step{
				{Return: &workflow.ReturnControl{Outputs: map[string]string{"dir": "run.dir"}}},
				{ID: "inner_after", Type: "return_cleanup", With: map[string]any{}},
			}}}},
			{ID: "outer_after", Type: "return_cleanup", With: map[string]any{}},
		},
		Finally: []workflow.Step{{ID: "cleanup", Type: "return_cleanup", With: map[string]any{}}},
	}
	state, err := New(registry).Run(t.Context(), definition, Options{RunDir: dir, Stdout: io.Discard, Stderr: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if state.Outputs["dir"] != dir || !cleanupRan || state.Stats.Skipped != 2 || state.Stats.Succeeded != 1 {
		t.Fatalf("cleanup = %v, state = %#v", cleanupRan, state)
	}
}

func TestReturnExpressionFailureFailsWorkflowAtomically(t *testing.T) {
	definition := &workflow.Definition{Version: 1, Name: "broken", Dir: t.TempDir(), Steps: []workflow.Step{{
		Return: &workflow.ReturnControl{Outputs: map[string]string{"good": "true", "missing": "steps.unknown.value"}},
	}}}
	state, err := New(step.NewRegistry()).Run(t.Context(), definition, Options{Stdout: io.Discard, Stderr: io.Discard})
	if err == nil || !strings.Contains(err.Error(), `return output "missing"`) {
		t.Fatalf("state = %#v, error = %v", state, err)
	}
}

func TestCanceledContextPreventsReturn(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	definition := &workflow.Definition{Version: 1, Name: "canceled-return", Dir: t.TempDir(), Steps: []workflow.Step{{
		Return: &workflow.ReturnControl{Outputs: map[string]string{"result": `"done"`}},
	}}}
	_, err := New(step.NewRegistry()).Run(ctx, definition, Options{Stdout: io.Discard, Stderr: io.Discard})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
}

func TestCompositeActionReturnSuppliesDeclaredOutputsWithoutRetry(t *testing.T) {
	registry := step.NewRegistry()
	var runs int
	if err := registry.Register("capture_return", func(raw map[string]any) (step.Runner, error) {
		return countingRunner{value: raw["value"], runs: &runs}, nil
	}); err != nil {
		t.Fatal(err)
	}
	action := &workflow.Action{
		Version: 1, Name: "returning-action", Dir: t.TempDir(),
		Inputs: map[string]workflow.ActionInput{"result": {Type: "string", Required: true}},
		Outputs: map[string]workflow.ActionOutput{
			"result": {Value: `"fallback"`}, "cached": {Value: "false"},
		},
		Steps: []workflow.Step{
			{Return: &workflow.ReturnControl{Outputs: map[string]string{"result": "inputs.result", "cached": "true"}}},
			{ID: "after", Type: "capture_return", With: map[string]any{"value": "not-run"}},
		},
	}
	definition := &workflow.Definition{Version: 1, Name: "caller", Dir: t.TempDir(), Steps: []workflow.Step{{
		ID: "call", Uses: workflow.ActionSource{URL: "https://example.test/action"}, Action: action,
		With: map[string]any{"result": "returned"}, Retry: immediateRetry(3),
	}}}
	state, err := New(registry).Run(t.Context(), definition, Options{Stdout: io.Discard, Stderr: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"result": "returned", "cached": true}
	if got := state.Steps["call"].(map[string]any); !reflect.DeepEqual(got, want) || runs != 0 {
		t.Fatalf("outputs = %#v, runs = %d", got, runs)
	}
	if attempts := len(state.Stats.Steps[0].Attempts); attempts != 1 {
		t.Fatalf("attempts = %d", attempts)
	}
}

func TestValidateRejectsReturnInProgrammaticParallelControls(t *testing.T) {
	returnStep := workflow.Step{Return: &workflow.ReturnControl{Outputs: map[string]string{}}}
	tests := []struct {
		name string
		step workflow.Step
	}{
		{name: "concurrent", step: workflow.Step{Concurrent: &workflow.ConcurrentGroup{MaxConcurrency: 1, Steps: []workflow.Step{returnStep, {ID: "work", Type: "missing", With: map[string]any{}}}}}},
		{name: "foreach", step: workflow.Step{ID: "loop", Foreach: &workflow.ForeachGroup{Items: "[]", MaxConcurrency: 1, MaxIterations: 10, Steps: []workflow.Step{returnStep}}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			definition := &workflow.Definition{Version: 1, Name: "invalid", Dir: t.TempDir(), Steps: []workflow.Step{test.step}}
			err := New(step.NewRegistry()).Validate(t.Context(), definition, Options{})
			if err == nil || !strings.Contains(err.Error(), "return is not supported") {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestReturnDoesNotMaskFinallyFailure(t *testing.T) {
	registry := step.NewRegistry()
	cleanupErr := errors.New("cleanup failed")
	if err := registry.Register("return_cleanup_fail", func(map[string]any) (step.Runner, error) {
		return runnerFunc(func(context.Context, step.Request) (step.Result, error) { return step.Result{}, cleanupErr }), nil
	}); err != nil {
		t.Fatal(err)
	}
	definition := &workflow.Definition{Version: 1, Name: "cleanup-failure", Dir: t.TempDir(),
		Steps:   []workflow.Step{{Return: &workflow.ReturnControl{Outputs: map[string]string{"result": `"done"`}}}},
		Finally: []workflow.Step{{ID: "cleanup", Type: "return_cleanup_fail", With: map[string]any{}}},
	}
	_, err := New(registry).Run(t.Context(), definition, Options{Stdout: io.Discard, Stderr: io.Discard})
	if !errors.Is(err, cleanupErr) {
		t.Fatalf("error = %v", err)
	}
}

func TestDryRunDisplaysReturnControl(t *testing.T) {
	registry := step.NewRegistry()
	if err := registry.Register("capture_return", func(map[string]any) (step.Runner, error) { return countingRunner{}, nil }); err != nil {
		t.Fatal(err)
	}
	definition := &workflow.Definition{Version: 1, Name: "dry-return", Dir: t.TempDir(), Steps: []workflow.Step{
		{Return: &workflow.ReturnControl{Outputs: map[string]string{"cached": "true", "artifact": "steps.build.path"}}, If: "vars.cached"},
		{ID: "build", Type: "capture_return", With: map[string]any{}},
	}}
	var output bytes.Buffer
	if _, err := New(registry).Run(t.Context(), definition, Options{DryRun: true, Stdout: &output, Stderr: io.Discard}); err != nil {
		t.Fatal(err)
	}
	want := "1. return (outputs: artifact, cached) if: vars.cached\n2. build (capture_return)\n"
	if output.String() != want {
		t.Fatalf("output = %q, want %q", output.String(), want)
	}
}
