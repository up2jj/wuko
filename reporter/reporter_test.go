package reporter

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/up2jj/wuko/correlation"
	"github.com/up2jj/wuko/diagnostic"
	"github.com/up2jj/wuko/engine"
)

func TestGroupPreservesOrderAndJoinsFinishErrors(t *testing.T) {
	var events []string
	firstErr := errors.New("first failed")
	secondErr := errors.New("second failed")
	reporters := Group{
		recordingReporter("first", &events, firstErr),
		recordingReporter("second", &events, secondErr),
	}

	reporters.Progress(engine.ProgressEvent{})
	reporters.Diagnostic(diagnostic.Event{})
	err := reporters.Finish(t.Context(), Outcome{WorkflowName: "check"})

	if !errors.Is(err, firstErr) || !errors.Is(err, secondErr) {
		t.Fatalf("Finish() error = %v, want both errors", err)
	}
	want := "first:progress,second:progress,first:diagnostic,second:diagnostic,first:finish,second:finish"
	if got := strings.Join(events, ","); got != want {
		t.Fatalf("events = %q, want %q", got, want)
	}
}

func TestGroupZeroValueIsNoOp(t *testing.T) {
	var reporters Group
	reporters.Progress(engine.ProgressEvent{})
	reporters.Diagnostic(diagnostic.Event{})
	if err := reporters.Finish(t.Context(), Outcome{}); err != nil {
		t.Fatalf("Finish() error = %v, want nil", err)
	}
}

func TestFuncsCallsOnlyConfiguredCallbacks(t *testing.T) {
	progressCalled := false
	finishCalled := false
	reporter := Funcs{
		ProgressFunc: func(engine.ProgressEvent) { progressCalled = true },
		FinishFunc: func(ctx context.Context, outcome Outcome) error {
			finishCalled = true
			if ctx != t.Context() {
				t.Error("Finish() context differs from caller context")
			}
			if outcome.WorkflowName != "check" {
				t.Errorf("Finish() workflow = %q, want check", outcome.WorkflowName)
			}
			return nil
		},
	}

	reporter.Progress(engine.ProgressEvent{})
	reporter.Diagnostic(diagnostic.Event{})
	if err := reporter.Finish(t.Context(), Outcome{WorkflowName: "check"}); err != nil {
		t.Fatal(err)
	}
	if !progressCalled || !finishCalled {
		t.Fatalf("callbacks = (progress %v, finish %v), want both true", progressCalled, finishCalled)
	}
}

func TestFuncsZeroValueIsNoOp(t *testing.T) {
	var reporter Funcs
	reporter.Progress(engine.ProgressEvent{})
	reporter.Diagnostic(diagnostic.Event{})
	if err := reporter.Finish(t.Context(), Outcome{}); err != nil {
		t.Fatalf("Finish() error = %v, want nil", err)
	}
}

func TestSessionStampsMixedEventsAndOutcome(t *testing.T) {
	invocationID := correlation.InvocationID("external")
	var progress engine.ProgressEvent
	var diagnosticEvent diagnostic.Event
	var outcome Outcome
	session := NewSession(invocationID, Funcs{
		ProgressFunc:   func(event engine.ProgressEvent) { progress = event },
		DiagnosticFunc: func(event diagnostic.Event) { diagnosticEvent = event },
		FinishFunc: func(_ context.Context, reported Outcome) error {
			outcome = reported
			return nil
		},
	})

	session.Progress(engine.ProgressEvent{RunID: correlation.RunID("run")})
	session.Diagnostic(diagnostic.Event{RunID: correlation.RunID("run")})
	if err := session.Finish(t.Context(), Outcome{RunID: correlation.RunID("run")}); err != nil {
		t.Fatalf("Finish() error = %v", err)
	}
	if progress.InvocationID != invocationID || progress.Sequence != 1 || progress.RunID != "run" {
		t.Fatalf("progress = %#v", progress)
	}
	if diagnosticEvent.InvocationID != invocationID || diagnosticEvent.Sequence != 2 || diagnosticEvent.RunID != "run" {
		t.Fatalf("diagnostic = %#v", diagnosticEvent)
	}
	if outcome.InvocationID != invocationID || outcome.RunID != "run" {
		t.Fatalf("outcome = %#v", outcome)
	}
}

func TestSessionZeroValueAndConcurrentDelivery(t *testing.T) {
	const total = 32
	sequences := make([]correlation.Sequence, 0, total)
	var sequencesMu sync.Mutex
	var session Session
	session.reporters = Group{Funcs{ProgressFunc: func(event engine.ProgressEvent) {
		sequencesMu.Lock()
		sequences = append(sequences, event.Sequence)
		sequencesMu.Unlock()
	}}}

	var wait sync.WaitGroup
	for range total {
		wait.Go(func() { session.Progress(engine.ProgressEvent{}) })
	}
	wait.Wait()
	if session.InvocationID() == "" {
		t.Fatal("zero-value session did not generate an invocation ID")
	}
	if len(sequences) != total {
		t.Fatalf("delivered %d events, want %d", len(sequences), total)
	}
	for index, sequence := range sequences {
		if want := correlation.Sequence(index + 1); sequence != want {
			t.Fatalf("sequence[%d] = %d, want %d", index, sequence, want)
		}
	}
}

func TestSessionWithSharesStampingAndPreservesFanoutOrder(t *testing.T) {
	var events []string
	base := Funcs{ProgressFunc: func(event engine.ProgressEvent) {
		events = append(events, "base:"+string(rune('0'+event.Sequence)))
	}}
	extra := Funcs{ProgressFunc: func(event engine.ProgressEvent) {
		events = append(events, "extra:"+string(rune('0'+event.Sequence)))
	}}
	session := NewSession("invocation", base)
	session.Progress(engine.ProgressEvent{})
	session.With(extra).Progress(engine.ProgressEvent{})
	if got, want := strings.Join(events, ","), "base:1,base:2,extra:2"; got != want {
		t.Fatalf("events = %q, want %q", got, want)
	}
}

func TestSessionReporterCanReadInvocationID(t *testing.T) {
	var session *Session
	session = NewSession("invocation", Funcs{ProgressFunc: func(engine.ProgressEvent) {
		if got := session.InvocationID(); got != "invocation" {
			t.Errorf("InvocationID() = %q", got)
		}
	}})
	session.Progress(engine.ProgressEvent{})
}

func recordingReporter(name string, events *[]string, finishErr error) Reporter {
	return Funcs{
		ProgressFunc: func(engine.ProgressEvent) { *events = append(*events, name+":progress") },
		DiagnosticFunc: func(diagnostic.Event) {
			*events = append(*events, name+":diagnostic")
		},
		FinishFunc: func(context.Context, Outcome) error {
			*events = append(*events, name+":finish")
			return finishErr
		},
	}
}
