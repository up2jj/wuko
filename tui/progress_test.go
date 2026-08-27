package tui

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/up2jj/wuko/engine"
)

func TestProgressRendersLifecycleAndSummary(t *testing.T) {
	var output bytes.Buffer
	progress := NewProgress(&output, false)
	events := []engine.ProgressEvent{
		{Kind: engine.WorkflowStarted, WorkflowName: "release", Total: 2},
		{Kind: engine.StepStarted, StepID: "publish", StepType: "shell", Index: 1, Total: 2, MaxAttempts: 2, Timeout: 2 * time.Second},
		{Kind: engine.AttemptStarted, StepID: "publish", Attempt: 1, MaxAttempts: 2},
		{Kind: engine.AttemptFinished, Status: engine.StatusTimedOut, StepID: "publish", Attempt: 1, MaxAttempts: 2, Duration: 2 * time.Second, Error: errors.New("deadline exceeded")},
		{Kind: engine.RetryScheduled, StepID: "publish", Attempt: 2, MaxAttempts: 2, RetryDelay: 500 * time.Millisecond},
		{Kind: engine.AttemptStarted, StepID: "publish", Attempt: 2, MaxAttempts: 2},
		{Kind: engine.AttemptFinished, Status: engine.StatusSucceeded, StepID: "publish", Attempt: 2, MaxAttempts: 2, Duration: 100 * time.Millisecond},
		{Kind: engine.StepFinished, Status: engine.StatusSucceeded, StepID: "publish", Index: 1, Total: 2, Attempt: 2, Duration: 2600 * time.Millisecond},
		{Kind: engine.StepFinished, Status: engine.StatusSkipped, StepID: "deploy", Index: 2, Total: 2},
		{Kind: engine.WorkflowFinished, Status: engine.StatusSucceeded, WorkflowName: "release", Duration: 2600 * time.Millisecond, Stats: engine.RunStats{Succeeded: 1, Skipped: 1, Attempts: 2, Retries: 1, TimedOut: 1, RetryWait: 500 * time.Millisecond}},
	}
	for _, event := range events {
		progress.Report(event)
	}
	want := `◆ Workflow release · 2 steps
→ [1/2] publish (shell) · up to 2 attempts · timeout 2s
  • attempt 1/2 started
  ⏱ attempt 1/2 timed out after 2s: deadline exceeded
  ↻ retrying with attempt 2/2 in 500ms
  • attempt 2/2 started
✓ [1/2] publish succeeded after 2.6s · 2 attempts
⊘ [2/2] deploy skipped
✓ Workflow release succeeded in 2.6s · 1 succeeded · 1 skipped · 2 attempts · 1 retry · 1 timeout · 500ms retry wait
`
	if output.String() != want {
		t.Fatalf("output =\n%s\nwant =\n%s", output.String(), want)
	}
}

func TestProgressUsesColorOnlyWhenEnabled(t *testing.T) {
	var plain, colored bytes.Buffer
	event := engine.ProgressEvent{Kind: engine.WorkflowStarted, WorkflowName: "test", Total: 1}
	NewProgress(&plain, false).Report(event)
	NewProgress(&colored, true).Report(event)
	if strings.Contains(plain.String(), "\x1b[") || !strings.Contains(colored.String(), "\x1b[36m◆\x1b[0m") {
		t.Fatalf("plain = %q, colored = %q", plain.String(), colored.String())
	}
}

func TestProgressRendersFailureTimeoutAndCancellation(t *testing.T) {
	var output bytes.Buffer
	progress := NewProgress(&output, false)
	cases := []struct {
		id     string
		status engine.ExecutionStatus
	}{{"broken", engine.StatusFailed}, {"slow", engine.StatusTimedOut}, {"stopped", engine.StatusCanceled}}
	for index, test := range cases {
		progress.Report(engine.ProgressEvent{
			Kind: engine.StepFinished, Status: test.status, StepID: test.id,
			Index: index + 1, Total: 3, Duration: time.Second,
		})
	}
	text := output.String()
	for _, want := range []string{
		"✗ [1/3] broken failed after 1s",
		"⏱ [2/3] slow timed out after 1s",
		"■ [3/3] stopped canceled after 1s",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("output = %q, want %q", text, want)
		}
	}
}

func TestProgressRendersConcurrentGroup(t *testing.T) {
	var output bytes.Buffer
	progress := NewProgress(&output, false)
	progress.Report(engine.ProgressEvent{
		Kind: engine.ConcurrentStarted, GroupSize: 3, MaxConcurrency: 2,
		Timeout: 5 * time.Minute, FailFast: false,
	})
	progress.Report(engine.ProgressEvent{
		Kind: engine.ConcurrentFinished, Status: engine.StatusSucceeded, Duration: 2 * time.Second,
	})
	want := "⇉ Concurrent · 3 steps · max 2 concurrent · timeout 5m0s · wait for all\n✓ Concurrent succeeded after 2s\n"
	if output.String() != want {
		t.Fatalf("output = %q, want %q", output.String(), want)
	}
}

