package observe

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/up2jj/wuko/engine"
	"github.com/up2jj/wuko/workflow"
)

// panicSource panics from whichever entry point the test names. Everything else behaves like a
// source that simply never produces.
type panicSource struct {
	at      string
	blocked chan struct{}
}

func (source *panicSource) Initial() any {
	if source.at == "initial" {
		panic("initial exploded")
	}
	return nil
}

func (source *panicSource) Next(ctx context.Context) (any, error) {
	if source.at == "next" {
		panic("next exploded")
	}
	if source.at == "add" || source.at == "binding" {
		// One observation, then park: the batch panics while handling it.
		select {
		case <-source.blocked:
			<-ctx.Done()
			return nil, ctx.Err()
		default:
			close(source.blocked)
			return map[string]any{"value": "observed"}, nil
		}
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

func (source *panicSource) NewBatch() Batch   { return &panicBatch{at: source.at} }
func (*panicSource) Metadata() map[string]any { return map[string]any{} }
func (*panicSource) Close() error             { return nil }

type panicBatch struct {
	at    string
	value any
}

func (batch *panicBatch) Add(value any) {
	if batch.at == "add" {
		panic("add exploded")
	}
	batch.value = value
}

func (batch *panicBatch) Merge(Batch) {}
func (batch *panicBatch) Empty() bool { return batch.value == nil }

func (batch *panicBatch) Binding() map[string]any {
	if batch.at == "binding" {
		panic("binding exploded")
	}
	return map[string]any{"test": batch.value}
}

func runPanicScheduler(t *testing.T, at string, body func(context.Context, map[string]any) error) (engine.BackgroundControlSummary, error) {
	t.Helper()
	scheduler := Scheduler{
		Source:     &panicSource{at: at, blocked: make(chan struct{})},
		SourceType: "test",
		OnChange:   workflow.ObserveQueue,
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	type outcome struct {
		summary engine.BackgroundControlSummary
		err     error
	}
	done := make(chan outcome, 1)
	go func() {
		summary, err := scheduler.Run(ctx, engine.BackgroundControlRuntime{
			RunIteration: body,
			Report:       func(engine.BackgroundControlEvent) {},
		})
		done <- outcome{summary, err}
	}()
	select {
	case got := <-done:
		return got.summary, got.err
	case <-ctx.Done():
		t.Fatal("Run did not return after the panic")
		return engine.BackgroundControlSummary{}, nil
	}
}

// A panic anywhere in source, batch, or scheduler code must end observation as a step failure.
// The supervisor runs jobs in bare goroutines, so an escaping panic would kill the process.
func TestPanicInSourceOrBatchFailsObservationInsteadOfCrashing(t *testing.T) {
	idle := func(ctx context.Context, _ map[string]any) error { <-ctx.Done(); return ctx.Err() }
	for _, testCase := range []struct{ at, want string }{
		{"initial", "initial exploded"},
		{"next", "next exploded"},
		{"add", "add exploded"},
		{"binding", "binding exploded"},
	} {
		t.Run(testCase.at, func(t *testing.T) {
			_, err := runPanicScheduler(t, testCase.at, idle)
			if err == nil {
				t.Fatal("Run returned no error")
			}
			if !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("error = %v, want it to mention %q", err, testCase.want)
			}
			// The stack is what locates the bug in the source being observed.
			if !strings.Contains(err.Error(), "observe.") {
				t.Fatalf("error carries no stack: %v", err)
			}
		})
	}
}

// A panicking body fails its own iteration and is reported like any other iteration error,
// rather than ending the process or silently vanishing.
func TestPanicInBodyIsReportedAsIterationFailure(t *testing.T) {
	var iterations atomic.Int32
	failures := make(chan error, 1)
	scheduler := Scheduler{Source: &panicSource{at: "none", blocked: make(chan struct{})}, SourceType: "test"}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := scheduler.Run(ctx, engine.BackgroundControlRuntime{
			RunIteration: func(context.Context, map[string]any) error {
				iterations.Add(1)
				panic("body exploded")
			},
			Report: func(event engine.BackgroundControlEvent) {
				if event.Kind == engine.BackgroundIterationFinished && event.Error != nil {
					select {
					case failures <- event.Error:
					default:
					}
				}
			},
		})
		done <- err
	}()

	select {
	case err := <-failures:
		if !strings.Contains(err.Error(), "body exploded") {
			t.Fatalf("iteration error = %v", err)
		}
	case <-ctx.Done():
		t.Fatal("no iteration failure was reported")
	}
	// Observation is still alive and owns the panic; cancelling is what ends it.
	cancel()
	if err := <-done; err == nil {
		t.Fatal("Run returned no error after cancellation")
	}
	if got := iterations.Load(); got != 1 {
		t.Fatalf("iterations = %d, want 1", got)
	}
}
