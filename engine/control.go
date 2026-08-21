package engine

import (
	"context"
	"fmt"
	"maps"
	"time"

	controlpkg "github.com/up2jj/wuko/control"
	"github.com/up2jj/wuko/diagnostic"
	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/workflow"
)

type controlExecution struct {
	state *State
	stats RunStats
}

type controlExpression struct {
	label string
	value string
}

func (e *Engine) validateControl(ctx context.Context, definition *workflow.Definition, workflowStep workflow.Step, options Options, state *State) error {
	if workflowStep.If != "" {
		if _, err := compileCondition(workflowStep.If); err != nil {
			return fmt.Errorf("if: %w", err)
		}
	}
	kind, children, expressions, maxConcurrency, _, _, err := controlDeclaration(workflowStep)
	if err != nil {
		return err
	}
	for _, expression := range expressions {
		if err := controlpkg.ValidateExpression(expression.value); err != nil {
			return fmt.Errorf("%s %s: %w", kind, expression.label, err)
		}
	}
	childState := cloneState(state)
	childState.Bindings = validationBindings(workflowStep)
	childOptions := options
	childOptions.depth += 2
	childOptions.Interactive = childOptions.Interactive && maxConcurrency == 1
	if !childOptions.Interactive {
		childOptions.Stdin = nil
	}
	if err := e.validateSteps(ctx, definition, children, childOptions, childState, false); err != nil {
		return fmt.Errorf("%s body: %w", kind, err)
	}
	return nil
}

