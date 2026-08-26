package engine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/workflow"
)

func concurrentDefinition(t *testing.T, group *workflow.ConcurrentGroup) *workflow.Definition {
	t.Helper()
	return testDefinition(t, "concurrent", workflow.Step{Concurrent: group})
}

func TestRunConcurrentGroupBoundsExecutionAndCommitsInDeclarationOrder(t *testing.T) {
	started := make(chan string, 3)
	release := make(chan struct{}, 3)
	var active atomic.Int32
	var maximum atomic.Int32
	registry := newTestRegistry(t, map[string]step.Builder{"gate": func(map[string]any) (step.Runner, error) {
		return runnerFunc(func(ctx context.Context, request step.Request) (step.Result, error) {
			current := active.Add(1)
			defer active.Add(-1)
			for {
				previous := maximum.Load()
				if current <= previous || maximum.CompareAndSwap(previous, current) {
					break
				}
			}
			started <- request.StepID
			select {
			case <-release:
				return step.Result{Outputs: map[string]any{"id": request.StepID}, Variables: map[string]any{"var_" + request.StepID: request.StepID}}, nil
			case <-ctx.Done():
				return step.Result{}, ctx.Err()
			}
		}), nil
	}})
	steps := []workflow.Step{
		{ID: "first", Type: "gate", With: map[string]any{}},
		{ID: "second", Type: "gate", With: map[string]any{}},
		{ID: "third", Type: "gate", With: map[string]any{}},
	}
	definition := concurrentDefinition(t, &workflow.ConcurrentGroup{Steps: steps, MaxConcurrency: 2, FailFast: true})
	type runResult struct {
		state *State
		err   error
	}
	done := make(chan runResult, 1)
	go func() {
		state, err := New(registry).Run(t.Context(), definition, Options{})
		done <- runResult{state: state, err: err}
	}()
	<-started
	<-started
	select {
	case id := <-started:
		t.Fatalf("third child %q started before a concurrency slot was released", id)
	default:
	}
	release <- struct{}{}
	third := <-started
	if third == "" {
		t.Fatal("third child did not start")
	}
	release <- struct{}{}
	release <- struct{}{}
	result := <-done
	if result.err != nil {
		t.Fatal(result.err)
	}
	if maximum.Load() != 2 {
		t.Fatalf("maximum active = %d, want 2", maximum.Load())
	}
	if len(result.state.Steps) != 3 || result.state.Vars["var_third"] != "third" {
		t.Fatalf("state = %#v", result.state)
	}
	for i, id := range []string{"first", "second", "third"} {
		if result.state.Stats.Steps[i].ID != id {
			t.Fatalf("stats order = %#v", result.state.Stats.Steps)
		}
	}
}

