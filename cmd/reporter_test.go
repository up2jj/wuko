package cmd

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/up2jj/wuko/correlation"
	"github.com/up2jj/wuko/diagnostic"
	"github.com/up2jj/wuko/engine"
	reporterpkg "github.com/up2jj/wuko/reporter"
	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/webui"
	"github.com/up2jj/wuko/workflow"
)

func TestFinishReportersOnceFinalizesOnlyFirstOutcome(t *testing.T) {
	var outcomes []reporterpkg.Outcome
	reporters := &runReporters{group: reporterpkg.Group{reporterpkg.Funcs{
		FinishFunc: func(context.Context, reporterpkg.Outcome) error {
			outcomes = append(outcomes, reporterpkg.Outcome{})
			return nil
		},
	}}}
	finish := finishReportersOnce(reporters)
	if err, called := finish(t.Context(), "check", nil, errors.New("preparation failed"), false); err != nil || !called {
		t.Fatalf("first finish = (%v, %v), want (nil, true)", err, called)
	}
	if err, called := finish(t.Context(), "check", &engine.State{}, nil, false); err != nil || called {
		t.Fatalf("second finish = (%v, %v), want (nil, false)", err, called)
	}
	if len(outcomes) != 1 {
		t.Fatalf("outcomes = %d, want one", len(outcomes))
	}
}

