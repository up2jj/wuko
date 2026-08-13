package engine

import (
	"fmt"
	"io"
	"time"
)

// ProgressKind identifies one point in the execution lifecycle.
type ProgressKind string

const (
	WorkflowStarted  ProgressKind = "workflow_started"
	WorkflowFinished ProgressKind = "workflow_finished"
	StepStarted      ProgressKind = "step_started"
	StepFinished     ProgressKind = "step_finished"
	AttemptStarted   ProgressKind = "attempt_started"
	AttemptFinished  ProgressKind = "attempt_finished"
	RetryScheduled   ProgressKind = "retry_scheduled"
)

// ExecutionStatus is the terminal state of a workflow, step, or attempt.
type ExecutionStatus string

const (
	StatusRunning   ExecutionStatus = "running"
	StatusSucceeded ExecutionStatus = "succeeded"
	StatusFailed    ExecutionStatus = "failed"
	StatusTimedOut  ExecutionStatus = "timed_out"
	StatusCanceled  ExecutionStatus = "canceled"
	StatusSkipped   ExecutionStatus = "skipped"
)

// AttemptStats records the outcome and wall-clock duration of one step attempt.
type AttemptStats struct {
	Number    int
	Status    ExecutionStatus
	StartedAt time.Time
	Duration  time.Duration
	Error     error
}

// StepStats records the terminal outcome and retry activity of one workflow step.
type StepStats struct {
	ID        string
	Type      string
	Index     int
	Status    ExecutionStatus
	StartedAt time.Time
	Duration  time.Duration
	RetryWait time.Duration
	Attempts  []AttemptStats
	Error     error
}

// RunStats summarizes one workflow execution. Composite actions have their own nested summary.
type RunStats struct {
	StartedAt  time.Time
	FinishedAt time.Time
	Duration   time.Duration
	Total      int
	Succeeded  int
	Failed     int
	Skipped    int
	Canceled   int
	TimedOut   int
	Attempts   int
	Retries    int
	RetryWait  time.Duration
	Steps      []StepStats
}

// ProgressEvent is emitted synchronously as execution changes state. Depth is zero for the
// selected workflow and increases for steps inside composite actions.
type ProgressEvent struct {
	Kind         ProgressKind
	Status       ExecutionStatus
	Time         time.Time
	WorkflowName string
	Depth        int
	StepID       string
	StepType     string
	Index        int
	Total        int
	Attempt      int
	MaxAttempts  int
	Timeout      time.Duration
	Duration     time.Duration
	RetryDelay   time.Duration
	Error        error
	Stats        RunStats
}

func report(options Options, event ProgressEvent) {
	if options.Progress != nil {
		options.Progress(event)
		return
	}
	reportLegacy(options, event)
}

func reportLegacy(options Options, event ProgressEvent) {
	switch event.Kind {
	case StepStarted:
		fmt.Fprintf(writerOrDiscard(options.Stdout), "[%d/%d] %s (%s)\n", event.Index, event.Total, event.StepID, event.StepType)
	case StepFinished:
		if event.Status == StatusSkipped {
			fmt.Fprintf(writerOrDiscard(options.Stdout), "[%d/%d] %s (%s) skipped\n", event.Index, event.Total, event.StepID, event.StepType)
		}
	case AttemptFinished:
		if event.Status != StatusSucceeded && event.Attempt < event.MaxAttempts {
			fmt.Fprintf(writerOrDiscard(options.Stderr), "%s: attempt %d/%d failed: %v\n", event.StepID, event.Attempt, event.MaxAttempts, event.Error)
		}
	case RetryScheduled:
		fmt.Fprintf(writerOrDiscard(options.Stderr), "%s: retrying in %s\n", event.StepID, event.RetryDelay)
	}
}

func writerOrDiscard(writer io.Writer) io.Writer {
	if writer == nil {
		return io.Discard
	}
	return writer
}
