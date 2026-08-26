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
	"github.com/up2jj/wuko/engine"
	reporterpkg "github.com/up2jj/wuko/reporter"
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
		Kind: engine.WorkflowFinished, Depth: 0, Status: engine.StatusTimedOut,
		Stats: engine.RunStats{Total: 4, Failed: 1, Duration: 3 * time.Second},
	})
	state := &engine.State{
		Stats:   engine.RunStats{Total: 2, Succeeded: 2},
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
	if got.Stats.Total != 4 || got.Stats.Duration != 3*time.Second {
		t.Fatalf("stats = %#v, want terminal event stats", got.Stats)
	}
	if value := got.Outputs["nested"].(map[string]any)["value"]; value != "original" {
		t.Fatalf("cloned output = %q, want original", value)
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
		Stats:   engine.RunStats{Total: 3, Succeeded: 3},
		Outputs: map[string]any{"artifact": "placeholder"},
	}
	if err := reporters.complete(t.Context(), "check", state, nil, true); err != nil {
		t.Fatal(err)
	}
	if got.Status != engine.StatusSucceeded || !got.DryRun || got.Stats.Total != 3 {
		t.Fatalf("outcome = %#v, want successful dry-run state", got)
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
}

func TestNewRunReportersRejectsUnknownReporter(t *testing.T) {
	command := &cobra.Command{}
	command.SetErr(new(bytes.Buffer))
	debug := false
	_, err := newRunReporters(command, dependencies{debug: &debug, getenv: func(string) string { return "" }}, t.TempDir(), []string{"otel"})
	if err == nil || !strings.Contains(err.Error(), `unknown reporter "otel"; expected plain or github`) {
		t.Fatalf("error = %v, want catalog-derived unknown reporter error", err)
	}
}

func TestRunReporterNamesFollowCatalogOrder(t *testing.T) {
	if got := strings.Join(runReporterNames(), ","); got != "plain,github" {
		t.Fatalf("reporter names = %q, want plain,github", got)
	}
}