func TestProgressRendersCanceledWorkSummary(t *testing.T) {
	var output bytes.Buffer
	progress := NewProgress(&output, false)
	progress.Report(engine.ProgressEvent{
		Kind: engine.ConcurrentFinished, Status: engine.StatusCanceled, Duration: 2 * time.Second,
		GroupSize: 5, Started: 2, Succeeded: 1, Error: context.Canceled,
	})
	want := "■ Concurrent canceled after 2s · 2/5 steps started · 1 succeeded · 3 not run: context canceled\n"
	if output.String() != want {
		t.Fatalf("output = %q, want %q", output.String(), want)
	}
}

func TestProgressRendersControlLifecycle(t *testing.T) {
	var output bytes.Buffer
	progress := NewProgress(&output, false)
	progress.Report(engine.ProgressEvent{
		Kind: engine.ControlStarted, ControlKind: "matrix", StepID: "checks",
		Iterations: 4, MaxConcurrency: 2, FailFast: true,
	})
	progress.Report(engine.ProgressEvent{Kind: engine.IterationStarted, Iteration: 0, Iterations: 4, Depth: 1})
	progress.Report(engine.ProgressEvent{
		Kind: engine.ControlFinished, ControlKind: "matrix", StepID: "checks",
		Status: engine.StatusSucceeded, Duration: 2 * time.Second,
	})
	want := "↻ Matrix checks · 4 iterations · max 2 concurrent · fail fast\n  • iteration 1/4 started\n✓ Matrix checks succeeded after 2s\n"
	if output.String() != want {
		t.Fatalf("output = %q, want %q", output.String(), want)
	}
}

func TestProgressRendersCancelOnLifecycle(t *testing.T) {
	var output bytes.Buffer
	progress := NewProgress(&output, false)
	progress.Report(engine.ProgressEvent{
		Kind: engine.ControlStarted, ControlKind: "cancel_on", StepID: "deployment_watch",
		Iterations: 3, MaxConcurrency: 3,
	})
	progress.Report(engine.ProgressEvent{
		Kind: engine.ControlFinished, ControlKind: "cancel_on", StepID: "deployment_watch",
		Status: engine.StatusFailed, Duration: 2 * time.Second, Iterations: 3,
		Started: 3, Succeeded: 1, Error: errors.New("collection failed"),
	})
	want := "↻ Cancel on deployment_watch · 3 participants · max 3 concurrent · wait for all\n✗ Cancel on deployment_watch failed after 2s · 3/3 participants started · 1 succeeded: collection failed\n"
	if output.String() != want {
		t.Fatalf("output = %q, want %q", output.String(), want)
	}
}

func TestProgressRendersTryCatchLifecycle(t *testing.T) {
	var output bytes.Buffer
	progress := NewProgress(&output, false)
	progress.Report(engine.ProgressEvent{
		Kind: engine.ControlStarted, ControlKind: "try", StepID: "deployment", Iterations: 3, MaxConcurrency: 1,
	})
	progress.Report(engine.ProgressEvent{
		Kind: engine.ControlFinished, ControlKind: "try", StepID: "deployment", Status: engine.StatusFailed,
		Duration: 2 * time.Second, Iterations: 3, Started: 3, Succeeded: 1, Error: errors.New("rollback failed"),
	})
	want := "↻ Try deployment · 3 phases\n✗ Try deployment failed after 2s · 3/3 phases started · 1 succeeded: rollback failed\n"
	if output.String() != want {
		t.Fatalf("output = %q, want %q", output.String(), want)
	}
}

func TestProgressRendersPollLifecycleAndSummary(t *testing.T) {
	var output bytes.Buffer
	progress := NewProgress(&output, false)
	progress.Report(engine.ProgressEvent{Kind: engine.PollStarted, Poll: 1})
	progress.Report(engine.ProgressEvent{Kind: engine.PollScheduled, Poll: 2, PollDelay: 5 * time.Second, Error: errors.New("not ready")})
	progress.Report(engine.ProgressEvent{Kind: engine.PollStarted, Poll: 2})
	progress.Report(engine.ProgressEvent{Kind: engine.PollFinished, Poll: 2, Matched: true, Duration: 20 * time.Millisecond})
	progress.Report(engine.ProgressEvent{
		Kind: engine.WorkflowFinished, Status: engine.StatusSucceeded, WorkflowName: "poll", Duration: 5020 * time.Millisecond,
		Stats: engine.RunStats{Succeeded: 1, Attempts: 1, Polls: 2, PollWait: 5 * time.Second},
	})
	want := `  • poll 1 started
  ↻ poll 1 did not match · poll 2 in 5s: not ready
  • poll 2 started
  ✓ poll 2 matched after 20ms
✓ Workflow poll succeeded in 5.02s · 1 succeeded · 1 attempt · 2 polls · 5s poll wait
`
	if output.String() != want {
		t.Fatalf("output = %q, want %q", output.String(), want)
	}
}
