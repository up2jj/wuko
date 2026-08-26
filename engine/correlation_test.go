package engine

import (
	"context"
	"errors"
	"testing"

	"github.com/up2jj/wuko/correlation"
	"github.com/up2jj/wuko/diagnostic"
	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/workflow"
)

func TestRunCorrelationPropagatesThroughRetriesAndStats(t *testing.T) {
	invocationID := correlation.InvocationID("invocation")
	var attempts int
	registry := newTestRegistry(t, map[string]step.Builder{"flaky": func(map[string]any) (step.Runner, error) {
		return runnerFunc(func(context.Context, step.Request) (step.Result, error) {
			attempts++
			if attempts == 1 {
				return step.Result{}, errors.New("temporary")
			}
			return step.Result{}, nil
		}), nil
	}})
	definition := testDefinition(t, "correlated", workflow.Step{ID: "run", Type: "flaky", Retry: immediateRetry(2), With: map[string]any{}})
	var events []ProgressEvent
	state, err := New(registry).Run(t.Context(), definition, Options{InvocationID: invocationID, Progress: func(event ProgressEvent) {
		events = append(events, event)
	}})
	if err != nil {
		t.Fatal(err)
	}
	if state.Stats.InvocationID != invocationID || state.Stats.RunID == "" {
		t.Fatalf("run stats identity = %#v", state.Stats)
	}
	if len(state.Stats.Steps) != 1 || state.Stats.Steps[0].StepRunID == "" {
		t.Fatalf("step stats = %#v", state.Stats.Steps)
	}
	runID := state.Stats.RunID
	stepRunID := state.Stats.Steps[0].StepRunID
	attemptEvents := 0
	for _, event := range events {
		if event.InvocationID != invocationID || event.RunID != runID {
			t.Fatalf("event identity = %#v", event)
		}
		if event.Kind == AttemptStarted || event.Kind == AttemptFinished || event.Kind == RetryScheduled {
			attemptEvents++
			if event.StepRunID != stepRunID {
				t.Fatalf("attempt event step run ID = %q, want %q", event.StepRunID, stepRunID)
			}
		}
	}
	if attemptEvents != 5 {
		t.Fatalf("attempt lifecycle events = %d, want 5", attemptEvents)
	}
}

func TestCompositeRunCorrelationIncludesCallingStep(t *testing.T) {
	registry := newTestRegistry(t, map[string]step.Builder{"ok": func(map[string]any) (step.Runner, error) {
		return runnerFunc(func(context.Context, step.Request) (step.Result, error) { return step.Result{}, nil }), nil
	}})
	action := testAction(t, "child", workflow.Step{ID: "inside", Type: "ok", With: map[string]any{}})
	definition := testDefinition(t, "parent", workflow.Step{
		ID: "call", Uses: workflow.ActionSource{URL: "https://example.test/action"}, Action: action, With: map[string]any{},
	})
	var events []ProgressEvent
	if _, err := New(registry).Run(t.Context(), definition, Options{InvocationID: "invocation", Progress: func(event ProgressEvent) {
		events = append(events, event)
	}}); err != nil {
		t.Fatal(err)
	}
	var parentWorkflow, childWorkflow, callingStep ProgressEvent
	for _, event := range events {
		switch {
		case event.Kind == WorkflowStarted && event.WorkflowName == "parent":
			parentWorkflow = event
		case event.Kind == StepStarted && event.WorkflowName == "parent" && event.StepID == "call":
			callingStep = event
		case event.Kind == WorkflowStarted && event.WorkflowName == "child":
			childWorkflow = event
		}
	}
	if parentWorkflow.RunID == "" || callingStep.StepRunID == "" || childWorkflow.RunID == "" {
		t.Fatalf("missing correlation events: parent=%#v call=%#v child=%#v", parentWorkflow, callingStep, childWorkflow)
	}
	if childWorkflow.RunID == parentWorkflow.RunID || childWorkflow.ParentRunID != parentWorkflow.RunID {
		t.Fatalf("child run ancestry = %#v", childWorkflow)
	}
	if childWorkflow.ParentStepRunID != callingStep.StepRunID {
		t.Fatalf("child parent step = %q, want %q", childWorkflow.ParentStepRunID, callingStep.StepRunID)
	}
}

