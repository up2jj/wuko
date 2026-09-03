package engine

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/workflow"
)

func TestAttachedDeferUnwindsGroupsBeforeFinally(t *testing.T) {
	registry := newTestRegistry(t, nil)
	var events []string
	register := func(kind string, run func(step.Request) error) {
		t.Helper()
		registerFinallyTestRunner(t, registry, kind, func(_ context.Context, request step.Request) (step.Result, error) {
			events = append(events, request.StepID)
			if run != nil {
				return step.Result{}, run(request)
			}
			return step.Result{Outputs: map[string]any{"done": true}}, nil
		})
	}
	register("create", nil)
	register("cleanup", nil)
	register("final", func(request step.Request) error {
		finally := request.Bindings["finally"].(map[string]any)
		if finally["status"] != "succeeded" || len(finally["errors"].([]any)) != 0 {
			t.Fatalf("finally binding = %#v", finally)
		}
		return nil
	})
	definition := testDefinition(t, "defer-order",
		workflow.Step{ID: "first", Type: "create", Defer: []workflow.Step{{ID: "first_a", Type: "cleanup"}, {ID: "first_b", Type: "cleanup"}}},
		workflow.Step{ID: "second", Type: "create", Defer: []workflow.Step{{ID: "second_a", Type: "cleanup"}}},
	)
	definition.Finally = []workflow.Step{{ID: "final", Type: "final"}}
	state, err := New(registry).Run(t.Context(), definition, Options{})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"first", "second", "second_a", "first_a", "first_b", "final"}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %#v, want %#v", events, want)
	}
	for _, id := range []string{"second_a", "first_a", "first_b"} {
		if state.Steps[id].(map[string]any)["done"] != true {
			t.Fatalf("cleanup %q was not committed: %#v", id, state.Steps)
		}
	}
}

func TestAttachedDeferRegistersOnlyAfterSuccessAndContinuesErrors(t *testing.T) {
	registry := newTestRegistry(t, nil)
	mainFailure := errors.New("create failed")
	cleanupFailure := errors.New("cleanup failed")
	var events []string
	registerFinallyTestRunner(t, registry, "ok", func(_ context.Context, request step.Request) (step.Result, error) {
		events = append(events, request.StepID)
		return step.Result{}, nil
	})
	registerFinallyTestRunner(t, registry, "fail_main", func(_ context.Context, request step.Request) (step.Result, error) {
		events = append(events, request.StepID)
		return step.Result{}, mainFailure
	})
	registerFinallyTestRunner(t, registry, "fail_cleanup", func(_ context.Context, request step.Request) (step.Result, error) {
		events = append(events, request.StepID)
		return step.Result{}, cleanupFailure
	})
	registerFinallyTestRunner(t, registry, "inspect", func(_ context.Context, request step.Request) (step.Result, error) {
		events = append(events, request.StepID)
		errorsValue := request.Bindings["finally"].(map[string]any)["errors"].([]any)
		if len(errorsValue) != 2 || errorsValue[1].(map[string]any)["step_id"] != "cleanup_fail" {
			t.Fatalf("progressive finally errors = %#v", errorsValue)
		}
		return step.Result{}, nil
	})
	definition := testDefinition(t, "defer-failure",
		workflow.Step{ID: "created", Type: "ok", Defer: []workflow.Step{{ID: "cleanup_fail", Type: "fail_cleanup"}, {ID: "cleanup_after", Type: "ok"}}},
		workflow.Step{ID: "failed", Type: "fail_main", Defer: []workflow.Step{{ID: "never", Type: "ok"}}},
	)
	definition.Finally = []workflow.Step{{ID: "inspect", Type: "inspect"}}
	state, err := New(registry).Run(t.Context(), definition, Options{})
	if state != nil || !errors.Is(err, mainFailure) || !errors.Is(err, cleanupFailure) {
		t.Fatalf("Run() state = %#v, error = %v", state, err)
	}
	want := []string{"created", "failed", "cleanup_fail", "cleanup_after", "inspect"}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %#v, want %#v", events, want)
	}
	if strings.Index(err.Error(), mainFailure.Error()) > strings.Index(err.Error(), cleanupFailure.Error()) {
		t.Fatalf("error order = %v", err)
	}
}

func TestAttachedDeferCapturesWorkingDirectory(t *testing.T) {
	registry := newTestRegistry(t, nil)
	dir := t.TempDir()
	var cleanupDir string
	registerFinallyTestRunner(t, registry, "ok", func(context.Context, step.Request) (step.Result, error) {
		return step.Result{}, nil
	})
	registerFinallyTestRunner(t, registry, "observe_dir", func(_ context.Context, request step.Request) (step.Result, error) {
		cleanupDir = request.RunDir
		return step.Result{}, nil
	})
	definition := testDefinition(t, "defer-dir", workflow.Step{WorkingDirectory: dir, Steps: []workflow.Step{{
		ID: "create", Type: "ok", Defer: []workflow.Step{{ID: "cleanup", Type: "observe_dir"}},
	}}})
	if _, err := New(registry).Run(t.Context(), definition, Options{RunDir: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	if cleanupDir != dir {
		t.Fatalf("cleanup RunDir = %q, want %q", cleanupDir, dir)
	}
}

func TestCompositeActionDeferRunsPerAttempt(t *testing.T) {
	registry := newTestRegistry(t, nil)
	mainRuns, cleanupRuns := 0, 0
	registerFinallyTestRunner(t, registry, "action_main", func(context.Context, step.Request) (step.Result, error) {
		mainRuns++
		return step.Result{}, nil
	})
	registerFinallyTestRunner(t, registry, "action_cleanup", func(context.Context, step.Request) (step.Result, error) {
		cleanupRuns++
		if cleanupRuns == 1 {
			return step.Result{}, errors.New("transient defer failure")
		}
		return step.Result{Outputs: map[string]any{"value": "clean"}}, nil
	})
	action := testAction(t, "action", workflow.Step{
		ID: "work", Type: "action_main", Defer: []workflow.Step{{ID: "cleanup", Type: "action_cleanup"}},
	})
	action.Outputs = map[string]workflow.ActionOutput{"result": {Value: "steps.cleanup.value"}}
	definition := testDefinition(t, "caller", attemptStep("call", immediateRetry(2), workflow.Step{
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

func TestAttachedDeferProgressCountsUnregisteredCleanup(t *testing.T) {
	registry := newTestRegistry(t, nil)
	registerFinallyTestRunner(t, registry, "ok", func(context.Context, step.Request) (step.Result, error) {
		return step.Result{}, nil
	})
	definition := testDefinition(t, "defer-progress",
		workflow.Step{ID: "skipped", Type: "ok", If: "false", Defer: []workflow.Step{{ID: "skipped_cleanup", Type: "ok"}}},
		workflow.Step{ID: "active", Type: "ok", Defer: []workflow.Step{{ID: "active_cleanup", Type: "ok"}}},
	)
	var finished ProgressEvent
	if _, err := New(registry).Run(t.Context(), definition, Options{Progress: func(event ProgressEvent) {
		if event.Kind == WorkflowFinished {
			finished = event
		}
	}}); err != nil {
		t.Fatal(err)
	}
	if finished.Stats.Total != 4 || finished.Stats.Skipped != 2 || len(finished.Stats.Steps) != 4 {
		t.Fatalf("stats = %#v", finished.Stats)
	}
	for i, stats := range finished.Stats.Steps {
		if stats.Index != i+1 {
			t.Fatalf("step stats = %#v", finished.Stats.Steps)
		}
	}
}
