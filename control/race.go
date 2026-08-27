package control

import (
	"context"
	"sync"
)

// Race runs every participant concurrently. The first participant returning true wins,
// cancels the others, and is returned after every participant has stopped. A negative
// winner means no participant was eligible. Parent cancellation always takes precedence.
func Race(ctx context.Context, count int, run func(context.Context, int) bool) (winner int, parentErr error) {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	type completion struct {
		index    int
		eligible bool
	}
	completed := make(chan completion, count)
	var group sync.WaitGroup
	for index := range count {
		group.Go(func() {
			completed <- completion{index: index, eligible: run(runCtx, index)}
		})
	}

	winner = -1
	for range count {
		item := <-completed
		if winner < 0 && item.eligible {
			winner = item.index
			cancel()
		}
	}
	group.Wait()
	if err := ctx.Err(); err != nil {
		return -1, err
	}
	return winner, nil
}