func TestLoopOccurrencesReceiveDistinctStepRunIDs(t *testing.T) {
	var polls int
	registry := newTestRegistry(t, map[string]step.Builder{"poll": func(map[string]any) (step.Runner, error) {
		return runnerFunc(func(context.Context, step.Request) (step.Result, error) {
			polls++
			return step.Result{Outputs: map[string]any{"done": polls == 2}}, nil
		}), nil
	}})
	definition := testDefinition(t, "loop", workflow.Step{ID: "wait", Loop: &workflow.LoopGroup{
		Until: "steps.poll.done", MaxIterations: 2,
		Steps: []workflow.Step{{ID: "poll", Type: "poll", With: map[string]any{}}},
	}})
	var occurrences []correlation.StepRunID
	if _, err := New(registry).Run(t.Context(), definition, Options{Progress: func(event ProgressEvent) {
		if event.Kind == StepStarted && event.StepID == "poll" {
			occurrences = append(occurrences, event.StepRunID)
		}
	}}); err != nil {
		t.Fatal(err)
	}
	if len(occurrences) != 2 || occurrences[0] == "" || occurrences[0] == occurrences[1] {
		t.Fatalf("loop step run IDs = %#v", occurrences)
	}
}

func TestMatrixAndSkippedOccurrencesReceiveStepRunIDs(t *testing.T) {
	registry := newTestRegistry(t, map[string]step.Builder{"ok": func(map[string]any) (step.Runner, error) {
		return runnerFunc(func(context.Context, step.Request) (step.Result, error) { return step.Result{}, nil }), nil
	}})
	definition := testDefinition(t, "occurrences",
		workflow.Step{ID: "skip", Type: "ok", If: "false", With: map[string]any{}},
		workflow.Step{ID: "matrix", Matrix: &workflow.MatrixGroup{
			Axes: workflow.MatrixAxes{{Name: "value", Values: []any{1, 2}}}, MaxConcurrency: 2,
			Steps: []workflow.Step{{ID: "inside", Type: "ok", With: map[string]any{}}},
		}},
	)
	var skipped correlation.StepRunID
	var matrixOccurrences []correlation.StepRunID
	state, err := New(registry).Run(t.Context(), definition, Options{Progress: func(event ProgressEvent) {
		if event.Kind == StepFinished && event.StepID == "skip" {
			skipped = event.StepRunID
		}
		if event.Kind == StepStarted && event.StepID == "inside" {
			matrixOccurrences = append(matrixOccurrences, event.StepRunID)
		}
	}})
	if err != nil {
		t.Fatal(err)
	}
	if skipped == "" || state.Stats.Steps[0].StepRunID != skipped {
		t.Fatalf("skipped identity = %q, stats = %#v", skipped, state.Stats.Steps[0])
	}
	if len(matrixOccurrences) != 2 || matrixOccurrences[0] == "" || matrixOccurrences[0] == matrixOccurrences[1] {
		t.Fatalf("matrix step run IDs = %#v", matrixOccurrences)
	}
}

func TestRunCorrelationExistsForDryRunAndValidationFailure(t *testing.T) {
	dryDefinition := testDefinition(t, "dry", workflow.Step{ID: "run", Type: "missing", With: map[string]any{}})
	var failed diagnostic.Event
	_, err := New(newTestRegistry(t, nil)).Run(t.Context(), dryDefinition, Options{InvocationID: "invocation", Diagnostics: func(event diagnostic.Event) {
		if event.Status == diagnostic.StatusFailed {
			failed = event
		}
	}})
	if err == nil || failed.InvocationID != "invocation" || failed.RunID == "" {
		t.Fatalf("validation error = %v, diagnostic = %#v", err, failed)
	}

	dryRegistry := newTestRegistry(t, map[string]step.Builder{"ok": func(map[string]any) (step.Runner, error) {
		return runnerFunc(func(context.Context, step.Request) (step.Result, error) { return step.Result{}, nil }), nil
	}})
	dryDefinition = testDefinition(t, "dry", workflow.Step{ID: "run", Type: "ok", With: map[string]any{}})
	state, err := New(dryRegistry).Run(t.Context(), dryDefinition, Options{InvocationID: "invocation", DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if state.Stats.InvocationID != "invocation" || state.Stats.RunID == "" {
		t.Fatalf("dry-run stats identity = %#v", state.Stats)
	}
}
