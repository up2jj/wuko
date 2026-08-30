package observe

import (
	"context"
	"time"

	"github.com/up2jj/wuko/engine"
	"github.com/up2jj/wuko/workflow"
)

type Scheduler struct {
	Source     Source
	SourceType string
	Debounce   time.Duration
	OnChange   string
	// OnError decides whether a source failure ends observation. Zero means workflow.ObserveFail.
	OnError string
	// RetryBase is the first delay after a tolerated source failure, doubling up to
	// maxSourceRetryDelay. Zero means defaultSourceRetryBase.
	RetryBase time.Duration
}

const (
	defaultSourceRetryBase = 250 * time.Millisecond
	maxSourceRetryDelay    = 30 * time.Second
)

type observation struct {
	event any
	err   error
}

type bodyResult struct {
	iteration int
	err       error
	started   time.Time
	duration  time.Duration
}

func (scheduler Scheduler) Run(ctx context.Context, runtime engine.BackgroundControlRuntime) (engine.BackgroundControlSummary, error) {
	observations := make(chan observation, 1)
	pumpDone := make(chan struct{})
	go func() {
		defer close(pumpDone)
		failures := 0
		for {
			event, err := scheduler.Source.Next(ctx)
			select {
			case observations <- observation{event: event, err: err}:
			case <-ctx.Done():
				return
			}
			if err == nil {
				failures = 0
				continue
			}
			if !scheduler.toleratesSourceErrors() {
				return
			}
			// A source that fails immediately would otherwise spin. Back off between
			// retries and start over once it produces an observation again.
			failures++
			if !sleep(ctx, scheduler.retryDelay(failures)) {
				return
			}
		}
	}()
	defer func() { <-pumpDone }()

	iterations := 0
	var bodyDone <-chan bodyResult
	var cancelBody context.CancelFunc
	pending := scheduler.Source.NewBatch()
	queued := scheduler.Source.NewBatch()
	var timer *time.Timer
	var timerC <-chan time.Time
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()

	startBody := func(initial bool, batch Batch) {
		iterations++
		iteration := iterations
		bodyCtx, cancel := context.WithCancel(ctx)
		cancelBody = cancel
		done := make(chan bodyResult, 1)
		bodyDone = done
		binding := cloneMap(batch.Binding())
		if binding == nil {
			binding = make(map[string]any)
		}
		binding["initial"] = initial
		binding["iteration"] = iteration
		binding["source"] = scheduler.SourceType
		started := time.Now()
		runtime.Report(engine.BackgroundControlEvent{Kind: engine.BackgroundIterationStarted, Iteration: iteration, StartedAt: started})
		go func() {
			err := runtime.RunIteration(bodyCtx, binding)
			done <- bodyResult{iteration: iteration, err: err, started: started, duration: time.Since(started)}
		}()
	}

	finishIteration := func(result bodyResult) {
		runtime.Report(engine.BackgroundControlEvent{Kind: engine.BackgroundIterationFinished, Iteration: result.iteration, StartedAt: result.started, Duration: result.duration, Error: result.err})
	}
	// stopBody ends the active iteration: it releases the body context so the child
	// never outlives the iteration, and it reports the outcome so a body failure that
	// lands as shutdown begins is still surfaced rather than silently dropped.
	stopBody := func() {
		if cancelBody != nil {
			cancelBody()
			cancelBody = nil
		}
		if bodyDone != nil {
			finishIteration(<-bodyDone)
			bodyDone = nil
		}
	}

	initial := scheduler.Source.NewBatch()
	if event := scheduler.Source.Initial(); event != nil {
		initial.Add(event)
	}
	startBody(true, initial)
	for {
		select {
		case <-ctx.Done():
			stopBody()
			return engine.BackgroundControlSummary{Iterations: iterations}, ctx.Err()
		case observed := <-observations:
			if observed.err != nil {
				if ctx.Err() != nil {
					continue
				}
				if scheduler.toleratesSourceErrors() {
					runtime.Report(engine.BackgroundControlEvent{Kind: engine.BackgroundSourceFailure, Action: workflow.ObserveContinue, Error: observed.err})
					continue
				}
				stopBody()
				return engine.BackgroundControlSummary{Iterations: iterations}, observed.err
			}
			if scheduler.OnChange == workflow.ObserveSkip && bodyDone != nil {
				runtime.Report(engine.BackgroundControlEvent{Kind: engine.BackgroundTriggerHandled, Action: workflow.ObserveSkip})
				continue
			}
			pending.Add(observed.event)
			if scheduler.Debounce == 0 {
				timerC = readyTimerChannel()
				continue
			}
			if timer == nil {
				timer = time.NewTimer(scheduler.Debounce)
			} else {
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				timer.Reset(scheduler.Debounce)
			}
			timerC = timer.C
		case <-timerC:
			timerC = nil
			batch := pending
			pending = scheduler.Source.NewBatch()
			if bodyDone == nil {
				queued.Merge(batch)
				batch = queued
				queued = scheduler.Source.NewBatch()
				startBody(false, batch)
				continue
			}
			queued.Merge(batch)
			if scheduler.OnChange == workflow.ObserveRestart && cancelBody != nil {
				runtime.Report(engine.BackgroundControlEvent{Kind: engine.BackgroundTriggerHandled, Action: workflow.ObserveRestart})
				cancelBody()
			} else {
				runtime.Report(engine.BackgroundControlEvent{Kind: engine.BackgroundTriggerHandled, Action: workflow.ObserveQueue})
			}
		case result := <-bodyDone:
			// Release the body context on the completion path too: it is a child of the
			// long-lived job context, so an uncancelled one stays registered with the
			// parent for the rest of the run.
			if cancelBody != nil {
				cancelBody()
				cancelBody = nil
			}
			bodyDone = nil
			finishIteration(result)
			if !queued.Empty() && timerC == nil {
				batch := queued
				queued = scheduler.Source.NewBatch()
				startBody(false, batch)
			}
		}
	}
}

func (scheduler Scheduler) toleratesSourceErrors() bool {
	return scheduler.OnError == workflow.ObserveContinue
}

func (scheduler Scheduler) retryDelay(failures int) time.Duration {
	base := scheduler.RetryBase
	if base <= 0 {
		base = defaultSourceRetryBase
	}
	delay := base
	for attempt := 1; attempt < failures && delay < maxSourceRetryDelay; attempt++ {
		delay *= 2
	}
	return min(delay, maxSourceRetryDelay)
}

// sleep reports whether the delay elapsed rather than the context ending.
func sleep(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}

func readyTimerChannel() <-chan time.Time {
	channel := make(chan time.Time, 1)
	channel <- time.Now()
	return channel
}
