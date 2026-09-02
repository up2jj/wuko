package engine

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/up2jj/wuko/step"
)

type backgroundPhase uint8

const (
	backgroundAccepting backgroundPhase = iota
	backgroundSealed
	backgroundStopping
	backgroundJoined
)

var errBackgroundStopped = errors.New("background work stopped by workflow lifecycle")
var errBackgroundEnded = errors.New("background service ended its workflow scope")

type backgroundJob struct {
	id       string
	kind     string
	err      error
	finished bool
	cancel   context.CancelCauseFunc
	options  step.ServiceOptions
}

// backgroundSupervisor is the reusable run-level service supervisor. It accepts any
// ready background program; observation is only one client. Programs own their domain
// state and must stop all child goroutines and resources before returning.
type backgroundSupervisor struct {
	ctx    context.Context
	cancel context.CancelCauseFunc

	mu    sync.Mutex
	phase backgroundPhase
	jobs  []*backgroundJob
	wg    sync.WaitGroup
}

func newBackgroundSupervisor(parent context.Context) *backgroundSupervisor {
	ctx, cancel := context.WithCancelCause(parent)
	return &backgroundSupervisor{ctx: ctx, cancel: cancel}
}

func (supervisor *backgroundSupervisor) context() context.Context { return supervisor.ctx }

// count reports how many jobs are still running. Finished jobs stay in the slice so
// wait can collect their errors, so they are skipped here.
func (supervisor *backgroundSupervisor) count() int {
	supervisor.mu.Lock()
	defer supervisor.mu.Unlock()
	active := 0
	for _, job := range supervisor.jobs {
		if !job.finished {
			active++
		}
	}
	return active
}

func (supervisor *backgroundSupervisor) start(id, kind string, run func(context.Context) error) error {
	return supervisor.StartService(id, kind, step.ServiceOptions{KeepAlive: true, FailFast: true}, run)
}

func (supervisor *backgroundSupervisor) StartService(id, kind string, options step.ServiceOptions, run func(context.Context) error) error {
	if run == nil {
		return fmt.Errorf("background job %q requires a runner", id)
	}
	supervisor.mu.Lock()
	if supervisor.phase != backgroundAccepting {
		supervisor.mu.Unlock()
		return fmt.Errorf("background job %q cannot start after foreground execution finished: services cannot be started from a finally or defer step", id)
	}
	if err := supervisor.ctx.Err(); err != nil {
		cause := context.Cause(supervisor.ctx)
		if cause == nil {
			cause = err
		}
		supervisor.mu.Unlock()
		return fmt.Errorf("background job %q cannot start after cancellation: %w", id, cause)
	}
	jobCtx, jobCancel := context.WithCancelCause(supervisor.ctx)
	job := &backgroundJob{id: id, kind: kind, cancel: jobCancel, options: options}
	supervisor.jobs = append(supervisor.jobs, job)
	supervisor.wg.Add(1)
	supervisor.mu.Unlock()

	go func() {
		defer supervisor.wg.Done()
		err := runJob(jobCtx, run)
		supervisor.mu.Lock()
		job.err = err
		job.finished = true
		supervisor.mu.Unlock()
		switch {
		case errors.Is(err, step.ErrServiceAborted):
			// The owning step reports this failure itself, so the scope stays untouched:
			// a service that never started neither failed fast nor ended the scope.
		case err != nil && !cancellationOnly(err) && options.FailFast:
			supervisor.stop(err)
		case err == nil && options.ExitOnEnd && jobCtx.Err() == nil:
			// Only a service that ended on its own ends the scope. A job canceled by seal
			// or stop returned because the lifecycle shut it down, which is not an exit.
			supervisor.stop(errBackgroundEnded)
		}
	}()
	return nil
}

// runJob calls a background program and turns a panic into its error. Jobs run in their own
// goroutine, so a panic that escaped would end the process before the job was ever marked
// finished: the failure would never reach wait, fail-fast would never fire, and the run would
// die without a diagnosis. Recovering keeps a broken job a failed job, which the caller then
// reports and joins like any other failure.
func runJob(ctx context.Context, run func(context.Context) error) (err error) {
	defer func() {
		if failure := RecoveredPanic(recover()); failure != nil {
			err = failure
		}
	}()
	return run(ctx)
}

func (supervisor *backgroundSupervisor) seal() {
	supervisor.mu.Lock()
	if supervisor.phase == backgroundAccepting {
		supervisor.phase = backgroundSealed
	}
	jobs := append([]*backgroundJob(nil), supervisor.jobs...)
	supervisor.mu.Unlock()
	for _, job := range jobs {
		if !job.options.KeepAlive {
			job.cancel(errBackgroundStopped)
		}
	}
}

func (supervisor *backgroundSupervisor) stop(cause error) {
	supervisor.mu.Lock()
	if supervisor.phase < backgroundStopping {
		supervisor.phase = backgroundStopping
	}
	supervisor.mu.Unlock()
	if cause == nil {
		cause = errBackgroundStopped
	}
	supervisor.cancel(cause)
}

func (supervisor *backgroundSupervisor) wait() error {
	supervisor.wg.Wait()
	supervisor.mu.Lock()
	supervisor.phase = backgroundJoined
	errs := make([]error, 0, len(supervisor.jobs)+1)
	for _, job := range supervisor.jobs {
		if job.err != nil && !cancellationOnly(job.err) && !errors.Is(job.err, step.ErrServiceAborted) {
			errs = append(errs, fmt.Errorf("background %s %q: %w", job.kind, job.id, job.err))
		}
	}
	supervisor.mu.Unlock()

	cause := context.Cause(supervisor.ctx)
	if cause != nil && !errors.Is(cause, errBackgroundStopped) && !errors.Is(cause, errBackgroundEnded) && !cancellationOnly(cause) {
		found := false
		for _, err := range errs {
			if errors.Is(err, cause) {
				found = true
				break
			}
		}
		if !found {
			errs = append([]error{cause}, errs...)
		}
	}
	if cause != nil && cancellationOnly(cause) && len(errs) == 0 {
		return cause
	}
	return errors.Join(errs...)
}

func (supervisor *backgroundSupervisor) endedScope() bool {
	return errors.Is(context.Cause(supervisor.ctx), errBackgroundEnded)
}

type reportingServiceLauncher struct {
	launcher     step.ServiceLauncher
	options      Options
	workflowName string
}

func (launcher reportingServiceLauncher) StartService(id, kind string, serviceOptions step.ServiceOptions, run func(context.Context) error) error {
	return launcher.launcher.StartService(id, kind, serviceOptions, func(ctx context.Context) error {
		started := time.Now()
		report(launcher.options, ProgressEvent{Kind: BackgroundStarted, Status: StatusRunning, Time: started,
			WorkflowName: launcher.workflowName, Depth: launcher.options.depth, StepID: id, StepType: kind})
		err := run(ctx)
		finished := time.Now()
		report(launcher.options, ProgressEvent{Kind: BackgroundFinished, Status: statusFromError(err), Time: finished,
			WorkflowName: launcher.workflowName, Depth: launcher.options.depth, StepID: id, StepType: kind,
			Duration: finished.Sub(started), Error: nonCancellationError(err)})
		return err
	})
}
