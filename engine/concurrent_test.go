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
			if request.StepID == "flaky_body" && request.Attempt == 1 {
				return step.Result{}, errors.New("temporary")
			}
			return step.Result{Outputs: map[string]any{"attempt": request.Attempt}}, nil
		}), nil
	}})
	policy := immediateRetry(2)
	steps := []workflow.Step{
		attemptStep("stable", policy, workflow.Step{Type: "retry_child", With: map[string]any{}}),
		attemptStep("flaky", policy, workflow.Step{Type: "retry_child", With: map[string]any{}}),
	}
	state, err := New(registry).Run(t.Context(), concurrentDefinition(t, &workflow.ConcurrentGroup{Steps: steps, MaxConcurrency: 2, FailFast: true}), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if attempts["stable_body"] != 1 || attempts["flaky_body"] != 2 {
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
		{ID: "consumer", Type: "write", If: "vars.produced != nil", With: map[string]any{"variable": "observed"}},
	}
	definition := concurrentDefinition(t, &workflow.ConcurrentGroup{Steps: steps, MaxConcurrency: 2, FailFast: false})
	definition.Vars = map[string]any{"produced": nil}
	state, err := New(registry).Run(t.Context(), definition, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if state.Vars["produced"] != "producer" {
		t.Fatalf("producer value = %#v", state.Vars["produced"])
	}
	if _, exists := state.Steps["consumer"]; exists {
		t.Fatalf("consumer observed a sibling write: %#v", state.Steps)
	}
}

func TestRunConcurrentGroupNeedsPropagatesAncestorState(t *testing.T) {
	var mu sync.Mutex
	observed := make(map[string]step.Request)
	registry := newTestRegistry(t, map[string]step.Builder{"dag": func(map[string]any) (step.Runner, error) {
		return runnerFunc(func(_ context.Context, request step.Request) (step.Result, error) {
			mu.Lock()
			observed[request.StepID] = request
			mu.Unlock()
			return step.Result{
				Outputs:   map[string]any{"value": request.StepID},
				Variables: map[string]any{"var_" + request.StepID: request.StepID},
			}, nil
		}), nil
	}})
	steps := []workflow.Step{
		{ID: "build", Type: "dag", Needs: []string{"lint", "test"}, With: map[string]any{}},
		{ID: "lint", Type: "dag", Needs: []string{"deps"}, With: map[string]any{}},
		{ID: "deps", Type: "dag", With: map[string]any{}},
		{ID: "test", Type: "dag", Needs: []string{"deps"}, With: map[string]any{}},
	}
	state, err := New(registry).Run(t.Context(), concurrentDefinition(t, &workflow.ConcurrentGroup{Steps: steps, MaxConcurrency: 2, FailFast: false}), Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"deps", "lint", "test"} {
		if _, exists := observed["build"].Steps[id]; !exists {
			t.Errorf("build did not observe step %q: %#v", id, observed["build"].Steps)
		}
		if observed["build"].Vars["var_"+id] != id {
			t.Errorf("build variable from %q = %#v", id, observed["build"].Vars["var_"+id])
		}
	}
	if _, exists := observed["lint"].Steps["test"]; exists {
		t.Fatalf("lint observed unrelated sibling state: %#v", observed["lint"].Steps)
	}
	if len(state.Steps) != 4 || state.Vars["var_build"] != "build" {
		t.Fatalf("committed state = %#v", state)
	}
}

func TestRunConcurrentGroupNeedsMergesReadsInDeclarationOrderBeforeJoinConflict(t *testing.T) {
	observed := make(chan any, 1)
	registry := newTestRegistry(t, map[string]step.Builder{"merge": func(map[string]any) (step.Runner, error) {
		return runnerFunc(func(_ context.Context, request step.Request) (step.Result, error) {
			if request.StepID == "consumer" {
				observed <- request.Vars["shared"]
				return step.Result{}, nil
			}
			return step.Result{Variables: map[string]any{"shared": request.StepID}}, nil
		}), nil
	}})
	steps := []workflow.Step{
		{ID: "first", Type: "merge", With: map[string]any{}},
		{ID: "second", Type: "merge", With: map[string]any{}},
		{ID: "consumer", Type: "merge", Needs: []string{"first", "second"}, With: map[string]any{}},
	}
	definition := concurrentDefinition(t, &workflow.ConcurrentGroup{Steps: steps, MaxConcurrency: 2, FailFast: false})
	definition.Vars = map[string]any{"shared": "initial"}
	_, err := New(registry).Run(t.Context(), definition, Options{})
	if err == nil || !strings.Contains(err.Error(), `steps "first" and "second" both write variable "shared"`) {
		t.Fatalf("conflict error = %v", err)
	}
	if got := <-observed; got != "second" {
		t.Fatalf("consumer observed shared = %#v, want second", got)
	}
}

func TestRunConcurrentGroupNeedsMergesAncestorsInDependencyOrder(t *testing.T) {
	observed := make(chan any, 1)
	registry := newTestRegistry(t, map[string]step.Builder{"merge": func(map[string]any) (step.Runner, error) {
		return runnerFunc(func(_ context.Context, request step.Request) (step.Result, error) {
			if request.StepID == "consumer" {
				observed <- request.Vars["shared"]
				return step.Result{}, nil
			}
			return step.Result{Variables: map[string]any{"shared": request.StepID}}, nil
		}), nil
	}})
	// "later" is declared before the "earlier" it needs, so slice order and
	// dependency order disagree; the consumer must see the later write.
	steps := []workflow.Step{
		{ID: "later", Type: "merge", Needs: []string{"earlier"}, With: map[string]any{}},
		{ID: "earlier", Type: "merge", With: map[string]any{}},
		{ID: "consumer", Type: "merge", Needs: []string{"later"}, With: map[string]any{}},
	}
	definition := concurrentDefinition(t, &workflow.ConcurrentGroup{Steps: steps, MaxConcurrency: 2, FailFast: false})
	definition.Vars = map[string]any{"shared": "initial"}
	_, err := New(registry).Run(t.Context(), definition, Options{})
	if err == nil || !strings.Contains(err.Error(), `both write variable "shared"`) {
		t.Fatalf("conflict error = %v", err)
	}
	if got := <-observed; got != "later" {
		t.Fatalf("consumer observed shared = %#v, want later", got)
	}
}

func TestRunConcurrentGroupNeedsSkipsDescendantsAndContinuesIndependentWork(t *testing.T) {
	var runs sync.Map
	registry := newTestRegistry(t, map[string]step.Builder{"dependency": func(map[string]any) (step.Runner, error) {
		return runnerFunc(func(_ context.Context, request step.Request) (step.Result, error) {
			runs.Store(request.StepID, true)
			if request.StepID == "failed" {
				return step.Result{}, errors.New("broken prerequisite")
			}
			return step.Result{}, nil
		}), nil
	}})
	steps := []workflow.Step{
		{ID: "failed", Type: "dependency", With: map[string]any{}},
		{ID: "child", Type: "dependency", Needs: []string{"failed"}, With: map[string]any{}},
		{ID: "grandchild", Type: "dependency", Needs: []string{"child"}, With: map[string]any{}},
		{ID: "independent", Type: "dependency", With: map[string]any{}},
	}
	statuses := make(map[string]ExecutionStatus)
	_, err := New(registry).Run(t.Context(), concurrentDefinition(t, &workflow.ConcurrentGroup{Steps: steps, MaxConcurrency: 2, FailFast: false}), Options{Progress: func(event ProgressEvent) {
		if event.Kind == StepFinished {
			statuses[event.StepID] = event.Status
		}
	}})
	if err == nil || !strings.Contains(err.Error(), "broken prerequisite") {
		t.Fatalf("error = %v", err)
	}
	if _, ran := runs.Load("child"); ran {
		t.Fatal("failed dependency child ran")
	}
	if _, ran := runs.Load("grandchild"); ran {
		t.Fatal("failed dependency grandchild ran")
	}
	if _, ran := runs.Load("independent"); !ran {
		t.Fatal("independent branch did not run with fail_fast false")
	}
	if statuses["child"] != StatusSkipped || statuses["grandchild"] != StatusSkipped {
		t.Fatalf("dependency statuses = %#v", statuses)
	}
}

func TestRunConcurrentGroupNeedsSkipsDescendantsBeforeFailFastStopsAdmission(t *testing.T) {
	var independentRuns atomic.Int32
	registry := newTestRegistry(t, map[string]step.Builder{"fail_fast_dependency": func(map[string]any) (step.Runner, error) {
		return runnerFunc(func(_ context.Context, request step.Request) (step.Result, error) {
			if request.StepID == "failed" {
				return step.Result{}, errors.New("broken prerequisite")
			}
			independentRuns.Add(1)
			return step.Result{}, nil
		}), nil
	}})
	steps := []workflow.Step{
		{ID: "failed", Type: "fail_fast_dependency", With: map[string]any{}},
		{ID: "child", Type: "fail_fast_dependency", Needs: []string{"failed"}, With: map[string]any{}},
		{ID: "independent", Type: "fail_fast_dependency", With: map[string]any{}},
	}
	statuses := make(map[string]ExecutionStatus)
	_, err := New(registry).Run(t.Context(), concurrentDefinition(t, &workflow.ConcurrentGroup{Steps: steps, MaxConcurrency: 1, FailFast: true}), Options{Progress: func(event ProgressEvent) {
		if event.Kind == StepFinished {
			statuses[event.StepID] = event.Status
		}
	}})
	if err == nil || !strings.Contains(err.Error(), "broken prerequisite") {
		t.Fatalf("error = %v", err)
	}
	if statuses["child"] != StatusSkipped {
		t.Fatalf("child status = %q", statuses["child"])
	}
	if independentRuns.Load() != 0 {
		t.Fatalf("independent runs = %d", independentRuns.Load())
	}
}

func TestRunConcurrentGroupConditionSkippedNeedAllowsDependent(t *testing.T) {
	var dependentRuns atomic.Int32
	registry := newTestRegistry(t, map[string]step.Builder{"conditional_need": func(map[string]any) (step.Runner, error) {
		return runnerFunc(func(_ context.Context, request step.Request) (step.Result, error) {
			if request.StepID == "dependent" {
				dependentRuns.Add(1)
				if _, exists := request.Steps["optional"]; exists {
					return step.Result{}, errors.New("skipped prerequisite published state")
				}
			}
			return step.Result{}, nil
		}), nil
	}})
	steps := []workflow.Step{
		{ID: "optional", Type: "conditional_need", If: "false", With: map[string]any{}},
		{ID: "dependent", Type: "conditional_need", Needs: []string{"optional"}, With: map[string]any{}},
	}
	if _, err := New(registry).Run(t.Context(), concurrentDefinition(t, &workflow.ConcurrentGroup{Steps: steps, MaxConcurrency: 2, FailFast: true}), Options{}); err != nil {
		t.Fatal(err)
	}
	if dependentRuns.Load() != 1 {
		t.Fatalf("dependent runs = %d", dependentRuns.Load())
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
		func() workflow.Step {
			wrapped := attemptStep("test", immediateRetry(2), workflow.Step{Type: "noop", With: map[string]any{}})
			wrapped.Needs = []string{"lint"}
			return wrapped
		}(),
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
   1.2 test (attempt) [2 attempts] [needs: lint]
      1.2.1 test_body (noop)
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
