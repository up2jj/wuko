package tui

import (
	"bytes"
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
