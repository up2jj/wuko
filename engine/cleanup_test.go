package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/steps/shell"
	tempstep "github.com/up2jj/wuko/steps/temp"
	"github.com/up2jj/wuko/workflow"
)

func TestManagedTempRemainsAvailableThroughFinally(t *testing.T) {
	var observedPath string
	registry := newTestRegistry(t, map[string]step.Builder{"observe_path": func(raw map[string]any) (step.Runner, error) {
		path, _ := raw["path"].(string)
		return runnerFunc(func(context.Context, step.Request) (step.Result, error) {
			if _, err := os.Stat(path); err != nil {
				return step.Result{}, fmt.Errorf("observing managed path: %w", err)
			}
			observedPath = path
			return step.Result{}, nil
		}), nil
	}})
	if err := tempstep.Register(registry); err != nil {
		t.Fatal(err)
	}
	definition := testDefinition(t, "managed-temp", workflow.Step{ID: "workspace", Type: "temp", With: map[string]any{"kind": "directory"}})
	definition.Finally = []workflow.Step{{ID: "observe", Type: "observe_path", With: map[string]any{
		"path": "{{ .steps.workspace.path }}",
	}}}
	state, err := New(registry).Run(t.Context(), definition, Options{})
	if err != nil {
		t.Fatal(err)
	}
	path := state.Steps["workspace"].(map[string]any)["path"].(string)
	if observedPath != path {
		t.Fatalf("finally observed %q, want %q", observedPath, path)
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("managed path remains after Run: %v", err)
	}
}

func TestManagedTempInsideActionLivesUntilRootCleanup(t *testing.T) {
	var observedPath string
	registry := newTestRegistry(t, map[string]step.Builder{"observe_action_path": func(raw map[string]any) (step.Runner, error) {
		path, _ := raw["path"].(string)
		return runnerFunc(func(context.Context, step.Request) (step.Result, error) {
			if _, err := os.Stat(path); err != nil {
				return step.Result{}, err
			}
			observedPath = path
			return step.Result{}, nil
		}), nil
	}})
	if err := tempstep.Register(registry); err != nil {
		t.Fatal(err)
	}
	action := testAction(t, "temporary-action", workflow.Step{ID: "workspace", Type: "temp", With: map[string]any{"kind": "file"}})
	action.Outputs = map[string]workflow.ActionOutput{"path": {Value: "steps.workspace.path"}}
	definition := testDefinition(t, "caller", workflow.Step{
		ID: "action", Uses: workflow.ActionSource{URL: "https://example.test/action"},
		Action: action,
	})
	definition.Finally = []workflow.Step{{ID: "observe", Type: "observe_action_path", With: map[string]any{
		"path": "{{ .steps.action.path }}",
	}}}
	state, err := New(registry).Run(t.Context(), definition, Options{})
	if err != nil {
		t.Fatal(err)
	}
	path := state.Steps["action"].(map[string]any)["path"].(string)
	if observedPath != path {
		t.Fatalf("root finally observed %q, want %q", observedPath, path)
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("action temp remains after root Run: %v", err)
	}
}

func TestManagedTempValidationAndDryRunCreateNothing(t *testing.T) {
	registry := newTestRegistry(t, nil)
	if err := tempstep.Register(registry); err != nil {
		t.Fatal(err)
	}
	patterns := []string{"wuko-no-execute-test-*", "wuko-fifo-*"}
	matches := func() []string {
		var values []string
		for _, pattern := range patterns {
			matches, err := filepath.Glob(filepath.Join(os.TempDir(), pattern))
			if err != nil {
				t.Fatal(err)
			}
			values = append(values, matches...)
		}
		return values
	}
	before := matches()
	definition := testDefinition(t, "no-execute",
		workflow.Step{ID: "workspace", Type: "temp", With: map[string]any{"kind": "directory", "pattern": patterns[0]}},
		workflow.Step{ID: "channel", Type: "temp", With: map[string]any{"kind": "fifo", "pattern": "wuko-no-execute-fifo-*"}},
	)

	engine := New(registry)
	if err := engine.Validate(t.Context(), definition, Options{Stdout: io.Discard, Stderr: io.Discard}); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Run(t.Context(), definition, Options{DryRun: true, Stdout: io.Discard, Stderr: io.Discard}); err != nil {
		t.Fatal(err)
	}
	if after := matches(); !reflect.DeepEqual(after, before) {
		t.Fatalf("temporary paths changed: before=%#v after=%#v", before, after)
	}
}

