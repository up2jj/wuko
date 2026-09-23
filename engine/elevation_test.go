package engine

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/up2jj/wuko/process"
	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/steps/shell"
	"github.com/up2jj/wuko/workflow"
)

type elevatedRecordingExecutor struct {
	runs    int
	options process.Options
}

func (executor *elevatedRecordingExecutor) Run(_ context.Context, options process.Options) (process.Result, error) {
	executor.runs++
	executor.options = options
	return process.Result{Stdout: "root"}, nil
}

func elevationTestEngine(t *testing.T, executor process.Executor) *Engine {
	t.Helper()
	registry := step.NewRegistry()
	if err := shell.Register(registry); err != nil {
		t.Fatal(err)
	}
	return New(registry, WithElevatedExecutor(executor))
}

func TestEngineRunsElevatedShellOnlyForAllowedLocalProvenance(t *testing.T) {
	executor := &elevatedRecordingExecutor{}
	workflowDefinition := testDefinition(t, "local", workflow.Step{ID: "root", Type: "shell", With: map[string]any{"command": "id", "elevated": true}})
	engine := elevationTestEngine(t, executor)
	options := Options{RunDir: t.TempDir(), ElevationAllowed: true, Stdout: io.Discard, Stderr: io.Discard}
	if _, err := engine.Run(t.Context(), workflowDefinition, options); err != nil {
		t.Fatal(err)
	}
	if executor.runs != 1 || executor.options.Command != "id" {
		t.Fatalf("runs = %d, options = %#v", executor.runs, executor.options)
	}

	options.ElevationAllowed = false
	err := engine.Validate(t.Context(), workflowDefinition, options)
	if err == nil || !strings.Contains(err.Error(), "trusted local") {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestEngineRestrictsElevationAcrossActionProvenance(t *testing.T) {
	executor := &elevatedRecordingExecutor{}
	action := testAction(t, "action", workflow.Step{ID: "root", Type: "shell", With: map[string]any{"command": "id", "elevated": true}})
	engine := elevationTestEngine(t, executor)
	options := Options{RunDir: t.TempDir(), ElevationAllowed: true, Stdout: io.Discard, Stderr: io.Discard}

	remote := testDefinition(t, "caller", workflow.Step{ID: "action", Uses: workflow.ActionSource{URL: "https://example.test/action"}, Action: action})
	err := engine.Validate(t.Context(), remote, options)
	if err == nil || !strings.Contains(err.Error(), "trusted local") {
		t.Fatalf("remote action validation error = %v", err)
	}

	local := testDefinition(t, "caller", workflow.Step{ID: "action", Uses: workflow.ActionSource{Path: "./action"}, Action: action})
	if err := engine.Validate(t.Context(), local, options); err != nil {
		t.Fatalf("local action validation error = %v", err)
	}
}