func TestRunConcurrentGroupRespectsRetriesPerChild(t *testing.T) {
	var mu sync.Mutex
	attempts := make(map[string]int)
	registry := newTestRegistry(t, map[string]step.Builder{"retry_child": func(map[string]any) (step.Runner, error) {
		return runnerFunc(func(_ context.Context, request step.Request) (step.Result, error) {
			mu.Lock()
			attempts[request.StepID]++
			mu.Unlock()
			if request.StepID == "flaky" && request.Attempt == 1 {
				return step.Result{}, errors.New("temporary")
			}
			return step.Result{Outputs: map[string]any{"attempt": request.Attempt}}, nil
		}), nil
	}})
	policy := &workflow.RetryPolicy{MaxAttempts: 2, BackoffMultiplier: 1}
	steps := []workflow.Step{
		{ID: "stable", Type: "retry_child", Retry: policy, With: map[string]any{}},
		{ID: "flaky", Type: "retry_child", Retry: policy, With: map[string]any{}},
	}
	state, err := New(registry).Run(t.Context(), concurrentDefinition(t, &workflow.ConcurrentGroup{Steps: steps, MaxConcurrency: 2, FailFast: true}), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if attempts["stable"] != 1 || attempts["flaky"] != 2 {
		t.Fatalf("attempts = %#v", attempts)
	}
	if state.Stats.Attempts != 3 || state.Stats.Retries != 1 {
		t.Fatalf("stats = %#v", state.Stats)
	}
}

func TestRunConcurrentGroupFailFastControlsQueuedSteps(t *testing.T) {
	for _, test := range []struct {
		name     string
		failFast bool
		wantRuns int32
	}{
		{name: "fail fast", failFast: true, wantRuns: 0},
		{name: "wait for all", failFast: false, wantRuns: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			var laterRuns atomic.Int32
			registry := newTestRegistry(t, map[string]step.Builder{"fail_or_count": func(map[string]any) (step.Runner, error) {
				return runnerFunc(func(_ context.Context, request step.Request) (step.Result, error) {
					if request.StepID == "first" {
						return step.Result{}, errors.New("broken")
					}
					laterRuns.Add(1)
					return step.Result{}, nil
				}), nil
			}})
			steps := []workflow.Step{{ID: "first", Type: "fail_or_count", With: map[string]any{}}, {ID: "later", Type: "fail_or_count", With: map[string]any{}}}
			_, err := New(registry).Run(t.Context(), concurrentDefinition(t, &workflow.ConcurrentGroup{Steps: steps, MaxConcurrency: 1, FailFast: test.failFast}), Options{})
			if err == nil || !strings.Contains(err.Error(), "broken") {
				t.Fatalf("error = %v", err)
			}
			if laterRuns.Load() != test.wantRuns {
				t.Fatalf("later runs = %d, want %d", laterRuns.Load(), test.wantRuns)
			}
		})
	}
}

func TestRunConcurrentGroupAggregatesErrorsInDeclarationOrder(t *testing.T) {
	registry := newTestRegistry(t, map[string]step.Builder{"always_fail": func(map[string]any) (step.Runner, error) {
		return runnerFunc(func(_ context.Context, request step.Request) (step.Result, error) {
			return step.Result{}, fmt.Errorf("%s failed", request.StepID)
		}), nil
	}})
	steps := []workflow.Step{{ID: "first", Type: "always_fail", With: map[string]any{}}, {ID: "second", Type: "always_fail", With: map[string]any{}}}
	_, err := New(registry).Run(t.Context(), concurrentDefinition(t, &workflow.ConcurrentGroup{Steps: steps, MaxConcurrency: 2, FailFast: false}), Options{})
	if err == nil {
		t.Fatal("expected group failure")
	}
	message := err.Error()
	first := strings.Index(message, "first failed")
	second := strings.Index(message, "second failed")
	if first < 0 || second < 0 || first >= second {
		t.Fatalf("error order = %q", message)
	}
}

func TestRunConcurrentGroupTimeoutCancelsChildren(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		registry := newTestRegistry(t, map[string]step.Builder{"block": func(map[string]any) (step.Runner, error) {
			return runnerFunc(func(ctx context.Context, _ step.Request) (step.Result, error) {
				<-ctx.Done()
				return step.Result{}, ctx.Err()
			}), nil
		}})
		timeout := workflow.Duration(time.Second)
		steps := []workflow.Step{{ID: "one", Type: "block", With: map[string]any{}}, {ID: "two", Type: "block", With: map[string]any{}}}
		_, err := New(registry).Run(t.Context(), concurrentDefinition(t, &workflow.ConcurrentGroup{Steps: steps, MaxConcurrency: 2, Timeout: &timeout, FailFast: true}), Options{})
		if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "timed out after 1s") {
			t.Fatalf("error = %v", err)
		}
	})
}

