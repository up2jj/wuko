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

	"github.com/up2jj/wuko/step"
	tempstep "github.com/up2jj/wuko/steps/temp"
	"github.com/up2jj/wuko/workflow"
)

func TestManagedTempRemainsAvailableThroughFinally(t *testing.T) {
	registry := step.NewRegistry()
	if err := tempstep.Register(registry); err != nil {
		t.Fatal(err)
	}
	var observedPath string
	if err := registry.Register("observe_path", func(raw map[string]any) (step.Runner, error) {
		path, _ := raw["path"].(string)
		return runnerFunc(func(context.Context, step.Request) (step.Result, error) {
			if _, err := os.Stat(path); err != nil {
				return step.Result{}, fmt.Errorf("observing managed path: %w", err)
			}
			observedPath = path
			return step.Result{}, nil
		}), nil
	}); err != nil {
		t.Fatal(err)
	}
	definition := &workflow.Definition{
		Version: 1, Name: "managed-temp", Dir: t.TempDir(),
		Steps: []workflow.Step{{ID: "workspace", Type: "temp", With: map[string]any{"kind": "directory"}}},
		Finally: []workflow.Step{{ID: "observe", Type: "observe_path", With: map[string]any{
			"path": "{{ .steps.workspace.path }}",
		}}},
	}
	state, err := New(registry).Run(t.Context(), definition, Options{Stdout: io.Discard, Stderr: io.Discard})
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
	registry := step.NewRegistry()
	if err := tempstep.Register(registry); err != nil {
		t.Fatal(err)
	}
	var observedPath string
	if err := registry.Register("observe_action_path", func(raw map[string]any) (step.Runner, error) {
		path, _ := raw["path"].(string)
		return runnerFunc(func(context.Context, step.Request) (step.Result, error) {
			if _, err := os.Stat(path); err != nil {
				return step.Result{}, err
			}
			observedPath = path
			return step.Result{}, nil
		}), nil
	}); err != nil {
		t.Fatal(err)
	}
	action := &workflow.Action{
		Version: 1, Name: "temporary-action", Dir: t.TempDir(),
		Outputs: map[string]workflow.ActionOutput{"path": {Value: "steps.workspace.path"}},
		Steps:   []workflow.Step{{ID: "workspace", Type: "temp", With: map[string]any{"kind": "file"}}},
	}
	definition := &workflow.Definition{
		Version: 1, Name: "caller", Dir: t.TempDir(),
		Steps: []workflow.Step{{
			ID: "action", Uses: workflow.ActionSource{URL: "https://example.test/action"},
			Action: action, With: map[string]any{},
		}},
		Finally: []workflow.Step{{ID: "observe", Type: "observe_action_path", With: map[string]any{
			"path": "{{ .steps.action.path }}",
		}}},
	}
	state, err := New(registry).Run(t.Context(), definition, Options{Stdout: io.Discard, Stderr: io.Discard})
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
	registry := step.NewRegistry()
	if err := tempstep.Register(registry); err != nil {
		t.Fatal(err)
	}
	pattern := "wuko-no-execute-test-*"
	matches := func() []string {
		values, err := filepath.Glob(filepath.Join(os.TempDir(), pattern))
		if err != nil {
			t.Fatal(err)
		}
		return values
	}
	before := matches()
	definition := &workflow.Definition{Version: 1, Name: "no-execute", Dir: t.TempDir(), Steps: []workflow.Step{{
		ID: "workspace", Type: "temp", With: map[string]any{"kind": "directory", "pattern": pattern},
	}}}
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

func TestManagedCleanupRunsInReverseAndJoinsErrors(t *testing.T) {
	registry := step.NewRegistry()
	var mu sync.Mutex
	var cleaned []string
	if err := registry.Register("managed", func(raw map[string]any) (step.Runner, error) {
		label, _ := raw["label"].(string)
		fail, _ := raw["fail"].(bool)
		return &managedTestRunner{label: label, fail: fail, mu: &mu, cleaned: &cleaned}, nil
	}); err != nil {
		t.Fatal(err)
	}
	definition := &workflow.Definition{Version: 1, Name: "cleanup-order", Dir: t.TempDir(), Steps: []workflow.Step{
		{ID: "first", Type: "managed", With: map[string]any{"label": "first", "fail": true}},
		{ID: "second", Type: "managed", With: map[string]any{"label": "second"}},
		{ID: "third", Type: "managed", With: map[string]any{"label": "third", "fail": true}},
	}}
	state, err := New(registry).Run(t.Context(), definition, Options{Stdout: io.Discard, Stderr: io.Discard})
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
	registry := step.NewRegistry()
	runner := &pollCleanupRunner{}
	if err := registry.Register("managed_poll", func(map[string]any) (step.Runner, error) { return runner, nil }); err != nil {
		t.Fatal(err)
	}
	timeout := workflow.Duration(1_000_000_000)
	definition := &workflow.Definition{Version: 1, Name: "poll-cleanup", Dir: t.TempDir(), Steps: []workflow.Step{{
		ID: "wait", Type: "wait", Timeout: &timeout, With: map[string]any{
			"step":  map[string]any{"type": "managed_poll", "with": map[string]any{}},
			"until": "result.poll == 3", "interval": "1ns",
		},
	}}}
	if _, err := New(registry).Run(t.Context(), definition, Options{Stdout: io.Discard, Stderr: io.Discard}); err != nil {
		t.Fatal(err)
	}
	if want := []int{3, 2, 1}; !reflect.DeepEqual(runner.cleaned, want) {
		t.Fatalf("cleaned polls = %#v, want %#v", runner.cleaned, want)
	}
}

func TestManagedCleanupRegistersOnlySuccessfulRetry(t *testing.T) {
	registry := step.NewRegistry()
	runner := &retryCleanupRunner{}
	if err := registry.Register("managed_retry", func(map[string]any) (step.Runner, error) { return runner, nil }); err != nil {
		t.Fatal(err)
	}
	definition := &workflow.Definition{Version: 1, Name: "retry-cleanup", Dir: t.TempDir(), Steps: []workflow.Step{{
		ID: "retry", Type: "managed_retry", Retry: &workflow.RetryPolicy{MaxAttempts: 2, BackoffMultiplier: 1}, With: map[string]any{},
	}}}
	if _, err := New(registry).Run(t.Context(), definition, Options{Stdout: io.Discard, Stderr: io.Discard}); err != nil {
		t.Fatal(err)
	}
	if runner.attempts != 2 || !reflect.DeepEqual(runner.cleaned, []int{2}) {
		t.Fatalf("attempts=%d cleaned=%#v", runner.attempts, runner.cleaned)
	}
}

func TestManagedCleanupConcurrentAndFanoutRegistration(t *testing.T) {
	registry := step.NewRegistry()
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
	definition := &workflow.Definition{Version: 1, Name: "parallel-cleanup", Dir: t.TempDir(), Steps: []workflow.Step{
		{ID: "parallel", Concurrent: &workflow.ConcurrentGroup{Steps: children, MaxConcurrency: 8, FailFast: false}},
		{ID: "fanout", Foreach: &workflow.ForeachGroup{
			Items: "vars.items", MaxConcurrency: 4, FailFast: true,
			Steps: []workflow.Step{{ID: "iteration", Type: "managed_parallel", With: map[string]any{"label": "{{ .foreach.item }}"}}},
		}},
	}, Vars: map[string]any{"items": []any{"a", "b", "c", "d"}}}
	if _, err := New(registry).Run(t.Context(), definition, Options{Stdout: io.Discard, Stderr: io.Discard}); err != nil {
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
