package engine

import (
	"fmt"
	"io"
	"sync"
	"time"
)

// ProgressKind identifies one point in the execution lifecycle.
type ProgressKind string

const (
	WorkflowStarted    ProgressKind = "workflow_started"
	WorkflowFinished   ProgressKind = "workflow_finished"
	StepStarted        ProgressKind = "step_started"
	StepFinished       ProgressKind = "step_finished"
	AttemptStarted     ProgressKind = "attempt_started"
	AttemptFinished    ProgressKind = "attempt_finished"
	RetryScheduled     ProgressKind = "retry_scheduled"
	PollStarted        ProgressKind = "poll_started"
	PollFinished       ProgressKind = "poll_finished"
	PollScheduled      ProgressKind = "poll_scheduled"
	ConcurrentStarted  ProgressKind = "concurrent_started"
	ConcurrentFinished ProgressKind = "concurrent_finished"
	ControlStarted     ProgressKind = "control_started"
	ControlFinished    ProgressKind = "control_finished"
	IterationStarted   ProgressKind = "iteration_started"
	IterationFinished  ProgressKind = "iteration_finished"
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

// StepStats records the terminal outcome, retries, and polling activity of one workflow step.
type StepStats struct {
	ID         string
	Type       string
	Index      int
	Status     ExecutionStatus
	StartedAt  time.Time
	Duration   time.Duration
	RetryWait  time.Duration
	Polls      int
	PollWait   time.Duration
	Attempts   []AttemptStats
	Error      error
	Iterations []IterationStats
}

// IterationStats records one foreach or matrix iteration without retaining its binding values.
type IterationStats struct {
	Index     int
	Status    ExecutionStatus
	StartedAt time.Time
	Duration  time.Duration
	Error     error
	Steps     []StepStats
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
	Polls      int
	PollWait   time.Duration
	Steps      []StepStats
}

// ProgressEvent is delivered synchronously and serialized as execution changes state. Depth is
// zero for the selected workflow and increases inside concurrent groups and composite actions.
type ProgressEvent struct {
	Kind           ProgressKind
	Status         ExecutionStatus
	Time           time.Time
	WorkflowName   string
	Depth          int
	StepID         string
	StepType       string
	Index          int
	Total          int
	Attempt        int
	MaxAttempts    int
	GroupSize      int
	Started        int
	Succeeded      int
	ControlKind    string
	Iteration      int
	Iterations     int
	MaxConcurrency int
	FailFast       bool
	Timeout        time.Duration
	Duration       time.Duration
	RetryDelay     time.Duration
	Poll           int
	PollDelay      time.Duration
	Matched        bool
	Polls          int
	PollWait       time.Duration
	Error          error
	Stats          RunStats
}

func report(options Options, event ProgressEvent) {
	if options.Progress != nil {
		if options.runtime != nil {
			options.runtime.mu.Lock()
			defer options.runtime.mu.Unlock()
		}
		options.Progress(event)
		return
	}
	reportLegacy(options, event)
}

type runRuntime struct {
	mu sync.Mutex
}

type synchronizedWriter struct {
	mu     *sync.Mutex
	writer io.Writer
}

func (writer synchronizedWriter) Write(data []byte) (int, error) {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	return writer.writer.Write(data)
}

func prepareRunOptions(options Options) Options {
	if options.runtime != nil {
		return options
	}
	runtime := &runRuntime{}
	options.runtime = runtime
	options.Stdout = synchronizedWriter{mu: &runtime.mu, writer: writerOrDiscard(options.Stdout)}
	options.Stderr = synchronizedWriter{mu: &runtime.mu, writer: writerOrDiscard(options.Stderr)}
	return options
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
	case PollScheduled:
		fmt.Fprintf(writerOrDiscard(options.Stderr), "%s: poll %d did not match; polling again in %s\n", event.StepID, event.Poll-1, event.PollDelay)
	}
}

func writerOrDiscard(writer io.Writer) io.Writer {
	if writer == nil {
		return io.Discard
	}
	return writer
}