func TestRunConcurrentGroupCancellationDoesNotAdmitQueuedChildren(t *testing.T) {
	started := make(chan struct{}, 1)
	var runs atomic.Int32
	registry := newTestRegistry(t, map[string]step.Builder{"block": func(map[string]any) (step.Runner, error) {
		return runnerFunc(func(ctx context.Context, _ step.Request) (step.Result, error) {
			runs.Add(1)
			started <- struct{}{}
			<-ctx.Done()
			return step.Result{}, ctx.Err()
		}), nil
	}})
	steps := []workflow.Step{
		{ID: "first", Type: "block", With: map[string]any{}},
		{ID: "queued", Type: "block", With: map[string]any{}},
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	var finished ProgressEvent
	definition := concurrentDefinition(t, &workflow.ConcurrentGroup{
		Steps: steps, MaxConcurrency: 1, FailFast: false,
	})
	go func() {
		_, err := New(registry).Run(ctx, definition, Options{Stdout: io.Discard, Stderr: io.Discard, Progress: func(event ProgressEvent) {
			if event.Kind == ConcurrentFinished {
				finished = event
			}
		}})
		done <- err
	}()
	<-started
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, want context.Canceled", err)
	}
	if runs.Load() != 1 || finished.Started != 1 || finished.Succeeded != 0 {
		t.Fatalf("runs = %d, progress = %#v", runs.Load(), finished)
	}
}

func TestRunConcurrentGroupUsesSnapshotAndRejectsVariableConflicts(t *testing.T) {
	registry := newTestRegistry(t, map[string]step.Builder{"write": func(raw map[string]any) (step.Runner, error) {
		value := maps.Clone(raw)
		return runnerFunc(func(_ context.Context, request step.Request) (step.Result, error) {
			variable, _ := value["variable"].(string)
			return step.Result{Outputs: map[string]any{"id": request.StepID}, Variables: map[string]any{variable: request.StepID}}, nil
		}), nil
	}})
	steps := []workflow.Step{
		{ID: "one", Type: "write", With: map[string]any{"variable": "shared"}},
		{ID: "two", Type: "write", With: map[string]any{"variable": "shared"}},
	}
	_, err := New(registry).Run(t.Context(), concurrentDefinition(t, &workflow.ConcurrentGroup{Steps: steps, MaxConcurrency: 2, FailFast: true}), Options{})
	if err == nil || !strings.Contains(err.Error(), `both write variable "shared"`) {
		t.Fatalf("conflict error = %v", err)
	}

	steps = []workflow.Step{
		{ID: "producer", Type: "write", With: map[string]any{"variable": "produced"}},
		{ID: "consumer", Type: "write", With: map[string]any{"variable": "{{ .vars.produced }}"}},
	}
	_, err = New(registry).Run(t.Context(), concurrentDefinition(t, &workflow.ConcurrentGroup{Steps: steps, MaxConcurrency: 2, FailFast: false}), Options{})
	if err == nil || !strings.Contains(err.Error(), `map has no entry for key "produced"`) {
		t.Fatalf("snapshot error = %v", err)
	}
}

