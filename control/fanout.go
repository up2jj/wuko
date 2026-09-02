package control

import (
	"context"

	"golang.org/x/sync/errgroup"
)

// FanOut runs count tasks with at most limit running concurrently, passing each
// task its index and the context it must honor. It returns once every admitted
// task has finished.
//
// When failFast is true the first task error cancels the context handed to the
// remaining tasks and stops further admission; otherwise task errors are ignored.
// Either way FanOut reports nothing: callers record per-task outcomes themselves.
//
// limit must be positive. errgroup.SetLimit(0) makes every Go call block forever,
// so callers validate their concurrency bound before reaching here -- see
// Policy.Validate.
//
// FanOut does not contain panics: a task that panics would end the process, and the
// error has nowhere to go because FanOut reports nothing. Tasks that can panic must
// recover for themselves and record it as their own error -- see RecoveredPanic.
//
// SetLimit may unblock one admission after cancellation, as an already-running
// task exits and frees its slot. Admission therefore does not imply the task ran:
// the context is checked before admission and again inside the task, and callers
// must check it once more before recording a task as started.
func FanOut(ctx context.Context, count, limit int, failFast bool, run func(context.Context, int) error) {
	if failFast {
		group, runCtx := errgroup.WithContext(ctx)
		group.SetLimit(limit)
		for index := range count {
			if runCtx.Err() != nil {
				break
			}
			group.Go(func() error {
				if runCtx.Err() != nil {
					return nil
				}
				return run(runCtx, index)
			})
		}
		_ = group.Wait()
		return
	}

	var group errgroup.Group
	group.SetLimit(limit)
	for index := range count {
		if ctx.Err() != nil {
			break
		}
		group.Go(func() error {
			if ctx.Err() != nil {
				return nil
			}
			_ = run(ctx, index)
			return nil
		})
	}
	_ = group.Wait()
}
