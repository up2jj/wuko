package engine

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/up2jj/wuko/diagnostic"
	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/workflow"
)

func TestSynchronizeWriterPreservesTerminalFileCapability(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = reader.Close()
		_ = writer.Close()
	})

	wrapped := synchronizeWriter(&sync.Mutex{}, writer)
	terminal, ok := wrapped.(terminalFile)
	if !ok {
		t.Fatalf("synchronized writer type %T does not preserve terminal capability", wrapped)
	}
	if terminal.Fd() != writer.Fd() {
		t.Fatalf("fd = %d, want %d", terminal.Fd(), writer.Fd())
	}
}

func TestSynchronizeWriterKeepsGenericWriterGeneric(t *testing.T) {
	wrapped := synchronizeWriter(&sync.Mutex{}, &strings.Builder{})
	if _, ok := wrapped.(terminalFile); ok {
		t.Fatalf("generic synchronized writer type %T unexpectedly has terminal capability", wrapped)
	}
}

func TestRunReportsProgressAndCollectsStats(t *testing.T) {
	var requests []step.Request
	registry := newTestRegistry(t, map[string]step.Builder{"retry": func(map[string]any) (step.Runner, error) {
		return retryTestRunner{failures: 1, requests: &requests}, nil
	}})
	definition := testDefinition(t, "progress",
		workflow.Step{ID: "publish", Type: "retry", Retry: immediateRetry(2)},
		workflow.Step{ID: "deploy", Type: "retry", If: "vars.run"},
	)
	definition.Vars = map[string]any{"run": false}
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
	registry := newTestRegistry(t, map[string]step.Builder{"fail": func(map[string]any) (step.Runner, error) { return alwaysFailRunner{}, nil }})
	definition := testDefinition(t, "failure", workflow.Step{ID: "break", Type: "fail"})
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

func TestRunReportsDiagnosticFailurePhaseAndLocation(t *testing.T) {
	registry := newTestRegistry(t, map[string]step.Builder{"fail": func(map[string]any) (step.Runner, error) { return alwaysFailRunner{}, nil }})
	location := diagnostic.Location{Source: "/project/workflow.yaml", Line: 8, Column: 5}
	definition := testDefinition(t, "failure", workflow.Step{ID: "break", Type: "fail", With: map[string]any{"token": "secret", "message": "visible"}, Location: location})
	definition.Location = diagnostic.Location{Source: "/project/workflow.yaml", Line: 1, Column: 1}
	var events []diagnostic.Event
	_, err := New(registry).Run(t.Context(), definition, Options{Diagnostics: func(event diagnostic.Event) { events = append(events, event) }})
	if err == nil {
		t.Fatal("expected execution error")
	}
	wants := map[diagnostic.Phase]diagnostic.Status{
		diagnostic.PhaseCondition: diagnostic.StatusSucceeded,
		diagnostic.PhaseRender:    diagnostic.StatusSucceeded,
		diagnostic.PhaseRunner:    diagnostic.StatusSucceeded,
		diagnostic.PhaseAttempt:   diagnostic.StatusFailed,
	}
	for _, event := range events {
		if want, ok := wants[event.Phase]; ok && event.Status == want {
			if event.Location != location {
				t.Fatalf("event location = %#v, want %#v", event.Location, location)
			}
			delete(wants, event.Phase)
		}
		if event.Phase == diagnostic.PhaseRender && event.Status == diagnostic.StatusSucceeded {
			configuration := event.Attributes[0].Value
			if strings.Contains(configuration, "secret") || !strings.Contains(configuration, "<redacted>") {
				t.Fatalf("configuration = %s", configuration)
			}
		}
	}
	if len(wants) != 0 {
		t.Fatalf("missing diagnostics = %#v; events = %#v", wants, events)
	}
}
