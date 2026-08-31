package engine

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/up2jj/wuko/step"
	processstep "github.com/up2jj/wuko/steps/process"
	"github.com/up2jj/wuko/workflow"
)

type touchFileRunner struct{ path string }

func (runner touchFileRunner) Run(context.Context, step.Request) (step.Result, error) {
	file, err := os.Create(runner.path)
	if err != nil {
		return step.Result{}, err
	}
	return step.Result{}, file.Close()
}

type fileExistsRunner struct{ path string }

func (runner fileExistsRunner) Run(context.Context, step.Request) (step.Result, error) {
	if _, err := os.Stat(runner.path); err != nil {
		return step.Result{}, fmt.Errorf("expected lifecycle marker %q: %w", runner.path, err)
	}
	return step.Result{}, nil
}

func TestProcessIsActiveForFollowingStepsAndStopsBeforeFinally(t *testing.T) {
	directory := t.TempDir()
	started := filepath.Join(directory, "started")
	stopped := filepath.Join(directory, "stopped")
	registry := step.NewRegistry()
	if err := processstep.Register(registry); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register("file_exists", func(raw map[string]any) (step.Runner, error) {
		path, _ := raw["path"].(string)
		return fileExistsRunner{path: path}, nil
	}); err != nil {
		t.Fatal(err)
	}
	definition := &workflow.Definition{Version: 1, Name: "process-lifecycle", Dir: directory, Steps: []workflow.Step{
		{ID: "service", Type: "process", With: map[string]any{
			"script": "touch \"$1\"; trap 'exit 0' TERM INT; printf 'ready\\n'; while :; do sleep 1; done",
			"args":   []any{started, stopped}, "readiness": map[string]any{"log": map[string]any{"pattern": "ready", "timeout": "2s"}},
			"shutdown": map[string]any{"timeout": "500ms", "command": map[string]any{"command": "touch", "args": []any{stopped}}},
		}},
		{ID: "during", Type: "file_exists", With: map[string]any{"path": started}},
	}, Finally: []workflow.Step{{ID: "after", Type: "file_exists", With: map[string]any{"path": stopped}}}}
	state, err := New(registry).Run(t.Context(), definition, Options{RunDir: directory, Stdout: io.Discard, Stderr: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if state.Steps["service"].(map[string]any)["ready"] != true {
		t.Fatalf("service outputs = %#v", state.Steps["service"])
	}
}

func TestProcessStartupFailureIsCaughtWithoutCancelingTheRun(t *testing.T) {
	directory := t.TempDir()
	rescued := filepath.Join(directory, "rescued")
	registry := step.NewRegistry()
	if err := processstep.Register(registry); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register("touch_file", func(raw map[string]any) (step.Runner, error) {
		path, _ := raw["path"].(string)
		return touchFileRunner{path: path}, nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register("file_exists", func(raw map[string]any) (step.Runner, error) {
		path, _ := raw["path"].(string)
		return fileExistsRunner{path: path}, nil
	}); err != nil {
		t.Fatal(err)
	}
	definition := &workflow.Definition{Version: 1, Name: "process-startup-failure", Dir: directory, Steps: []workflow.Step{
		{ID: "guarded",
			Try: &workflow.TryBlock{Steps: []workflow.Step{{ID: "service", Type: "process", With: map[string]any{
				"script":    "trap 'exit 0' TERM INT; while :; do sleep 1; done",
				"readiness": map[string]any{"log": map[string]any{"pattern": "never matches", "timeout": "200ms"}},
				"shutdown":  map[string]any{"timeout": "200ms"},
				// A service that never starts must not end its scope, or the catch branch and
				// every step after it would be canceled and the run would report success.
				"exit_on_end": true, "exit_on_failure": true,
			}}}},
			Catch: &workflow.CatchBlock{Steps: []workflow.Step{{ID: "rescue", Type: "touch_file", With: map[string]any{"path": rescued}}}}},
		{ID: "after", Type: "file_exists", With: map[string]any{"path": rescued}},
	}}
	if _, err := New(registry).Run(t.Context(), definition, Options{RunDir: directory, Stdout: io.Discard, Stderr: io.Discard}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(rescued); err != nil {
		t.Fatalf("catch branch did not run: %v", err)
	}
}

func TestProcessArgvExpressionReferencesAreCheckedBeforeTheRunStarts(t *testing.T) {
	directory := t.TempDir()
	registry := step.NewRegistry()
	if err := processstep.Register(registry); err != nil {
		t.Fatal(err)
	}
	definition := &workflow.Definition{Version: 1, Name: "process-argv", Dir: directory, Steps: []workflow.Step{
		{ID: "service", Type: "process", With: map[string]any{"argv": map[string]any{"expr": "steps.nosuchstep.argv"}}},
	}}
	_, err := New(registry).Run(t.Context(), definition, Options{RunDir: directory, Stdout: io.Discard, Stderr: io.Discard})
	if err == nil || !strings.Contains(err.Error(), `step "nosuchstep" is not available here`) {
		t.Fatalf("error = %v, want the unknown argv reference", err)
	}
}
