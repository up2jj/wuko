package control

import (
	"context"
	"sync"
)

// Race runs every participant concurrently. The first participant returning true wins,
// cancels the others, and is returned after every participant has stopped. A negative
// winner means no participant was eligible. Parent cancellation always takes precedence,
// and a participant that panicked is reported as the error with no winner.
func Race(ctx context.Context, count int, run func(context.Context, int) bool) (winner int, parentErr error) {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	type completion struct {
		index    int
		eligible bool
		failure  error
	}
	completed := make(chan completion, count)
	var group sync.WaitGroup
	for index := range count {
		group.Go(func() {
			item := completion{index: index}
			// Participants run in goroutines of their own, so a panic that escaped would end
			// the process before any participant was joined: the losers would keep running,
			// the winner's reporting would never happen, and the run would die without naming
			// the participant that broke. Recovering keeps a broken participant a failed race.
			defer func() {
				if failure := RecoveredPanic(recover()); failure != nil {
					completed <- completion{index: index, failure: failure}
				}
			}()
			item.eligible = run(runCtx, index)
			completed <- item
		})
	}

	winner = -1
	var failure error
	for range count {
		item := <-completed
		switch {
		case item.failure != nil:
			if failure == nil {
				failure = item.failure
			}
			cancel()
		case winner < 0 && item.eligible:
			winner = item.index
			cancel()
		}
	}
	group.Wait()
	if err := ctx.Err(); err != nil {
		return -1, err
	}
	if failure != nil {
		return -1, failure
	}
	return winner, nil
}