func (e *Engine) executeControl(ctx context.Context, definition *workflow.Definition, workflowStep workflow.Step, options Options, state *State, index, total int) stepOutcome {
	kind, children, _, maxConcurrency, timeout, failFast, declarationErr := controlDeclaration(workflowStep)
	startedAt := time.Now()
	outcome := stepOutcome{started: true}
	finish := func(status ExecutionStatus, err error, iterations []IterationStats, nested RunStats) {
		outcome.stats = StepStats{
			ID: workflowStep.ID, Type: kind, Index: index, Status: status,
			StartedAt: startedAt, Duration: time.Since(startedAt), Error: err, Iterations: iterations,
		}
		outcome.nested = &nested
		reportStepFinished(options, definition.Name, workflowStep.ID, kind, index, total, outcome.stats)
	}

	conditionStarted := time.Now()
	traceStep(options, definition, workflowStep, diagnostic.PhaseCondition, diagnostic.StatusStarted, time.Time{}, string(workflowStep.If), nil)
	run, err := evaluateCondition(workflowStep.If, makeConditionEnvironment(definition, options.RunDir, state))
	if err != nil {
		traceStep(options, definition, workflowStep, diagnostic.PhaseCondition, diagnostic.StatusFailed, conditionStarted, "", err)
		stepErr := fmt.Errorf("workflow %q step %q (%s): evaluating if: %w", definition.Name, workflowStep.ID, kind, err)
		finish(StatusFailed, stepErr, nil, RunStats{})
		outcome.err = stepErr
		return outcome
	}
	if !run {
		traceStep(options, definition, workflowStep, diagnostic.PhaseCondition, diagnostic.StatusSkipped, conditionStarted, "condition evaluated false", nil)
		finish(StatusSkipped, nil, nil, RunStats{})
		outcome.skipped = true
		return outcome
	}
	traceStep(options, definition, workflowStep, diagnostic.PhaseCondition, diagnostic.StatusSucceeded, conditionStarted, "condition evaluated true", nil)
	report(options, ProgressEvent{
		Kind: StepStarted, Status: StatusRunning, Time: startedAt, WorkflowName: definition.Name,
		Depth: options.depth, StepID: workflowStep.ID, StepType: kind, Index: index, Total: total, Timeout: timeout,
	})
	if declarationErr != nil {
		finish(StatusFailed, declarationErr, nil, RunStats{})
		outcome.err = declarationErr
		return outcome
	}

	expansionStarted := time.Now()
	traceStep(options, definition, workflowStep, diagnostic.PhaseControl, diagnostic.StatusStarted, time.Time{}, "expanding "+kind, nil)
	iterations, err := expandControl(workflowStep, templateData(definition, options.RunDir, state))
	if err != nil {
		traceStep(options, definition, workflowStep, diagnostic.PhaseControl, diagnostic.StatusFailed, expansionStarted, "expanding "+kind, err)
		stepErr := fmt.Errorf("workflow %q step %q (%s): expanding: %w", definition.Name, workflowStep.ID, kind, err)
		finish(StatusFailed, stepErr, nil, RunStats{})
		outcome.err = stepErr
		return outcome
	}
	traceStep(options, definition, workflowStep, diagnostic.PhaseControl, diagnostic.StatusSucceeded, expansionStarted, "expanded "+kind, nil, diagnostic.Attr("iterations", fmt.Sprint(len(iterations))))
	report(options, ProgressEvent{
		Kind: ControlStarted, Status: StatusRunning, Time: time.Now(), WorkflowName: definition.Name,
		Depth: options.depth, StepID: workflowStep.ID, ControlKind: kind, Iterations: len(iterations),
		MaxConcurrency: maxConcurrency, FailFast: failFast, Timeout: timeout,
	})

	childOptions := options
	childOptions.depth += 2
	childOptions.Interactive = childOptions.Interactive && maxConcurrency == 1
	if !childOptions.Interactive {
		childOptions.Stdin = nil
	}
	policy := controlpkg.Policy{MaxConcurrency: maxConcurrency, Timeout: timeout, FailFast: failFast}
	observer := func(event controlpkg.Event) {
		progressKind := IterationStarted
		status := StatusRunning
		if event.Kind == controlpkg.IterationFinished {
			progressKind = IterationFinished
			status = statusFromError(event.Err)
		}
		report(options, ProgressEvent{
			Kind: progressKind, Status: status, Time: time.Now(), WorkflowName: definition.Name,
			Depth: options.depth + 1, StepID: workflowStep.ID, ControlKind: kind,
			Iteration: event.Index, Iterations: len(iterations), Duration: event.Duration, Error: event.Err,
		})
	}
	outcomes, runErr := controlpkg.Run(ctx, iterations, policy, observer, func(iterationCtx context.Context, iteration controlpkg.Iteration) (controlExecution, error) {
		iterationState := cloneState(state)
		iterationState.Bindings = cloneMap(iteration.Bindings)
		bodyTotal := leafStepCount(children)
		bodyStats := RunStats{StartedAt: time.Now(), Total: bodyTotal, Steps: make([]StepStats, 0, bodyTotal)}
		err := e.executeSequence(iterationCtx, definition, children, childOptions, iterationState, &bodyStats, 1, bodyTotal)
		bodyStats.FinishedAt = time.Now()
		bodyStats.Duration = bodyStats.FinishedAt.Sub(bodyStats.StartedAt)
		return controlExecution{state: iterationState, stats: bodyStats}, err
	})

	nested := RunStats{}
	iterationStats := make([]IterationStats, 0, len(outcomes))
	for _, item := range outcomes {
		if !item.Started {
			continue
		}
		stats := item.Value.stats
		rollupNestedMetrics(&nested, stats)
		iterationStats = append(iterationStats, IterationStats{
			Index: item.Iteration.Index, Status: statusFromError(item.Err), StartedAt: item.StartedAt,
			Duration: item.Duration, Error: item.Err, Steps: stats.Steps,
		})
	}
	finishedAt := time.Now()
	report(options, ProgressEvent{
		Kind: ControlFinished, Status: statusFromError(runErr), Time: finishedAt, WorkflowName: definition.Name,
		Depth: options.depth, StepID: workflowStep.ID, ControlKind: kind, Iterations: len(iterations),
		MaxConcurrency: maxConcurrency, FailFast: failFast, Timeout: timeout,
		Duration: finishedAt.Sub(startedAt), Error: runErr,
	})
	if runErr != nil {
		stepErr := fmt.Errorf("workflow %q step %q (%s): %w", definition.Name, workflowStep.ID, kind, runErr)
		finish(statusFromError(runErr), stepErr, iterationStats, nested)
		outcome.err = stepErr
		return outcome
	}

	resultRecords := make([]any, len(outcomes))
	for i, item := range outcomes {
		steps := selectedStepOutputs(item.Value.state, children)
		record := map[string]any{"index": item.Iteration.Index, "steps": steps}
		if kind == "foreach" {
			record["item"] = cloneAny(bindingRoot(item.Iteration.Bindings, "foreach")["item"])
		} else {
			record["matrix"] = cloneAny(bindingRoot(item.Iteration.Bindings, "matrix"))
		}
		resultRecords[i] = record
	}
	outcome.result = step.Result{Outputs: map[string]any{"count": len(resultRecords), "results": resultRecords}}
	finish(StatusSucceeded, nil, iterationStats, nested)
	return outcome
}

