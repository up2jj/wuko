package engine

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/expr-lang/expr"
	"github.com/up2jj/wuko/diagnostic"
	wukoexpr "github.com/up2jj/wuko/expression"
	"github.com/up2jj/wuko/workflow"
)

func (e *Engine) validateReturn(workflowStep workflow.Step) error {
	if err := workflowStep.ValidateReturnControl(); err != nil {
		return err
	}
	if workflowStep.If != "" {
		if _, err := compileCondition(workflowStep.If); err != nil {
			return fmt.Errorf("if: %w", err)
		}
	}
	for name, source := range workflowStep.Return.Outputs {
		if _, err := wukoexpr.Compile(source, expr.AllowUndefinedVariables()); err != nil {
			return fmt.Errorf("output %q: %w", name, err)
		}
	}
	return nil
}

func (e *Engine) executeReturn(ctx context.Context, definition *workflow.Definition, workflowStep workflow.Step, options Options, state *State) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	started := time.Now()
	trace(options, diagnostic.Event{
		Phase: diagnostic.PhaseControl, Status: diagnostic.StatusStarted, Time: started,
		WorkflowName: definition.Name, Location: workflowStep.Location, Message: "evaluating return",
	})
	environment := makeConditionEnvironment(definition, options.RunDir, state)
	run, err := evaluateCondition(workflowStep.If, environment)
	if err != nil {
		trace(options, diagnostic.Event{Phase: diagnostic.PhaseControl, Status: diagnostic.StatusFailed, Time: time.Now(), Duration: time.Since(started), WorkflowName: definition.Name, Location: workflowStep.Location, Error: err})
		return false, fmt.Errorf("workflow %q return: evaluating if: %w", definition.Name, err)
	}
	if !run {
		trace(options, diagnostic.Event{Phase: diagnostic.PhaseControl, Status: diagnostic.StatusSkipped, Time: time.Now(), Duration: time.Since(started), WorkflowName: definition.Name, Location: workflowStep.Location, Message: "return condition evaluated false"})
		return false, nil
	}

	outputs := make(map[string]any, len(workflowStep.Return.Outputs))
	for _, name := range slices.Sorted(maps.Keys(workflowStep.Return.Outputs)) {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		value, err := wukoexpr.Eval(workflowStep.Return.Outputs[name], environment)
		if err != nil {
			trace(options, diagnostic.Event{Phase: diagnostic.PhaseControl, Status: diagnostic.StatusFailed, Time: time.Now(), Duration: time.Since(started), WorkflowName: definition.Name, Location: workflowStep.Location, Error: err})
			return false, fmt.Errorf("workflow %q return output %q: %w", definition.Name, name, err)
		}
		if !workflow.ActionDataValue(value) {
			err := fmt.Errorf("output %q is not a YAML/JSON-compatible value", name)
			trace(options, diagnostic.Event{Phase: diagnostic.PhaseControl, Status: diagnostic.StatusFailed, Time: time.Now(), Duration: time.Since(started), WorkflowName: definition.Name, Location: workflowStep.Location, Error: err})
			return false, fmt.Errorf("workflow %q return: %w", definition.Name, err)
		}
		outputs[name] = cloneAny(value)
	}
	state.Outputs = outputs
	state.returning = true
	state.didReturn = true
	trace(options, diagnostic.Event{Phase: diagnostic.PhaseControl, Status: diagnostic.StatusSucceeded, Time: time.Now(), Duration: time.Since(started), WorkflowName: definition.Name, Location: workflowStep.Location, Message: "workflow returned successfully", Attributes: []diagnostic.Attribute{diagnostic.Attr("outputs", fmt.Sprint(len(outputs)))}})
	return true, nil
}

func containsReturn(steps []workflow.Step) bool {
	for _, workflowStep := range steps {
		if workflowStep.Return != nil {
			return true
		}
		if workflowStep.IsWorkingDirectoryBlock() || workflowStep.IsConditionalBlock() {
			if containsReturn(workflowStep.Steps) {
				return true
			}
		}
		if workflowStep.Concurrent != nil && containsReturn(workflowStep.Concurrent.Steps) {
			return true
		}
		if workflowStep.Foreach != nil && containsReturn(workflowStep.Foreach.Steps) {
			return true
		}
		if workflowStep.Matrix != nil && containsReturn(workflowStep.Matrix.Steps) {
			return true
		}
	}
	return false
}
