package engine

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/workflow"
)

type finallyTestRunner struct {
	run func(context.Context, step.Request) (step.Result, error)
}

func (runner finallyTestRunner) Run(ctx context.Context, request step.Request) (step.Result, error) {
	return runner.run(ctx, request)
}

func registerFinallyTestRunner(t *testing.T, registry *step.Registry, name string, run func(context.Context, step.Request) (step.Result, error)) {
	t.Helper()
	if err := registry.Register(name, func(map[string]any) (step.Runner, error) {
		return finallyTestRunner{run: run}, nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestFinallyRunsAfterSuccessWithCommittedState(t *testing.T) {
	registry := newTestRegistry(t, nil)
	registerFinallyTestRunner(t, registry, "main", func(context.Context, step.Request) (step.Result, error) {
		return step.Result{
			Outputs:   map[string]any{"value": "main-output"},
			Variables: map[string]any{"main_var": "committed"},
		}, nil
	})
	if err := registry.Register("cleanup", func(raw map[string]any) (step.Runner, error) {
		return finallyTestRunner{run: func(_ context.Context, request step.Request) (step.Result, error) {
			finally := request.Bindings["finally"].(map[string]any)
			if finally["status"] != "succeeded" || len(finally["errors"].([]any)) != 0 || raw["status"] != "succeeded" {
				t.Fatalf("finally binding = %#v, rendered config = %#v", finally, raw)
			}
			if request.Vars["main_var"] != "committed" || request.Steps["prepare"].(map[string]any)["value"] != "main-output" {
				t.Fatalf("cleanup request state = %#v", request)
			}
			return step.Result{
				Outputs:   map[string]any{"value": "cleanup-output"},
				Variables: map[string]any{"cleanup_var": "committed"},
			}, nil
		}}, nil
	}); err != nil {
		t.Fatal(err)
	}

	definition := testDefinition(t, "success", workflow.Step{ID: "prepare", Type: "main"})
	definition.Finally = []workflow.Step{{
		ID: "cleanup", Type: "cleanup",
		If: `finally.status == "succeeded" && len(finally.errors) == 0`, With: map[string]any{"status": "{{ .finally.status }}"},
	}}
	state, err := New(registry).Run(t.Context(), definition, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if state.Vars["cleanup_var"] != "committed" || state.Steps["cleanup"].(map[string]any)["value"] != "cleanup-output" {
		t.Fatalf("state = %#v", state)
	}
	if _, exists := state.Bindings["finally"]; exists {
		t.Fatalf("cleanup-only binding leaked into returned state: %#v", state.Bindings)
	}
	if state.Stats.Total != 2 || state.Stats.Succeeded != 2 || len(state.Stats.Steps) != 2 {
		t.Fatalf("stats = %#v", state.Stats)
	}
}

func TestFinallyContinuesAndAccumulatesStructuredErrors(t *testing.T) {
	registry := newTestRegistry(t, nil)
	mainFailure := errors.New("main broke")
	cleanupFailure := errors.New("cleanup broke")
	registerFinallyTestRunner(t, registry, "main_fail", func(context.Context, step.Request) (step.Result, error) {
		return step.Result{Variables: map[string]any{"uncommitted": true}}, mainFailure
	})
	registerFinallyTestRunner(t, registry, "cleanup_fail", func(_ context.Context, request step.Request) (step.Result, error) {
		finally := request.Bindings["finally"].(map[string]any)
		errorsValue := finally["errors"].([]any)
		if len(errorsValue) != 1 || errorsValue[0].(map[string]any)["step_id"] != "main" {
			t.Fatalf("initial errors = %#v", errorsValue)
		}
		if _, exists := request.Vars["uncommitted"]; exists {
			t.Fatalf("failed step variables were committed: %#v", request.Vars)
		}
		return step.Result{}, cleanupFailure
	})
	secondRan := false
	registerFinallyTestRunner(t, registry, "cleanup_after", func(_ context.Context, request step.Request) (step.Result, error) {
		secondRan = true
		finally := request.Bindings["finally"].(map[string]any)
		errorsValue := finally["errors"].([]any)
		if finally["status"] != "failed" || len(errorsValue) != 2 {
			t.Fatalf("progressive finally binding = %#v", finally)
		}
		second := errorsValue[1].(map[string]any)
		message, _ := second["message"].(string)
		if second["step_id"] != "cleanup_one" || second["step_type"] != "cleanup_fail" || second["status"] != "failed" || !strings.Contains(message, cleanupFailure.Error()) {
			t.Fatalf("cleanup error record = %#v", second)
		}
		return step.Result{}, nil
	})

	definition := testDefinition(t, "failure", workflow.Step{ID: "main", Type: "main_fail"})
	definition.Finally = []workflow.Step{
		{ID: "cleanup_one", Type: "cleanup_fail", If: `any(finally.errors, {.step_id == "main" && .status == "failed"})`, With: map[string]any{}},
		{ID: "cleanup_two", Type: "cleanup_after", If: `len(finally.errors) == 2`, With: map[string]any{}},
	}
	_, err := New(registry).Run(t.Context(), definition, Options{})
	if !errors.Is(err, mainFailure) || !errors.Is(err, cleanupFailure) || !secondRan {
		t.Fatalf("Run() error = %v, second ran = %v", err, secondRan)
	}
	if strings.Index(err.Error(), mainFailure.Error()) > strings.Index(err.Error(), cleanupFailure.Error()) {
		t.Fatalf("errors are out of order: %v", err)
	}
}

func TestFinallyUsesFreshContextAfterCancellation(t *testing.T) {
	registry := newTestRegistry(t, nil)
	cleanupFailure := errors.New("cleanup after cancellation failed")
	registerFinallyTestRunner(t, registry, "main", func(context.Context, step.Request) (step.Result, error) {
		t.Fatal("main step should not start with a canceled context")
		return step.Result{}, nil
	})
	cleanupRan := false
	registerFinallyTestRunner(t, registry, "cleanup", func(ctx context.Context, request step.Request) (step.Result, error) {
		cleanupRan = true
		if err := ctx.Err(); err != nil {
			t.Fatalf("cleanup context is canceled: %v", err)
		}
		finally := request.Bindings["finally"].(map[string]any)
		errorsValue := finally["errors"].([]any)
		if finally["status"] != "canceled" || len(errorsValue) != 1 || errorsValue[0].(map[string]any)["step_id"] != "" {
			t.Fatalf("cancellation binding = %#v", finally)
		}
		return step.Result{}, cleanupFailure
	})
	definition := testDefinition(t, "canceled", workflow.Step{ID: "main", Type: "main"})
	definition.Finally = []workflow.Step{{ID: "cleanup", Type: "cleanup"}}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	var events []ProgressEvent
	_, err := New(registry).Run(ctx, definition, Options{
		Stdout: io.Discard, Stderr: io.Discard,
		Progress: func(event ProgressEvent) { events = append(events, event) },
	})
	if !errors.Is(err, context.Canceled) || !errors.Is(err, cleanupFailure) || !cleanupRan {
		t.Fatalf("Run() error = %v, cleanup ran = %v", err, cleanupRan)
	}
	last := events[len(events)-1]
	if last.Kind != WorkflowFinished || last.Status != StatusCanceled || last.Stats.Total != 2 || last.Stats.Failed != 1 {
		t.Fatalf("terminal event = %#v", last)
	}
}

func TestCompositeActionFinallyRunsPerAttemptAndFeedsOutputs(t *testing.T) {
	registry := newTestRegistry(t, nil)
	mainRuns, cleanupRuns := 0, 0
	registerFinallyTestRunner(t, registry, "action_main", func(context.Context, step.Request) (step.Result, error) {
		mainRuns++
		return step.Result{}, nil
	})
	registerFinallyTestRunner(t, registry, "action_cleanup", func(context.Context, step.Request) (step.Result, error) {
		cleanupRuns++
		if cleanupRuns == 1 {
			return step.Result{}, errors.New("transient cleanup failure")
		}
		return step.Result{Outputs: map[string]any{"value": "clean"}}, nil
	})
	action := testAction(t, "action", workflow.Step{ID: "work", Type: "action_main"})
	action.Outputs = map[string]workflow.ActionOutput{"result": {Value: "steps.cleanup.value"}}
	action.Finally = []workflow.Step{{ID: "cleanup", Type: "action_cleanup"}}
	definition := testDefinition(
		t, "caller",
		attemptStep("call", immediateRetry(2), workflow.Step{
			Uses: workflow.ActionSource{URL: "https://example.test/action"}, Action: action,
			With: map[string]any{},
		}))

	state, err := New(registry).Run(t.Context(), definition, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if mainRuns != 2 || cleanupRuns != 2 || attemptBody(state, "call", "call_body")["result"] != "clean" {
		t.Fatalf("main runs = %d, cleanup runs = %d, state = %#v", mainRuns, cleanupRuns, state)
	}
}

func TestFinallyRejectsControlBeforeContextValidation(t *testing.T) {
	definition := testDefinition(t, "control-cleanup", workflow.Step{ID: "main", Type: "main"})
	definition.Vars = map[string]any{"items": []any{"resource"}}
	definition.Finally = []workflow.Step{{ID: "cleanup", Foreach: &workflow.ForeachGroup{
		Items: "vars.items", MaxConcurrency: 1, FailFast: true,
		Steps: []workflow.Step{{ID: "remove", Type: "cleanup"}},
	}}}
	err := New(newTestRegistry(t, nil)).Validate(t.Context(), definition, Options{})
	if err == nil || !strings.Contains(err.Error(), "nested foreach controls are not supported") {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestFinallyDryRunShowsWorkflowAndActionSections(t *testing.T) {
	registry := newTestRegistry(t, nil)
	registerFinallyTestRunner(t, registry, "noop", func(context.Context, step.Request) (step.Result, error) {
		return step.Result{}, nil
	})
	action := testAction(t, "action", workflow.Step{ID: "inside", Type: "noop"})
	action.Finally = []workflow.Step{{ID: "inside_cleanup", Type: "noop"}}
	definition := testDefinition(t, "dry", workflow.Step{ID: "call", Uses: workflow.ActionSource{URL: "https://example.test/action"}, Action: action})
	definition.Finally = []workflow.Step{{ID: "cleanup", Type: "noop"}}
	var output bytes.Buffer
	if _, err := New(registry).Run(t.Context(), definition, Options{DryRun: true, Stdout: &output, Stderr: io.Discard}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"inside_cleanup (noop)", "finally:\n", "cleanup (noop)"} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("dry-run output = %q, want %q", output.String(), want)
		}
	}
}

func TestDeferDryRunShowsAttachedSection(t *testing.T) {
	registry := newTestRegistry(t, nil)
	registerFinallyTestRunner(t, registry, "noop", func(context.Context, step.Request) (step.Result, error) {
		return step.Result{}, nil
	})
	definition := testDefinition(t, "dry-defer", workflow.Step{
		ID: "create", Type: "noop", Defer: []workflow.Step{{ID: "remove", Type: "noop"}},
	})
	var output bytes.Buffer
	if _, err := New(registry).Run(t.Context(), definition, Options{DryRun: true, Stdout: &output, Stderr: io.Discard}); err != nil {
		t.Fatal(err)
	}
	if want := "create (noop)\n   defer:\n      1.1 remove (noop)"; !strings.Contains(output.String(), want) {
		t.Fatalf("dry-run output = %q, want substring %q", output.String(), want)
	}
}
