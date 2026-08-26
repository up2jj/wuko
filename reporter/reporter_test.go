package reporter

import (
	"context"
	"errors"
	"strings"
	"testing"

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
