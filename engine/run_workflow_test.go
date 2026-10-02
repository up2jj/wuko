package engine

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/up2jj/wuko/step"
	runworkflowstep "github.com/up2jj/wuko/steps/runworkflow"
	"github.com/up2jj/wuko/workflow"
)

type nestedWorkflowHost struct {
	registry   *step.Registry
	definition *workflow.Definition
}

func (host *nestedWorkflowHost) ValidateWorkflow(context.Context, WorkflowInvocation) error {
	return nil
}

func (host *nestedWorkflowHost) RunWorkflow(ctx context.Context, invocation WorkflowInvocation) (map[string]any, error) {
	state, err := New(host.registry, WithWorkflowInvoker(host)).Run(ctx, host.definition, invocation.EngineOptions(nil, "", ""))
	if err != nil {
		return nil, err
	}
	return state.Outputs, nil
}

func TestRunWorkflowProgressIsCorrelatedToInvokingStep(t *testing.T) {
	directory := t.TempDir()
	childPath := filepath.Join(directory, "child.yaml")
	parentPath := filepath.Join(directory, "parent.yaml")
	if err := os.WriteFile(childPath, []byte(`version: 1
name: child
outputs:
  artifact: {type: string, value: '"dist/app.tar.gz"'}
steps:
  - return: {outputs: {artifact: '"dist/app.tar.gz"'}}
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(parentPath, []byte(`version: 1
name: parent
steps:
  - {id: build, type: run_workflow, with: {workflow: child}}
`), 0o644); err != nil {
		t.Fatal(err)
	}
	loader := workflow.NewLoader(nil)
	child, err := loader.Load(t.Context(), childPath, workflow.LoadOptions{RunDir: directory})
	if err != nil {
		t.Fatal(err)
	}
	parent, err := loader.Load(t.Context(), parentPath, workflow.LoadOptions{RunDir: directory})
	if err != nil {
		t.Fatal(err)
	}
	registry := step.NewRegistry()
	if err := runworkflowstep.Register(registry); err != nil {
		t.Fatal(err)
	}
	host := &nestedWorkflowHost{registry: registry, definition: child}
	var mu sync.Mutex
	var events []ProgressEvent
	state, err := New(registry, WithWorkflowInvoker(host)).Run(t.Context(), parent, Options{
		RunDir: directory, Stdout: io.Discard, Stderr: io.Discard, ElevationAllowed: true,
		Progress: func(event ProgressEvent) {
			mu.Lock()
			events = append(events, event)
			mu.Unlock()
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := state.Steps["build"].(map[string]any)["artifact"]; got != "dist/app.tar.gz" {
		t.Fatalf("artifact = %#v", got)
	}
	var parentStart, stepStart, childStart ProgressEvent
	for _, event := range events {
		switch {
		case event.Kind == WorkflowStarted && event.WorkflowName == "parent":
			parentStart = event
		case event.Kind == StepStarted && event.WorkflowName == "parent" && event.StepID == "build":
			stepStart = event
		case event.Kind == WorkflowStarted && event.WorkflowName == "child":
			childStart = event
		}
	}
	if parentStart.RunID == "" || stepStart.StepRunID == "" {
		t.Fatalf("missing parent identities: parent=%#v step=%#v", parentStart, stepStart)
	}
	if childStart.Depth != 1 || childStart.ParentRunID != parentStart.RunID || childStart.ParentStepRunID != stepStart.StepRunID {
		t.Fatalf("child correlation = %#v, parent=%#v step=%#v", childStart, parentStart, stepStart)
	}
}

var _ WorkflowInvoker = (*nestedWorkflowHost)(nil)
