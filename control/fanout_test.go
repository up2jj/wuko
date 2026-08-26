package control

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestFanOutBoundsConcurrencyAndCoversEveryIndex(t *testing.T) {
	t.Parallel()
	const count, limit = 12, 3

	var active, peak atomic.Int32
	var mu sync.Mutex
	seen := make([]bool, count)

	FanOut(t.Context(), count, limit, false, func(_ context.Context, index int) error {
		current := active.Add(1)
		defer active.Add(-1)
		for {
			previous := peak.Load()
			if current <= previous || peak.CompareAndSwap(previous, current) {
				break
			}
		}
		time.Sleep(time.Millisecond)
		mu.Lock()
		seen[index] = true
		mu.Unlock()
		return nil
	})

	for index, ran := range seen {
		if !ran {
			t.Errorf("index %d never ran", index)
		}
	}
	if got := peak.Load(); got > limit {
		t.Errorf("peak concurrency = %d, want <= %d", got, limit)
	}
}

func TestFanOutFailFastStopsAdmittingAfterAnError(t *testing.T) {
	t.Parallel()
	const count = 40

	var started atomic.Int32
	failure := errors.New("boom")

	FanOut(t.Context(), count, 2, true, func(ctx context.Context, index int) error {
		if ctx.Err() != nil {
			return nil
		}
		started.Add(1)
		if index == 0 {
			return failure
		}
		<-ctx.Done()
		return ctx.Err()
	})

	// The first task fails immediately; SetLimit can still release a slot or two as
	// running tasks exit, so this asserts the loop stopped rather than an exact count.
	if got := started.Load(); got >= count {
		t.Errorf("started = %d, want fewer than %d: fail-fast did not stop admission", got, count)
	}
}

func TestFanOutWithoutFailFastRunsEveryTaskDespiteErrors(t *testing.T) {
	t.Parallel()
	const count = 10
	var ran atomic.Int32

	FanOut(t.Context(), count, 4, false, func(context.Context, int) error {
		ran.Add(1)
		return errors.New("ignored")
	})

	if got := ran.Load(); got != count {
		t.Errorf("ran = %d, want %d: task errors must not stop a non-fail-fast fan-out", got, count)
	}
}

func TestFanOutDoesNotRunTasksAdmittedAfterCancellation(t *testing.T) {
	t.Parallel()
	const count = 40

	var executed atomic.Int32
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	FanOut(ctx, count, 2, false, func(taskCtx context.Context, index int) error {
		// A task that observes a live context here has genuinely been admitted to run.
		if taskCtx.Err() != nil {
			t.Errorf("index %d ran with an already-canceled context", index)
			return nil
		}
		executed.Add(1)
		if index == 0 {
			cancel()
		}
		return nil
	})

	if got := executed.Load(); got >= count {
		t.Errorf("executed = %d, want fewer than %d: cancellation did not stop admission", got, count)
	}
}

func TestFanOutReturnsImmediatelyForZeroTasks(t *testing.T) {
	t.Parallel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		FanOut(t.Context(), 0, 1, true, func(context.Context, int) error {
			t.Error("run called for a zero-task fan-out")
			return nil
		})
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("FanOut did not return for a zero-task fan-out")
	}
}