func controlDeclaration(workflowStep workflow.Step) (kind string, children []workflow.Step, expressions []controlExpression, maxConcurrency int, timeout time.Duration, failFast bool, err error) {
	switch {
	case workflowStep.Foreach != nil:
		group := workflowStep.Foreach
		if validationErr := group.Validate(); validationErr != nil {
			err = validationErr
		}
		return "foreach", group.Steps, []controlExpression{{label: "items", value: group.Items}}, group.MaxConcurrency, durationValue(group.Timeout), group.FailFast, err
	case workflowStep.Matrix != nil:
		group := workflowStep.Matrix
		if validationErr := group.Validate(); validationErr != nil {
			err = validationErr
		}
		for _, axis := range group.Axes {
			if axis.Expression != "" {
				expressions = append(expressions, controlExpression{label: "axis " + axis.Name, value: axis.Expression})
			}
		}
		return "matrix", group.Steps, expressions, group.MaxConcurrency, durationValue(group.Timeout), group.FailFast, err
	default:
		return "", nil, nil, 0, 0, false, fmt.Errorf("control declaration is missing")
	}
}

func expandControl(workflowStep workflow.Step, environment map[string]any) ([]controlpkg.Iteration, error) {
	if workflowStep.Foreach != nil {
		items, err := controlpkg.EvaluateList(workflowStep.Foreach.Items, environment)
		if err != nil {
			return nil, fmt.Errorf("items: %w", err)
		}
		return controlpkg.Foreach(items)
	}
	axes := make([]controlpkg.Axis, 0, len(workflowStep.Matrix.Axes))
	for _, declaration := range workflowStep.Matrix.Axes {
		values := declaration.Values
		if declaration.Expression != "" {
			var err error
			values, err = controlpkg.EvaluateList(declaration.Expression, environment)
			if err != nil {
				return nil, fmt.Errorf("axis %q: %w", declaration.Name, err)
			}
		}
		axes = append(axes, controlpkg.Axis{Name: declaration.Name, Values: values})
	}
	return controlpkg.Matrix(axes)
}

func validationBindings(workflowStep workflow.Step) map[string]any {
	if workflowStep.Foreach != nil {
		return map[string]any{"foreach": map[string]any{"index": 0, "item": nil}}
	}
	matrix := make(map[string]any, len(workflowStep.Matrix.Axes))
	for _, axis := range workflowStep.Matrix.Axes {
		matrix[axis.Name] = nil
	}
	return map[string]any{"matrix": matrix}
}

func durationValue(duration *workflow.Duration) time.Duration {
	if duration == nil {
		return 0
	}
	return duration.Value()
}

func selectedStepOutputs(state *State, steps []workflow.Step) map[string]any {
	result := make(map[string]any)
	for _, workflowStep := range steps {
		if workflowStep.Concurrent != nil {
			maps.Copy(result, selectedStepOutputs(state, workflowStep.Concurrent.Steps))
			continue
		}
		if value, exists := state.Steps[workflowStep.ID]; exists {
			result[workflowStep.ID] = cloneAny(value)
		}
	}
	return result
}
