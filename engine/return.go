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
	if workflowStep.If != "" {
		if _, err := e.compileCondition(workflowStep.If); err != nil {
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

func validateWorkflowOutputExpressions(definition *workflow.Definition) error {
	for name, output := range definition.Outputs {
		if _, err := wukoexpr.Compile(output.Value, expr.AllowUndefinedVariables()); err != nil {
			return fmt.Errorf("workflow output %q: %w", name, err)
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
	run, err := e.evaluateCondition(workflowStep.If, environment)
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
	if err := validateWorkflowOutputValues(definition.Outputs, outputs); err != nil {
		trace(options, diagnostic.Event{Phase: diagnostic.PhaseControl, Status: diagnostic.StatusFailed, Time: time.Now(), Duration: time.Since(started), WorkflowName: definition.Name, Location: workflowStep.Location, Error: err})
		return false, fmt.Errorf("workflow %q return: %w", definition.Name, err)
	}
	state.Outputs = outputs
	state.returning = true
	state.didReturn = true
	trace(options, diagnostic.Event{Phase: diagnostic.PhaseControl, Status: diagnostic.StatusSucceeded, Time: time.Now(), Duration: time.Since(started), WorkflowName: definition.Name, Location: workflowStep.Location, Message: "workflow returned successfully", Attributes: []diagnostic.Attribute{diagnostic.Attr("outputs", fmt.Sprint(len(outputs)))}})
	return true, nil
}

func (e *Engine) finishWorkflowOutputs(definition *workflow.Definition, options Options, state *State) error {
	if len(definition.Outputs) == 0 {
		return nil
	}
	if state.didReturn {
		return validateWorkflowOutputValues(definition.Outputs, state.Outputs)
	}
	environment := makeConditionEnvironment(definition, options.RunDir, state)
	outputs := make(map[string]any, len(definition.Outputs))
	for _, name := range slices.Sorted(maps.Keys(definition.Outputs)) {
		declaration := definition.Outputs[name]
		value, err := wukoexpr.Eval(declaration.Value, environment)
		if err != nil {
			return fmt.Errorf("workflow %q output %q: evaluating value: %w", definition.Name, name, err)
		}
		outputs[name] = value
	}
	if err := validateWorkflowOutputValues(definition.Outputs, outputs); err != nil {
		return fmt.Errorf("workflow %q: %w", definition.Name, err)
	}
	state.Outputs = cloneMap(outputs)
	return nil
}

func validateWorkflowOutputValues(contract map[string]workflow.WorkflowOutput, outputs map[string]any) error {
	if len(contract) == 0 {
		return nil
	}
	for name, declaration := range contract {
		value, exists := outputs[name]
		if !exists {
			return fmt.Errorf("workflow output %q is missing", name)
		}
		if !workflow.OutputValueMatches(declaration.Type, value) {
			return fmt.Errorf("workflow output %q value does not match type %s", name, declaration.Type)
		}
		if !workflow.ActionDataValue(value) {
			return fmt.Errorf("workflow output %q is not a YAML/JSON-compatible value", name)
		}
	}
	return nil
}
