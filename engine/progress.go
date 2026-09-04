package engine

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/up2jj/wuko/correlation"
	"github.com/up2jj/wuko/diagnostic"
	"github.com/up2jj/wuko/process"
)

// ProgressKind identifies one point in the execution lifecycle.
type ProgressKind string

const (
	WorkflowStarted     ProgressKind = "workflow_started"
	WorkflowFinished    ProgressKind = "workflow_finished"
	StepStarted         ProgressKind = "step_started"
	StepFinished        ProgressKind = "step_finished"
	AttemptStarted      ProgressKind = "attempt_started"
	AttemptFinished     ProgressKind = "attempt_finished"
	RetryScheduled      ProgressKind = "retry_scheduled"
	PollStarted         ProgressKind = "poll_started"
	PollFinished        ProgressKind = "poll_finished"
	PollScheduled       ProgressKind = "poll_scheduled"
	ConcurrentStarted   ProgressKind = "concurrent_started"
	ConcurrentFinished  ProgressKind = "concurrent_finished"
	ControlStarted      ProgressKind = "control_started"
	ControlFinished     ProgressKind = "control_finished"
	IterationStarted    ProgressKind = "iteration_started"
	IterationFinished   ProgressKind = "iteration_finished"
	BackgroundStarted   ProgressKind = "background_started"
	BackgroundJoining   ProgressKind = "background_joining"
	BackgroundFinished  ProgressKind = "background_finished"
	BackgroundTriggered ProgressKind = "background_triggered"
	// BackgroundSourceFailed reports a source failure a background control tolerated. The
	// control is still running; the failure is not the job's outcome.
	BackgroundSourceFailed ProgressKind = "background_source_failed"
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

// StepStats records the source location, terminal outcome, retries, and polling activity of one
// workflow step.
type StepStats struct {
	StepRunID  correlation.StepRunID
	ID         string
	Type       string
	Location   diagnostic.Location
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

// IterationStats records one control iteration or participant without retaining its binding values.
type IterationStats struct {
	Index     int
	Label     string
	Status    ExecutionStatus
	StartedAt time.Time
	Duration  time.Duration
	Error     error
	Steps     []StepStats
}

// RunStats summarizes one workflow execution. Composite actions have their own nested summary.
type RunStats struct {
	InvocationID    correlation.InvocationID
	RunID           correlation.RunID
	ParentRunID     correlation.RunID
	ParentStepRunID correlation.StepRunID
	StartedAt       time.Time
	FinishedAt      time.Time
	Duration        time.Duration
	Total           int
	Succeeded       int
	Failed          int
	Skipped         int
	Canceled        int
	TimedOut        int
	Attempts        int
	Retries         int
	RetryWait       time.Duration
	Polls           int
	PollWait        time.Duration
	Steps           []StepStats
}

// ProgressEvent is delivered synchronously and serialized as execution changes state. Depth is
// zero for the selected workflow and increases inside concurrent groups and composite actions.
type ProgressEvent struct {
	InvocationID    correlation.InvocationID
	RunID           correlation.RunID
	ParentRunID     correlation.RunID
	ParentStepRunID correlation.StepRunID
	StepRunID       correlation.StepRunID
	Sequence        correlation.Sequence
	Kind            ProgressKind
	Status          ExecutionStatus
	Time            time.Time
	WorkflowName    string
	Depth           int
	StepID          string
	StepType        string
	Index           int
	Total           int
	Attempt         int
	MaxAttempts     int
	GroupSize       int
	Started         int
	Succeeded       int
	ControlKind     string
	Action          string
	Iteration       int
	Iterations      int
	MaxConcurrency  int
	FailFast        bool
	Timeout         time.Duration
	Duration        time.Duration
	RetryDelay      time.Duration
	Poll            int
	PollDelay       time.Duration
	Matched         bool
	Polls           int
	PollWait        time.Duration
	Error           error
	Stats           RunStats
}

func report(options Options, event ProgressEvent) {
	if options.secretSession != nil {
		event.Error = options.secretSession.RedactError(event.Error)
		event.Stats = redactRunStats(event.Stats, options.secretSession.RedactError)
	}
	event.InvocationID = options.InvocationID
	event.RunID = options.runID
	event.ParentRunID = options.parentRunID
	event.ParentStepRunID = options.parentStepRunID
	event.StepRunID = options.stepRunID
	if options.Progress != nil {
		if options.runtime != nil {
			options.runtime.reportMu.Lock()
			defer options.runtime.reportMu.Unlock()
		}
		options.Progress(event)
		return
	}
	reportLegacy(options, event)
}

func redactRunStats(stats RunStats, redact func(error) error) RunStats {
	stats.Steps = append([]StepStats(nil), stats.Steps...)
	for i := range stats.Steps {
		stats.Steps[i] = redactStepStats(stats.Steps[i], redact)
	}
	return stats
}

func redactStepStats(stats StepStats, redact func(error) error) StepStats {
	stats.Error = redact(stats.Error)
	stats.Attempts = append([]AttemptStats(nil), stats.Attempts...)
	for i := range stats.Attempts {
		stats.Attempts[i].Error = redact(stats.Attempts[i].Error)
	}
	stats.Iterations = append([]IterationStats(nil), stats.Iterations...)
	for i := range stats.Iterations {
		stats.Iterations[i].Error = redact(stats.Iterations[i].Error)
		stats.Iterations[i].Steps = append([]StepStats(nil), stats.Iterations[i].Steps...)
		for j := range stats.Iterations[i].Steps {
			stats.Iterations[i].Steps[j] = redactStepStats(stats.Iterations[i].Steps[j], redact)
		}
	}
	return stats
}

// runRuntime holds the state one root run shares across concurrent branches.
// Its three locks are deliberately separate: a single lock covering both the
// output writers and the reporting callbacks would deadlock any callback that
// writes to Options.Stdout or Options.Stderr, because those writers take the
// very same lock and sync.Mutex is not reentrant.
type runRuntime struct {
	// writeMu serializes writes to the wrapped Stdout and Stderr so parallel
	// branches interleave at Write granularity rather than tearing.
	writeMu sync.Mutex
	// reportMu serializes the Progress and Diagnostics callbacks. One lock covers
	// both so progress and trace events stay mutually ordered.
	reportMu sync.Mutex
	// cleanups is the run-level managed-resource scope, released once the root run
	// finishes. Background control bodies scope their own iteration instead.
	cleanups   cleanupScope
	background *backgroundSupervisor
}

// cleanupScope owns the managed resources created on one execution path and releases
// them together. Branches append to it concurrently, so it carries its own lock.
type cleanupScope struct {
	mu       sync.Mutex
	cleanups []func(context.Context) error
}

func (scope *cleanupScope) register(cleanup func(context.Context) error) {
	scope.mu.Lock()
	defer scope.mu.Unlock()
	scope.cleanups = append(scope.cleanups, cleanup)
}

// adopt moves this scope's pending resources onto target, preserving registration order so the
// combined scope still releases everything in reverse completion order. A nested scope uses it
// when its resources must outlive it -- an attempt promotes its winning pass this way.
func (scope *cleanupScope) adopt(target *cleanupScope) {
	scope.mu.Lock()
	cleanups := scope.cleanups
	scope.cleanups = nil
	scope.mu.Unlock()
	if len(cleanups) == 0 || target == nil {
		return
	}
	target.mu.Lock()
	defer target.mu.Unlock()
	target.cleanups = append(target.cleanups, cleanups...)
}

// run releases the scope's resources in reverse completion order. ctx should be
// detached from the run's cancellation so managed resources are still released after
// Ctrl-C; see step.Cleaner for why it carries no overall deadline.
func (scope *cleanupScope) run(ctx context.Context) []error {
	scope.mu.Lock()
	cleanups := scope.cleanups
	scope.cleanups = nil
	scope.mu.Unlock()

	var cleanupErrors []error
	for index := len(cleanups) - 1; index >= 0; index-- {
		if err := cleanups[index](ctx); err != nil {
			cleanupErrors = append(cleanupErrors, err)
		}
	}
	return cleanupErrors
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

// terminalFile is the capability terminal UI libraries use to recognize an
// interactive output and query its dimensions. Keep it local so the engine
// does not need to depend on a particular terminal package.
type terminalFile interface {
	io.ReadWriteCloser
	Fd() uintptr
}

type synchronizedTerminalWriter struct {
	terminalFile
	mu *sync.Mutex
}

func (writer synchronizedTerminalWriter) Write(data []byte) (int, error) {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	return writer.terminalFile.Write(data)
}

func synchronizeWriter(mu *sync.Mutex, writer io.Writer) io.Writer {
	writer = writerOrDiscard(writer)
	if terminal, ok := writer.(terminalFile); ok {
		return synchronizedTerminalWriter{terminalFile: terminal, mu: mu}
	}
	return synchronizedWriter{mu: mu, writer: writer}
}

func prepareRunOptions(options Options) Options {
	if options.Executor == nil {
		options.Executor = process.LocalExecutor{}
	}
	if options.runtime != nil {
		return options
	}
	runtime := &runRuntime{}
	options.runtime = runtime
	options.Stdout = synchronizeWriter(&runtime.writeMu, options.Stdout)
	options.Stderr = synchronizeWriter(&runtime.writeMu, options.Stderr)
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
	case BackgroundStarted:
		fmt.Fprintf(writerOrDiscard(options.Stdout), "%s (%s) started in background\n", event.StepID, event.StepType)
	case BackgroundJoining:
		fmt.Fprintf(writerOrDiscard(options.Stdout), "waiting for %d background job(s)\n", event.Started)
	case BackgroundFinished:
		if event.Error != nil {
			fmt.Fprintf(writerOrDiscard(options.Stderr), "%s (%s) background stopped: %v\n", event.StepID, event.StepType, event.Error)
		}
	case BackgroundTriggered:
		fmt.Fprintf(writerOrDiscard(options.Stdout), "%s (%s) trigger %s\n", event.StepID, event.StepType, event.Action)
	case BackgroundSourceFailed:
		fmt.Fprintf(writerOrDiscard(options.Stderr), "%s (%s) source failed, still observing: %v\n", event.StepID, event.StepType, event.Error)
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
