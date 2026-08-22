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

func (runner *managedTestRunner) Cleanup(result step.Result) error {
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

func (runner *retryCleanupRunner) Cleanup(result step.Result) error {
	runner.cleaned = append(runner.cleaned, result.Outputs["attempt"].(int))
	return nil
}

func (runner *pollCleanupRunner) Run(context.Context, step.Request) (step.Result, error) {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	runner.polls++
	return step.Result{Outputs: map[string]any{"poll": runner.polls}}, nil
}

func (runner *pollCleanupRunner) Cleanup(result step.Result) error {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	runner.cleaned = append(runner.cleaned, result.Outputs["poll"].(int))
	return nil
}

var _ step.Cleaner = (*managedTestRunner)(nil)
var _ step.Cleaner = (*pollCleanupRunner)(nil)
var _ step.Cleaner = (*retryCleanupRunner)(nil)
