package engine

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

type backgroundPhase uint8

const (
	backgroundAccepting backgroundPhase = iota
	backgroundSealed
	backgroundStopping
	backgroundJoined
)

var errBackgroundStopped = errors.New("background work stopped by workflow lifecycle")

type backgroundJob struct {
	id       string
	kind     string
	err      error
	finished bool
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
	if run == nil {
		return fmt.Errorf("background job %q requires a runner", id)
	}
	supervisor.mu.Lock()
	if supervisor.phase != backgroundAccepting {
		supervisor.mu.Unlock()
		return fmt.Errorf("background job %q cannot start after foreground execution finished", id)
	}
	if err := supervisor.ctx.Err(); err != nil {
		cause := context.Cause(supervisor.ctx)
		if cause == nil {
			cause = err
		}
		supervisor.mu.Unlock()
		return fmt.Errorf("background job %q cannot start after cancellation: %w", id, cause)
	}
	job := &backgroundJob{id: id, kind: kind}
	supervisor.jobs = append(supervisor.jobs, job)
	supervisor.wg.Add(1)
	supervisor.mu.Unlock()

	go func() {
		defer supervisor.wg.Done()
		err := run(supervisor.ctx)
		supervisor.mu.Lock()
		job.err = err
		job.finished = true
		supervisor.mu.Unlock()
		if err != nil && !cancellationOnly(err) {
			supervisor.stop(err)
		}
	}()
	return nil
}

func (supervisor *backgroundSupervisor) seal() {
	supervisor.mu.Lock()
	if supervisor.phase == backgroundAccepting {
		supervisor.phase = backgroundSealed
	}
	supervisor.mu.Unlock()
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
		if job.err != nil && !cancellationOnly(job.err) {
			errs = append(errs, fmt.Errorf("background %s %q: %w", job.kind, job.id, job.err))
		}
	}
	supervisor.mu.Unlock()

	cause := context.Cause(supervisor.ctx)
	if cause != nil && !errors.Is(cause, errBackgroundStopped) && !cancellationOnly(cause) {
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