func TestRunReportersCompleteBuildsSafeOutcome(t *testing.T) {
	var got reporterpkg.Outcome
	contextDetached := false
	reporters := &runReporters{group: reporterpkg.Group{reporterpkg.Funcs{
		FinishFunc: func(ctx context.Context, outcome reporterpkg.Outcome) error {
			got = outcome
			contextDetached = ctx.Err() == nil
			return nil
		},
	}}}
	reporters.Progress(engine.ProgressEvent{
		Kind: engine.WorkflowFinished, Depth: 0, Status: engine.StatusTimedOut, WorkflowName: "check",
		RunID: "terminal-run", ParentRunID: "parent-run", ParentStepRunID: "parent-step",
		Stats: engine.RunStats{RunID: "terminal-run", Total: 4, Failed: 1, Duration: 3 * time.Second},
	})
	state := &engine.State{
		Stats: engine.RunStats{
			RunID: "terminal-run", ParentRunID: "parent-run", ParentStepRunID: "parent-step",
			Total: 4, Failed: 1, Duration: 3 * time.Second,
		},
		Outputs: map[string]any{"nested": map[string]any{"value": "original"}},
		Env:     map[string]string{"SECRET": "hidden"}, Vars: map[string]any{"private": true},
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	runErr := context.DeadlineExceeded
	if err := reporters.complete(ctx, "check", state, runErr, false); err != nil {
		t.Fatal(err)
	}

	state.Outputs["nested"].(map[string]any)["value"] = "changed"
	if !contextDetached {
		t.Error("Finish() context remained canceled")
	}
	if got.WorkflowName != "check" || got.Status != engine.StatusTimedOut || !errors.Is(got.Err, runErr) {
		t.Fatalf("outcome = %#v, want named timed-out outcome", got)
	}
	if got.InvocationID == "" || got.RunID != "terminal-run" || got.ParentRunID != "parent-run" || got.ParentStepRunID != "parent-step" {
		t.Fatalf("outcome identity = %#v", got)
	}
	if got.Stats.Total != 4 || got.Stats.Duration != 3*time.Second {
		t.Fatalf("stats = %#v, want terminal event stats", got.Stats)
	}
	if value := got.Outputs["nested"].(map[string]any)["value"]; value != "original" {
		t.Fatalf("cloned output = %q, want original", value)
	}
}

func TestRunReportersCompleteFallsBackToTerminalEventWithoutState(t *testing.T) {
	var got reporterpkg.Outcome
	reporters := &runReporters{group: reporterpkg.Group{reporterpkg.Funcs{
		FinishFunc: func(_ context.Context, outcome reporterpkg.Outcome) error {
			got = outcome
			return nil
		},
	}}}
	reporters.Progress(engine.ProgressEvent{
		Kind: engine.WorkflowFinished, Depth: 0, Status: engine.StatusFailed, WorkflowName: "check",
		RunID: "check-run", ParentRunID: "parent-run", ParentStepRunID: "parent-step",
		Stats: engine.RunStats{RunID: "check-run", Total: 4, Failed: 1, Duration: 3 * time.Second},
	})
	if err := reporters.complete(t.Context(), "check", nil, errors.New("step failed"), false); err != nil {
		t.Fatal(err)
	}
	if got.Status != engine.StatusFailed || got.Stats.Total != 4 || got.Stats.Failed != 1 {
		t.Fatalf("outcome = %#v, want terminal event stats for the failed run", got)
	}
	if got.RunID != "check-run" || got.ParentRunID != "parent-run" || got.ParentStepRunID != "parent-step" {
		t.Fatalf("outcome identity = %#v, want terminal event identity", got)
	}
}

func TestRunReportersCompleteIgnoresTerminalEventFromAnotherWorkflow(t *testing.T) {
	var got reporterpkg.Outcome
	reporters := &runReporters{group: reporterpkg.Group{reporterpkg.Funcs{
		FinishFunc: func(_ context.Context, outcome reporterpkg.Outcome) error {
			got = outcome
			return nil
		},
	}}}
	reporters.Progress(engine.ProgressEvent{
		Kind: engine.WorkflowFinished, Depth: 0, Status: engine.StatusSucceeded, WorkflowName: "dependency",
		RunID: "dependency-run",
		Stats: engine.RunStats{RunID: "dependency-run", Total: 3, Succeeded: 3},
	})
	runErr := errors.New("workflow \"check\": validation failed")
	if err := reporters.complete(t.Context(), "check", nil, runErr, false); err != nil {
		t.Fatal(err)
	}
	if got.Status != engine.StatusFailed {
		t.Fatalf("status = %q, want the failure the run actually returned", got.Status)
	}
	if got.Stats.Total != 0 || got.Stats.Succeeded != 0 {
		t.Fatalf("stats = %#v, want no stats borrowed from the dependency run", got.Stats)
	}
}

func TestRunReportersCompleteIgnoresStaleSuccessBeforeLaterFailure(t *testing.T) {
	var got reporterpkg.Outcome
	reporters := &runReporters{group: reporterpkg.Group{reporterpkg.Funcs{
		FinishFunc: func(_ context.Context, outcome reporterpkg.Outcome) error {
			got = outcome
			return nil
		},
	}}}
	reporters.Progress(engine.ProgressEvent{
		Kind: engine.WorkflowFinished, Depth: 0, Status: engine.StatusSucceeded, WorkflowName: "check",
		RunID: "first-run",
		Stats: engine.RunStats{RunID: "first-run", Total: 3, Succeeded: 3},
	})
	if err := reporters.complete(t.Context(), "check", nil, errors.New("reload failed"), false); err != nil {
		t.Fatal(err)
	}
	if got.Status != engine.StatusFailed {
		t.Fatalf("status = %q, want the failure the run actually returned", got.Status)
	}
	if got.Stats.Succeeded != 0 {
		t.Fatalf("stats = %#v, want no stats borrowed from the earlier successful run", got.Stats)
	}
}

func TestRunReportersCompleteFallsBackToStateForDryRun(t *testing.T) {
	var got reporterpkg.Outcome
	reporters := &runReporters{group: reporterpkg.Group{reporterpkg.Funcs{
		FinishFunc: func(_ context.Context, outcome reporterpkg.Outcome) error {
			got = outcome
			return nil
		},
	}}}
	state := &engine.State{
		Stats:   engine.RunStats{InvocationID: "invocation", RunID: "dry-run", Total: 3, Succeeded: 3},
		Outputs: map[string]any{"artifact": "placeholder"},
	}
	if err := reporters.complete(t.Context(), "check", state, nil, true); err != nil {
		t.Fatal(err)
	}
	if got.Status != engine.StatusSucceeded || !got.DryRun || got.Stats.Total != 3 {
		t.Fatalf("outcome = %#v, want successful dry-run state", got)
	}
	if got.InvocationID == "" || got.RunID != "dry-run" {
		t.Fatalf("dry-run outcome identity = %#v", got)
	}
}

func TestRunReportersSequenceSpansRunsAndStreams(t *testing.T) {
	var progress []engine.ProgressEvent
	var diagnostics []diagnostic.Event
	reporters := &runReporters{group: reporterpkg.Group{reporterpkg.Funcs{
		ProgressFunc:   func(event engine.ProgressEvent) { progress = append(progress, event) },
		DiagnosticFunc: func(event diagnostic.Event) { diagnostics = append(diagnostics, event) },
	}}}
	invocationID := reporters.InvocationID()
	reporters.Diagnostic(diagnostic.Event{Message: "preparing"})
	registry := step.NewRegistry()
	if err := registry.Register("ok", func(map[string]any) (step.Runner, error) {
		return correlationTestRunner{}, nil
	}); err != nil {
		t.Fatal(err)
	}
	definition := &workflow.Definition{Version: 1, Name: "scheduled", Dir: t.TempDir(), Steps: []workflow.Step{{ID: "run", Type: "ok", With: map[string]any{}}}}
	options := engine.Options{InvocationID: invocationID, Progress: reporters.Progress, Diagnostics: reporters.Diagnostic}
	for range 2 {
		if _, err := engine.New(registry).Run(t.Context(), definition, options); err != nil {
			t.Fatal(err)
		}
	}
	var starts []engine.ProgressEvent
	for _, event := range progress {
		if event.Kind == engine.WorkflowStarted {
			starts = append(starts, event)
		}
	}
	if len(starts) != 2 || len(diagnostics) == 0 {
		t.Fatalf("events = %d starts, %d diagnostics", len(starts), len(diagnostics))
	}
	if diagnostics[0].InvocationID != invocationID || diagnostics[0].Sequence != 1 ||
		starts[0].InvocationID != invocationID || starts[1].InvocationID != invocationID ||
		starts[0].Sequence <= 1 || starts[1].Sequence <= starts[0].Sequence {
		t.Fatalf("correlated events = %#v / %#v", diagnostics, starts)
	}
	if starts[0].RunID == "" || starts[0].RunID == starts[1].RunID {
		t.Fatalf("repeated run IDs = %q, %q", starts[0].RunID, starts[1].RunID)
	}
}

type correlationTestRunner struct{}

func (correlationTestRunner) Run(context.Context, step.Request) (step.Result, error) {
	return step.Result{}, nil
}

func TestBrowserReporterProjectsCorrelation(t *testing.T) {
	var got webui.Progress
	reporter := browserReporter{stage: "workflow", emit: func(event webui.Progress) { got = event }}
	reporter.Progress(engine.ProgressEvent{
		InvocationID: "invocation", RunID: "run", ParentRunID: "parent",
		ParentStepRunID: "parent-step", StepRunID: "step", Sequence: 7,
	})
	if got.InvocationID != correlation.InvocationID("invocation") || got.RunID != "run" || got.ParentRunID != "parent" ||
		got.ParentStepRunID != "parent-step" || got.StepRunID != "step" || got.Sequence != 7 {
		t.Fatalf("browser progress = %#v", got)
	}
}

func TestOutcomeStatus(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want engine.ExecutionStatus
	}{
		{name: "success", want: engine.StatusSucceeded},
		{name: "failure", err: errors.New("broken"), want: engine.StatusFailed},
		{name: "canceled", err: fmtWrap(context.Canceled), want: engine.StatusCanceled},
		{name: "timed out", err: fmtWrap(context.DeadlineExceeded), want: engine.StatusTimedOut},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := outcomeStatus(test.err); got != test.want {
				t.Fatalf("outcomeStatus() = %q, want %q", got, test.want)
			}
		})
	}
}

