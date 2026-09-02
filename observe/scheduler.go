package observe

import (
	"context"
	"fmt"
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
	// FailurePace overrides how fast a source that fails instantly may be retried, and with it
	// how long such a source churns before its failure is treated as permanent. Zero means
	// defaultFailurePace.
	FailurePace time.Duration
}

const (
	// defaultFailurePace bounds how quickly a source that fails without doing any work is
	// retried. It is deliberately short: the pump is the only reader of a push source, so a
	// paused pump is a source draining nothing and losing events, not a source resting.
	defaultFailurePace = 50 * time.Millisecond
	// failureWindowPaces is how many paced retries a source may churn through, failing
	// instantly every time, before observation gives up on it. Any failure that took real
	// work, and any observation at all, starts the count over.
	failureWindowPaces = 200
)

type observation struct {
	event any
	err   error
	// fatal marks an error that ends observation even under workflow.ObserveContinue.
	fatal bool
}

type bodyResult struct {
	iteration int
	err       error
	started   time.Time
	duration  time.Duration
}

func (scheduler Scheduler) Run(ctx context.Context, runtime engine.BackgroundControlRuntime) (summary engine.BackgroundControlSummary, runErr error) {
	iterations := 0
	// Sources and their batches are a registry extension point, and the body is arbitrary
	// workflow steps, but the background supervisor runs jobs in bare goroutines. A panic that
	// escapes any of them would end the process instead of the step. Recovering is registered
	// first so it unwinds last, once the pump is stopped and joined and the body is released:
	// observation still ends, but it ends the way a source failure does.
	defer func() {
		if failure := engine.RecoveredPanic(recover()); failure != nil {
			summary = engine.BackgroundControlSummary{Iterations: iterations}
			runErr = failure
		}
	}()

	// The first observation is read before the pump exists. Source does not promise that
	// Initial and Next are safe to call at the same time, and starting the pump first would
	// call them concurrently on every run.
	initial := scheduler.Source.NewBatch()
	if event := scheduler.Source.Initial(); event != nil {
		initial.Add(event)
	}

	observations := make(chan observation, 1)
	pumpDone := make(chan struct{})
	// The pump reads the source until its own context ends. Cancelling that context on the way
	// out is what lets Run return while the pump is parked inside Next: without it, an exit the
	// source did not cause — a panic unwinding through Run, or a future early return — would
	// block forever on pumpDone instead of surfacing.
	pumpCtx, stopPump := context.WithCancel(ctx)
	go func() {
		defer close(pumpDone)
		// A panicking source ends observation, not the process. The failure takes the path a
		// fatal source error takes, so Run reports it and stops the body; it is never paced
		// and retried, because a source that panics is broken rather than merely failing.
		defer func() {
			failure := engine.RecoveredPanic(recover())
			if failure == nil {
				return
			}
			select {
			case observations <- observation{err: failure, fatal: true}:
			case <-pumpCtx.Done():
			}
		}()
		pace := scheduler.failurePace()
		churn := 0
		for {
			polled := time.Now()
			event, err := scheduler.Source.Next(pumpCtx)
			elapsed := time.Since(polled)
			fatal := err != nil && !scheduler.toleratesSourceErrors()
			switch {
			case err == nil || elapsed >= pace:
				// The source produced something, or spent real work failing. Either way it
				// is still doing its job, however badly, so nothing is churning.
				churn = 0
			case churn >= failureWindowPaces:
				err = fmt.Errorf("source failed %d times without doing any work: %w", churn, err)
				fatal = true
			default:
				churn++
			}
			select {
			case observations <- observation{event: event, err: err, fatal: fatal}:
			case <-pumpCtx.Done():
				return
			}
			if fatal {
				return
			}
			// Pace instant failures instead of pausing on them: retrying at once would spin,
			// but waiting is not free either, because a push source buffers nothing while the
			// pump is asleep.
			if err != nil && elapsed < pace && !sleep(pumpCtx, pace-elapsed) {
				return
			}
		}
	}()
	defer func() { <-pumpDone }()
	defer stopPump()

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
		// Binding already hands back a fresh map, and the engine deep-copies it again before
		// the body can reach it, so copying here only multiplies a payload that can be an
		// entire JSON response.
		//
		// It is also source code, so it can panic. Nothing that has to be paired with a
		// running body is published until the goroutine that owns it has started: an
		// unwinding startBody must not leave a cancel dangling, nor a result channel that
		// stopBody would then wait on forever.
		binding := batch.Binding()
		if binding == nil {
			binding = make(map[string]any)
		}
		binding["initial"] = initial
		binding["iteration"] = iteration
		binding["source"] = scheduler.SourceType
		started := time.Now()
		runtime.Report(engine.BackgroundControlEvent{Kind: engine.BackgroundIterationStarted, Iteration: iteration, StartedAt: started})
		bodyCtx, cancel := context.WithCancel(ctx)
		done := make(chan bodyResult, 1)
		go func() {
			// A panicking body fails its iteration the way a returned error does, so the
			// iteration is still reported and the change policy still decides what runs next.
			var err error
			defer func() {
				if failure := engine.RecoveredPanic(recover()); failure != nil {
					err = failure
				}
				done <- bodyResult{iteration: iteration, err: err, started: started, duration: time.Since(started)}
			}()
			err = runtime.RunIteration(bodyCtx, binding)
		}()
		cancelBody = cancel
		bodyDone = done
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
	// Every ordinary exit calls stopBody itself and leaves nothing for this to do. It is here
	// for the panic path, so an unwinding Run still cancels and reports its iteration instead
	// of leaving the body running until the supervisor gets around to cancelling the job.
	defer stopBody()

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
				if scheduler.toleratesSourceErrors() && !observed.fatal {
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

func (scheduler Scheduler) failurePace() time.Duration {
	if scheduler.FailurePace <= 0 {
		return defaultFailurePace
	}
	return scheduler.FailurePace
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