func TestManagedFIFOConnectsConcurrentProcessesAndLivesThroughFinally(t *testing.T) {
	var observed bool
	registry := newTestRegistry(t, map[string]step.Builder{"observe_fifo": func(raw map[string]any) (step.Runner, error) {
		path, _ := raw["path"].(string)
		return runnerFunc(func(context.Context, step.Request) (step.Result, error) {
			info, err := os.Lstat(path)
			if err != nil {
				return step.Result{}, fmt.Errorf("observing managed FIFO: %w", err)
			}
			if info.Mode()&os.ModeNamedPipe == 0 {
				return step.Result{}, fmt.Errorf("managed path %s is not a FIFO", path)
			}
			observed = true
			return step.Result{}, nil
		}), nil
	}})
	for _, register := range []func(*step.Registry) error{tempstep.Register, shell.Register} {
		if err := register(registry); err != nil {
			t.Fatal(err)
		}
	}

	timeout := workflow.Duration(5 * time.Second)
	definition := testDefinition(t, "managed-fifo",
		workflow.Step{ID: "channel", Type: "temp", With: map[string]any{"kind": "fifo", "pattern": "events-*"}},
		workflow.Step{Concurrent: &workflow.ConcurrentGroup{
			MaxConcurrency: 2,
			FailFast:       true,
			Timeout:        &timeout,
			Steps: []workflow.Step{
				{ID: "reader", Type: "shell", With: map[string]any{
					"script": `cat "$1"`, "args": []any{"{{ .steps.channel.path }}"},
				}},
				{ID: "writer", Type: "shell", With: map[string]any{
					"script": `printf %s "$2" > "$1"`, "args": []any{"{{ .steps.channel.path }}", "hello through fifo"},
				}},
			},
		}},
	)
	definition.Finally = []workflow.Step{{ID: "observe", Type: "observe_fifo", With: map[string]any{
		"path": "{{ .steps.channel.path }}",
	}}}

	state, err := New(registry).Run(t.Context(), definition, Options{Stdout: io.Discard, Stderr: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if !observed {
		t.Fatal("finally did not observe the managed FIFO")
	}
	if output := state.Steps["reader"].(map[string]any)["stdout"]; output != "hello through fifo" {
		t.Fatalf("reader stdout = %q", output)
	}
	path := state.Steps["channel"].(map[string]any)["path"].(string)
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("managed FIFO remains after Run: %v", err)
	}
	if _, err := os.Lstat(filepath.Dir(path)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("managed FIFO directory remains after Run: %v", err)
	}
}

func TestManagedCleanupRunsInReverseAndJoinsErrors(t *testing.T) {
	registry := newTestRegistry(t, nil)
	var mu sync.Mutex
	var cleaned []string
	if err := registry.Register("managed", func(raw map[string]any) (step.Runner, error) {
		label, _ := raw["label"].(string)
		fail, _ := raw["fail"].(bool)
		return &managedTestRunner{label: label, fail: fail, mu: &mu, cleaned: &cleaned}, nil
	}); err != nil {
		t.Fatal(err)
	}
	definition := testDefinition(t, "cleanup-order",
		workflow.Step{ID: "first", Type: "managed", With: map[string]any{"label": "first", "fail": true}},
		workflow.Step{ID: "second", Type: "managed", With: map[string]any{"label": "second"}},
		workflow.Step{ID: "third", Type: "managed", With: map[string]any{"label": "third", "fail": true}},
	)

	state, err := New(registry).Run(t.Context(), definition, Options{})
	if state != nil || err == nil {
		t.Fatalf("state=%#v error=%v, want cleanup failure", state, err)
	}
	if !strings.Contains(err.Error(), "cleanup third") || !strings.Contains(err.Error(), "cleanup first") {
		t.Fatalf("cleanup error = %v", err)
	}
	if want := []string{"third", "second", "first"}; !reflect.DeepEqual(cleaned, want) {
		t.Fatalf("cleanup order = %#v, want %#v", cleaned, want)
	}
}

func TestManagedCleanupRegistersEverySuccessfulPoll(t *testing.T) {
	registry := newTestRegistry(t, nil)
	runner := &pollCleanupRunner{}
	if err := registry.Register("managed_poll", func(map[string]any) (step.Runner, error) { return runner, nil }); err != nil {
		t.Fatal(err)
	}
	timeout := workflow.Duration(1_000_000_000)
	definition := testDefinition(t, "poll-cleanup", workflow.Step{
		ID: "wait", Type: "wait", Timeout: &timeout, With: map[string]any{
			"step":  map[string]any{"type": "managed_poll", "with": map[string]any{}},
			"until": "result.poll == 3", "interval": "1ns",
		},
	})

	if _, err := New(registry).Run(t.Context(), definition, Options{}); err != nil {
		t.Fatal(err)
	}
	if want := []int{3, 2, 1}; !reflect.DeepEqual(runner.cleaned, want) {
		t.Fatalf("cleaned polls = %#v, want %#v", runner.cleaned, want)
	}
}

func TestManagedCleanupRegistersOnlySuccessfulRetry(t *testing.T) {
	registry := newTestRegistry(t, nil)
	runner := &retryCleanupRunner{}
	if err := registry.Register("managed_retry", func(map[string]any) (step.Runner, error) { return runner, nil }); err != nil {
		t.Fatal(err)
	}
	definition := testDefinition(t, "retry-cleanup", workflow.Step{
		ID: "retry", Type: "managed_retry", Retry: &workflow.RetryPolicy{MaxAttempts: 2, BackoffMultiplier: 1}, With: map[string]any{},
	})

	if _, err := New(registry).Run(t.Context(), definition, Options{}); err != nil {
		t.Fatal(err)
	}
	if runner.attempts != 2 || !reflect.DeepEqual(runner.cleaned, []int{2}) {
		t.Fatalf("attempts=%d cleaned=%#v", runner.attempts, runner.cleaned)
	}
}

func TestManagedCleanupConcurrentAndFanoutRegistration(t *testing.T) {
	registry := newTestRegistry(t, nil)
	var mu sync.Mutex
	var cleaned []string
	if err := registry.Register("managed_parallel", func(raw map[string]any) (step.Runner, error) {
		label, _ := raw["label"].(string)
		return &managedTestRunner{label: label, mu: &mu, cleaned: &cleaned}, nil
	}); err != nil {
		t.Fatal(err)
	}
	children := make([]workflow.Step, 16)
	for index := range children {
		children[index] = workflow.Step{ID: fmt.Sprintf("child_%d", index), Type: "managed_parallel", With: map[string]any{"label": fmt.Sprintf("child_%d", index)}}
	}
	definition := testDefinition(t, "parallel-cleanup",
		workflow.Step{Concurrent: &workflow.ConcurrentGroup{Steps: children, MaxConcurrency: 8, FailFast: false}},
		workflow.Step{ID: "fanout", Foreach: &workflow.ForeachGroup{
			Items: "vars.items", MaxConcurrency: 4, FailFast: true,
			Steps: []workflow.Step{{ID: "iteration", Type: "managed_parallel", With: map[string]any{"label": "{{ .foreach.item }}"}}},
		}},
	)
	definition.Vars = map[string]any{"items": []any{"a", "b", "c", "d"}}
	if _, err := New(registry).Run(t.Context(), definition, Options{}); err != nil {
		t.Fatal(err)
	}
	if len(cleaned) != 20 {
		t.Fatalf("cleanup count = %d, want 20: %#v", len(cleaned), cleaned)
	}
}

type managedTestRunner struct {
	label   string
	fail    bool
	mu      *sync.Mutex
	cleaned *[]string
}

func (runner *managedTestRunner) Run(context.Context, step.Request) (step.Result, error) {
	return step.Result{Outputs: map[string]any{"label": runner.label}}, nil
}

func (runner *managedTestRunner) Cleanup(_ context.Context, result step.Result) error {
	label, _ := result.Outputs["label"].(string)
	runner.mu.Lock()
	*runner.cleaned = append(*runner.cleaned, label)
	runner.mu.Unlock()
	if runner.fail {
		return fmt.Errorf("cleanup %s", label)
	}
	return nil
}

type pollCleanupRunner struct {
	mu      sync.Mutex
	polls   int
	cleaned []int
}

type retryCleanupRunner struct {
	attempts int
	cleaned  []int
}

func (runner *retryCleanupRunner) Run(_ context.Context, request step.Request) (step.Result, error) {
	runner.attempts++
	if request.Attempt == 1 {
		return step.Result{}, errors.New("temporary failure")
	}
	return step.Result{Outputs: map[string]any{"attempt": request.Attempt}}, nil
}

func (runner *retryCleanupRunner) Cleanup(_ context.Context, result step.Result) error {
	runner.cleaned = append(runner.cleaned, result.Outputs["attempt"].(int))
	return nil
}

func (runner *pollCleanupRunner) Run(context.Context, step.Request) (step.Result, error) {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	runner.polls++
	return step.Result{Outputs: map[string]any{"poll": runner.polls}}, nil
}

func (runner *pollCleanupRunner) Cleanup(_ context.Context, result step.Result) error {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	runner.cleaned = append(runner.cleaned, result.Outputs["poll"].(int))
	return nil
}

var _ step.Cleaner = (*managedTestRunner)(nil)
var _ step.Cleaner = (*pollCleanupRunner)(nil)
var _ step.Cleaner = (*retryCleanupRunner)(nil)

// TestManagedCleanupContextSurvivesRunCancellation pins the contract documented on
// step.Cleaner: the context handed to Cleanup carries the run's values but is
// detached from its cancellation, so managed resources are still released after
// Ctrl-C. Before Cleanup took a context, implementations had to invent
// context.Background() themselves and the engine could not express this.
func TestManagedCleanupContextSurvivesRunCancellation(t *testing.T) {
	type ctxKey struct{}

	started := make(chan struct{})
	var observed struct {
		mu     sync.Mutex
		err    error
		value  any
		called bool
	}

	registry := newTestRegistry(t, map[string]step.Builder{
		"managed": func(map[string]any) (step.Runner, error) {
			return &contextCleanupRunner{observe: func(cleanupCtx context.Context) {
				observed.mu.Lock()
				defer observed.mu.Unlock()
				observed.called = true
				observed.err = cleanupCtx.Err()
				observed.value = cleanupCtx.Value(ctxKey{})
			}}, nil
		},
		"block": func(map[string]any) (step.Runner, error) {
			return runnerFunc(func(ctx context.Context, _ step.Request) (step.Result, error) {
				close(started)
				<-ctx.Done()
				return step.Result{}, ctx.Err()
			}), nil
		},
	})

	definition := testDefinition(t, "cancel-cleanup",
		workflow.Step{ID: "resource", Type: "managed", With: map[string]any{}},
		workflow.Step{ID: "waiter", Type: "block", With: map[string]any{}},
	)

	ctx, cancel := context.WithCancel(context.WithValue(t.Context(), ctxKey{}, "carried"))
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = New(registry).Run(ctx, definition, Options{})
	}()

	<-started
	cancel()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("Run() did not return after cancellation")
	}

	observed.mu.Lock()
	defer observed.mu.Unlock()
	if !observed.called {
		t.Fatal("Cleanup was not called after the run was canceled")
	}
	if observed.err != nil {
		t.Errorf("cleanup context Err() = %v, want nil (cleanup must not inherit cancellation)", observed.err)
	}
	if observed.value != "carried" {
		t.Errorf("cleanup context value = %v, want %q (cleanup must carry run values)", observed.value, "carried")
	}
}

type contextCleanupRunner struct {
	observe func(context.Context)
}

func (*contextCleanupRunner) Run(context.Context, step.Request) (step.Result, error) {
	return step.Result{Outputs: map[string]any{}}, nil
}

func (runner *contextCleanupRunner) Cleanup(ctx context.Context, _ step.Result) error {
	runner.observe(ctx)
	return nil
}

var _ step.Cleaner = (*contextCleanupRunner)(nil)
