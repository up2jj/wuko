package engine

import (
	"context"
	"fmt"
	"io"
	"maps"

	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/workflow"
)

// boundInlineWorkflowRunner executes callbacks from the immutable state captured when the owning
// step request was built. A fresh clone and lifecycle are used for every call.
type boundInlineWorkflowRunner struct {
	engine      *Engine
	definition  *workflow.Definition
	options     Options
	state       *State
	operationID string
}

func (runner boundInlineWorkflowRunner) ValidateInlineWorkflow(ctx context.Context, call step.InlineWorkflowCall) error {
	definition, options, err := runner.prepare(call)
	if err != nil {
		return err
	}
	// Callbacks are validated before the enclosing workflow runs, so the captured state does
	// not yet hold earlier step results or variables those steps write. Expose every parent
	// step ID and leave variables open; the real state is checked again on each run.
	parentIDs := parentStepIDs(runner.definition)
	parent := make(map[string]struct{}, len(parentIDs))
	for _, id := range parentIDs {
		parent[id] = struct{}{}
	}
	if err := rejectShadowedSteps(call, parent); err != nil {
		return err
	}
	for _, id := range parentIDs {
		if _, exists := options.initialState.Steps[id]; !exists {
			options.initialState.Steps[id] = nil
		}
	}
	options.openParentReferences = true
	return runner.engine.Validate(ctx, definition, options)
}

func parentStepIDs(definition *workflow.Definition) []string {
	var ids []string
	var collect func([]workflow.Step)
	collect = func(steps []workflow.Step) {
		for _, declaration := range steps {
			if declaration.ID != "" {
				ids = append(ids, declaration.ID)
			}
			for _, sequence := range declaration.ChildSequences() {
				collect(sequence.Steps)
			}
		}
	}
	collect(definition.Steps)
	collect(definition.Finally)
	return ids
}

// rejectShadowedSteps reports a callback step whose ID would replace a parent step result.
func rejectShadowedSteps(call step.InlineWorkflowCall, parent map[string]struct{}) error {
	var check func([]workflow.Step) error
	check = func(steps []workflow.Step) error {
		for _, declaration := range steps {
			if _, exists := parent[declaration.ID]; declaration.ID != "" && exists {
				return fmt.Errorf("step id %q shadows a workflow step", declaration.ID)
			}
			for _, sequence := range declaration.ChildSequences() {
				if err := check(sequence.Steps); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := check(call.Steps); err != nil {
		return err
	}
	return check(call.Finally)
}

func (runner boundInlineWorkflowRunner) RunInlineWorkflow(ctx context.Context, call step.InlineWorkflowCall) (map[string]any, error) {
	definition, options, err := runner.prepare(call)
	if err != nil {
		return nil, err
	}
	ran := make(map[string]struct{}, len(runner.state.Steps))
	for id := range runner.state.Steps {
		ran[id] = struct{}{}
	}
	if err := rejectShadowedSteps(call, ran); err != nil {
		return nil, err
	}
	state, err := runner.engine.Run(ctx, definition, options)
	if err != nil {
		return nil, err
	}
	return workflow.CloneMap(state.Outputs), nil
}

func (runner boundInlineWorkflowRunner) prepare(call step.InlineWorkflowCall) (*workflow.Definition, Options, error) {
	if runner.engine == nil || runner.definition == nil || runner.state == nil {
		return nil, Options{}, fmt.Errorf("inline workflow execution is unavailable")
	}
	if len(call.Steps) == 0 {
		return nil, Options{}, fmt.Errorf("inline workflow steps are required")
	}
	name := call.Name
	if name == "" {
		name = "callback"
	}
	steps := append([]workflow.Step(nil), call.Steps...)
	if len(call.Outputs) > 0 {
		// A return step requires outputs; callbacks without declared outputs return nothing.
		steps = append(steps, workflow.Step{Return: &workflow.ReturnControl{Outputs: maps.Clone(call.Outputs)}})
	}
	scoped := *runner.definition
	scoped.Name = runner.definition.Name + " " + name
	scoped.Steps = steps
	scoped.Finally = append([]workflow.Step(nil), call.Finally...)
	scoped.Install = nil
	scoped.Uninstall = nil
	scoped.Targets = nil
	scoped.Outputs = nil
	scoped.Cron = ""

	initial := cloneState(runner.state)
	if initial.Steps == nil {
		initial.Steps = make(map[string]any)
	}
	if initial.Bindings == nil {
		initial.Bindings = make(map[string]any)
	}
	for key, value := range call.Bindings {
		initial.Bindings[key] = cloneAny(value)
	}
	options := runner.options
	options.Stdin = nil
	options.Stdout = io.Discard
	options.Stderr = io.Discard
	options.Interactive = false
	options.DryRun = false
	options.Progress = nil
	options.Diagnostics = nil
	options.OnReturn = nil
	options.runtime = nil
	options.defers = nil
	options.services = nil
	options.runID = ""
	options.stepRunID = ""
	options.parentRunID = ""
	options.parentStepRunID = ""
	options.operationPrefix = runner.operationID + "/" + name
	options.operationID = ""
	options.previousPass = nil
	options.passFailure = nil
	options.cleanups = nil
	options.onceClaims = nil
	options.initialState = initial
	return &scoped, options, nil
}

var _ step.InlineWorkflowRunner = boundInlineWorkflowRunner{}
