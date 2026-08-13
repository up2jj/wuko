package engine

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/workflow"
)

func TestRunReportsProgressAndCollectsStats(t *testing.T) {
	registry := step.NewRegistry()
	var requests []step.Request
	if err := registry.Register("retry", func(map[string]any) (step.Runner, error) {
		return retryTestRunner{failures: 1, requests: &requests}, nil
	}); err != nil {
		t.Fatal(err)
	}
	definition := &workflow.Definition{
		Version: 1, Name: "progress", Dir: t.TempDir(), Vars: map[string]any{"run": false},
		Steps: []workflow.Step{
			{ID: "publish", Type: "retry", Retry: immediateRetry(2), With: map[string]any{}},
			{ID: "deploy", Type: "retry", If: "vars.run", With: map[string]any{}},
		},
	}
	var events []ProgressEvent
	state, err := New(registry).Run(t.Context(), definition, Options{
		Stdout: io.Discard, Stderr: io.Discard,
		Progress: func(event ProgressEvent) { events = append(events, event) },
	})
	if err != nil {
		t.Fatal(err)
	}
	stats := state.Stats
	if stats.Total != 2 || stats.Succeeded != 1 || stats.Skipped != 1 || stats.Attempts != 2 || stats.Retries != 1 {
		t.Fatalf("stats = %#v", stats)
	}
	if len(stats.Steps) != 2 || len(stats.Steps[0].Attempts) != 2 || stats.Steps[1].Status != StatusSkipped {
		t.Fatalf("step stats = %#v", stats.Steps)
	}
	wantKinds := []ProgressKind{
		WorkflowStarted, StepStarted, AttemptStarted, AttemptFinished, RetryScheduled,
		AttemptStarted, AttemptFinished, StepFinished, StepFinished, WorkflowFinished,
	}
	if len(events) != len(wantKinds) {
		t.Fatalf("event count = %d, want %d: %#v", len(events), len(wantKinds), events)
	}
	for i, want := range wantKinds {
		if events[i].Kind != want {
			t.Fatalf("event %d kind = %q, want %q", i, events[i].Kind, want)
		}
	}
	if events[3].Status != StatusFailed || events[5].Attempt != 2 || events[9].Stats.Attempts != 2 {
		t.Fatalf("events = %#v", events)
	}
}

type alwaysFailRunner struct{}

func (alwaysFailRunner) Run(context.Context, step.Request) (step.Result, error) {
	return step.Result{}, errors.New("broken")
}

func TestRunReportsTerminalFailure(t *testing.T) {
	registry := step.NewRegistry()
	if err := registry.Register("fail", func(map[string]any) (step.Runner, error) { return alwaysFailRunner{}, nil }); err != nil {
		t.Fatal(err)
	}
	definition := &workflow.Definition{Version: 1, Name: "failure", Dir: t.TempDir(), Steps: []workflow.Step{{ID: "break", Type: "fail", With: map[string]any{}}}}
	var events []ProgressEvent
	_, err := New(registry).Run(t.Context(), definition, Options{Progress: func(event ProgressEvent) { events = append(events, event) }})
	if err == nil {
		t.Fatal("expected execution error")
	}
	last := events[len(events)-1]
	if last.Kind != WorkflowFinished || last.Status != StatusFailed || last.Stats.Failed != 1 || last.Stats.Attempts != 1 {
		t.Fatalf("last event = %#v", last)
	}
}