func fmtWrap(err error) error {
	return errors.Join(errors.New("run failed"), err)
}

func TestNewRunReportersDefaultsToPlainAndComposesExplicitReporters(t *testing.T) {
	command := &cobra.Command{}
	command.SetOut(new(bytes.Buffer))
	command.SetErr(new(bytes.Buffer))
	debug := false
	deps := dependencies{debug: &debug, getenv: func(string) string { return "" }}

	defaults, err := newRunReporters(command, deps, t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(defaults.group) != 1 {
		t.Fatalf("default reporters = %d, want 1", len(defaults.group))
	}
	if _, ok := defaults.group[0].(*plainReporter); !ok {
		t.Fatalf("default reporter = %T, want *plainReporter", defaults.group[0])
	}

	output := filepath.Join(t.TempDir(), "output")
	summary := filepath.Join(t.TempDir(), "summary")
	for _, path := range []string{output, summary} {
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	values := map[string]string{"GITHUB_OUTPUT": output, "GITHUB_STEP_SUMMARY": summary}
	deps.getenv = func(name string) string { return values[name] }
	composed, err := newRunReporters(command, deps, t.TempDir(), []string{"plain", "github", "plain"})
	if err != nil {
		t.Fatal(err)
	}
	if len(composed.group) != 2 {
		t.Fatalf("explicit reporters = %d, want two unique reporters", len(composed.group))
	}
	if _, ok := composed.group[0].(*plainReporter); !ok {
		t.Fatalf("first reporter = %T, want *plainReporter", composed.group[0])
	}

	multiplexerOnly, err := newRunReporters(command, deps, t.TempDir(), []string{"multiplexer"})
	if err != nil {
		t.Fatal(err)
	}
	if len(multiplexerOnly.group) != 1 {
		t.Fatalf("multiplexer reporters = %d, want one", len(multiplexerOnly.group))
	}
}

func TestNewRunReportersRejectsUnknownReporter(t *testing.T) {
	command := &cobra.Command{}
	command.SetErr(new(bytes.Buffer))
	debug := false
	_, err := newRunReporters(command, dependencies{debug: &debug, getenv: func(string) string { return "" }}, t.TempDir(), []string{"otel"})
	if err == nil || !strings.Contains(err.Error(), `unknown reporter "otel"; expected plain or github or multiplexer`) {
		t.Fatalf("error = %v, want catalog-derived unknown reporter error", err)
	}
}

func TestRunReporterNamesFollowCatalogOrder(t *testing.T) {
	if got := strings.Join(runReporterNames(), ","); got != "plain,github,multiplexer" {
		t.Fatalf("reporter names = %q, want plain,github,multiplexer", got)
	}
}