func TestRunConcurrentGroupSerializesProgressAndOutput(t *testing.T) {
	registry := newTestRegistry(t, map[string]step.Builder{"write_output": func(map[string]any) (step.Runner, error) {
		return runnerFunc(func(_ context.Context, request step.Request) (step.Result, error) {
			if request.Interactive || request.Stdin != nil {
				return step.Result{}, errors.New("concurrent child received interactive stdin")
			}
			for range 20 {
				fmt.Fprintln(request.Stdout, request.StepID)
				fmt.Fprintln(request.Stderr, request.StepID)
			}
			return step.Result{}, nil
		}), nil
	}})
	steps := make([]workflow.Step, 12)
	for i := range steps {
		steps[i] = workflow.Step{ID: fmt.Sprintf("step_%d", i), Type: "write_output", With: map[string]any{}}
	}
	var output bytes.Buffer
	var events []ProgressEvent
	state, err := New(registry).Run(t.Context(), concurrentDefinition(t, &workflow.ConcurrentGroup{Steps: steps, MaxConcurrency: 12, FailFast: true}), Options{
		Stdin: strings.NewReader("shared input"), Interactive: true,
		Stdout: &output, Stderr: &output, Progress: func(event ProgressEvent) { events = append(events, event) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if state.Stats.Succeeded != len(steps) || len(events) == 0 || output.Len() == 0 {
		t.Fatalf("stats = %#v, events = %d, output = %d", state.Stats, len(events), output.Len())
	}
}

func TestConcurrentGroupDryRunShowsChildrenAndPolicies(t *testing.T) {
	registry := newTestRegistry(t, map[string]step.Builder{"noop": func(map[string]any) (step.Runner, error) {
		return runnerFunc(func(context.Context, step.Request) (step.Result, error) { return step.Result{}, nil }), nil
	}})
	timeout := workflow.Duration(5 * time.Minute)
	steps := []workflow.Step{
		{ID: "lint", Type: "noop", With: map[string]any{}},
		{ID: "test", Type: "noop", Retry: immediateRetry(2), With: map[string]any{}},
	}
	var output bytes.Buffer
	_, err := New(registry).Run(t.Context(), concurrentDefinition(t, &workflow.ConcurrentGroup{
		Steps: steps, MaxConcurrency: 2, Timeout: &timeout, FailFast: false,
	}), Options{DryRun: true, Stdout: &output, Stderr: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	want := `1. concurrent [max 2, timeout 5m0s, wait for all]
   1.1 lint (noop)
   1.2 test (noop) [2 attempts]
`
	if output.String() != want {
		t.Fatalf("output = %q, want %q", output.String(), want)
	}
}

// TestSlowProgressCallbackDoesNotBlockBranchOutput guards the separation of
// runRuntime.writeMu from runRuntime.reportMu. report holds the reporting lock
// across the callback; while that was the same lock the wrapped Stdout and
// Stderr use, a slow reporter stalled every concurrent branch trying to write.
// tui.Progress does terminal I/O inside that callback, so this is the real cost
// of the shared lock.
func TestSlowProgressCallbackDoesNotBlockBranchOutput(t *testing.T) {
	writerParked := make(chan struct{})
	blocking := make(chan struct{})
	release := make(chan struct{})
	wrote := make(chan struct{})

	registry := newTestRegistry(t, map[string]step.Builder{"branch": func(map[string]any) (step.Runner, error) {
		return runnerFunc(func(ctx context.Context, request step.Request) (step.Result, error) {
			switch request.StepID {
			case "writer":
				// Park inside the runner, past every report call this branch makes,
				// so the reporter can be held without stalling this goroutine there.
				close(writerParked)
				select {
				case <-blocking:
				case <-ctx.Done():
					return step.Result{}, ctx.Err()
				}
				fmt.Fprintln(request.Stdout, "writer produced output")
				close(wrote)
			case "blocker":
				select {
				case <-writerParked:
				case <-ctx.Done():
					return step.Result{}, ctx.Err()
				}
			}
			return step.Result{Outputs: map[string]any{"id": request.StepID}}, nil
		}), nil
	}})

	definition := concurrentDefinition(t, &workflow.ConcurrentGroup{
		Steps: []workflow.Step{
			{ID: "writer", Type: "branch", With: map[string]any{}},
			{ID: "blocker", Type: "branch", With: map[string]any{}},
		},
		MaxConcurrency: 2, FailFast: true,
	})

	var output lockedBuffer
	var parked sync.Once
	done := make(chan error, 1)
	go func() {
		_, err := New(registry).Run(t.Context(), definition, Options{
			Stdout: &output, Stderr: io.Discard,
			Progress: func(event ProgressEvent) {
				// StepFinished for "blocker" only fires once its runner has returned,
				// which it does only after "writer" is parked inside its own runner.
				if event.Kind != StepFinished || event.StepID != "blocker" {
					return
				}
				parked.Do(func() {
					close(blocking)
					<-release
				})
			},
		})
		done <- err
	}()

	select {
	case <-wrote:
	case <-time.After(15 * time.Second):
		close(release)
		<-done
		t.Fatal("branch output blocked behind a parked Progress callback")
	}
	close(release)

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("Run() did not finish")
	}
	if !strings.Contains(output.String(), "writer produced output") {
		t.Errorf("output missing writer line; got:\n%s", output.String())
	}
}

// lockedBuffer is the assertion target, not the thing under test: the engine
// already serializes writes, but the test reads the buffer from another
// goroutine once Run returns.
type lockedBuffer struct {
	mu     sync.Mutex
	buffer strings.Builder
}

func (b *lockedBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.Write(data)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.String()
}
