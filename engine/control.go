package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"reflect"
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
	kind, children, expressions, maxConcurrency, _, _, _, err := controlDeclaration(workflowStep)
	if err != nil {
		return err
	}
	for _, expression := range expressions {
		if err := controlpkg.ValidateExpression(expression.value); err != nil {
			return fmt.Errorf("%s %s: %w", kind, expression.label, err)
		}
	}
	if collect := controlCollect(workflowStep); collect != "" {
		if err := controlpkg.ValidateExpression(collect); err != nil {
			return fmt.Errorf("%s collect: %w", kind, err)
		}
	}
	childState := cloneState(state)
	if childState.Bindings == nil {
		childState.Bindings = make(map[string]any)
	}
	maps.Copy(childState.Bindings, validationBindings(workflowStep))
	childOptions := options
	childOptions.depth += 2
	childOptions.Interactive = childOptions.Interactive && maxConcurrency == 1
	if !childOptions.Interactive {
		childOptions.Stdin = nil
	}
	if err := e.validateSteps(ctx, definition, children, childOptions, childState); err != nil {
		return fmt.Errorf("%s body: %w", kind, err)
	}
	return nil
}

func (e *Engine) executeControl(ctx context.Context, definition *workflow.Definition, workflowStep workflow.Step, options Options, state *State, index, total int) stepOutcome {
	if err := ctx.Err(); err != nil {
		return stepOutcome{err: err}
	}
	kind, children, _, maxConcurrency, maxIterations, timeout, failFast, declarationErr := controlDeclaration(workflowStep)
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
	iterations, err := expandControl(ctx, workflowStep, templateData(definition, options.RunDir, state), maxIterations)
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
		if iterationState.Bindings == nil {
			iterationState.Bindings = make(map[string]any)
		}
		maps.Copy(iterationState.Bindings, cloneMap(iteration.Bindings))
		bodyTotal := leafStepCount(children)
		bodyStats := RunStats{StartedAt: time.Now(), Total: bodyTotal, Steps: make([]StepStats, 0, bodyTotal)}
		err := e.executeSequence(iterationCtx, definition, children, childOptions, iterationState, &bodyStats, 1, bodyTotal)
		bodyStats.FinishedAt = time.Now()
		bodyStats.Duration = bodyStats.FinishedAt.Sub(bodyStats.StartedAt)
		return controlExecution{state: iterationState, stats: bodyStats}, err
	})

	nested := RunStats{}
	iterationStats := make([]IterationStats, 0, len(outcomes))
	startedIterations := 0
	succeededIterations := 0
	for _, item := range outcomes {
		if !item.Started {
			continue
		}
		startedIterations++
		if item.Err == nil {
			succeededIterations++
		}
		stats := item.Value.stats
		rollupNestedMetrics(&nested, stats)
		iterationStats = append(iterationStats, IterationStats{
			Index: item.Iteration.Index, Status: statusFromError(item.Err), StartedAt: item.StartedAt,
			Duration: item.Duration, Error: item.Err, Steps: stats.Steps,
		})
	}
	if runErr != nil {
		stepErr := fmt.Errorf("workflow %q step %q (%s): %w", definition.Name, workflowStep.ID, kind, runErr)
		reportControlFinished(options, definition, workflowStep, kind, iterations, maxConcurrency, failFast, timeout, startedAt, startedIterations, succeededIterations, stepErr)
		finish(statusFromError(runErr), stepErr, iterationStats, nested)
		outcome.err = stepErr
		return outcome
	}

	outputs := map[string]any{"count": len(iterations)}
	if collect := controlCollect(workflowStep); collect != "" {
		collectionStarted := time.Now()
		traceStep(options, definition, workflowStep, diagnostic.PhaseControl, diagnostic.StatusStarted, time.Time{}, "collecting "+kind+" results", nil)
		results, err := collectControlResults(ctx, definition, options.RunDir, collect, outcomes)
		if err != nil {
			traceStep(options, definition, workflowStep, diagnostic.PhaseControl, diagnostic.StatusFailed, collectionStarted, "collecting "+kind+" results", err)
			stepErr := fmt.Errorf("workflow %q step %q (%s): %w", definition.Name, workflowStep.ID, kind, err)
			reportControlFinished(options, definition, workflowStep, kind, iterations, maxConcurrency, failFast, timeout, startedAt, startedIterations, succeededIterations, stepErr)
			finish(statusFromError(stepErr), stepErr, iterationStats, nested)
			outcome.err = stepErr
			return outcome
		}
		traceStep(options, definition, workflowStep, diagnostic.PhaseControl, diagnostic.StatusSucceeded, collectionStarted, "collected "+kind+" results", nil, diagnostic.Attr("results", fmt.Sprint(len(results))))
		outputs["results"] = results
	}
	reportControlFinished(options, definition, workflowStep, kind, iterations, maxConcurrency, failFast, timeout, startedAt, startedIterations, succeededIterations, nil)
	outcome.result = step.Result{Outputs: outputs}
	finish(StatusSucceeded, nil, iterationStats, nested)
	return outcome
}

func collectControlResults(ctx context.Context, definition *workflow.Definition, runDir, expression string, outcomes []controlpkg.Outcome[controlExecution]) ([]any, error) {
	results := make([]any, len(outcomes))
	for i, item := range outcomes {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("collect iteration %d: %w", item.Iteration.Index, err)
		}
		value, err := controlpkg.EvaluateExpression(expression, templateData(definition, runDir, item.Value.state))
		if err != nil {
			return nil, fmt.Errorf("collect iteration %d: %w", item.Iteration.Index, err)
		}
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("collect iteration %d: %w", item.Iteration.Index, err)
		}
		if !workflow.ActionDataValue(value) {
			return nil, fmt.Errorf("collect iteration %d: expression returned %T, want YAML/JSON-compatible value", item.Iteration.Index, value)
		}
		results[i] = cloneAny(value)
	}
	return results, nil
}

func reportControlFinished(options Options, definition *workflow.Definition, workflowStep workflow.Step, kind string, iterations []controlpkg.Iteration, maxConcurrency int, failFast bool, timeout time.Duration, startedAt time.Time, startedIterations, succeededIterations int, err error) {
	finishedAt := time.Now()
	report(options, ProgressEvent{
		Kind: ControlFinished, Status: statusFromError(err), Time: finishedAt, WorkflowName: definition.Name,
		Depth: options.depth, StepID: workflowStep.ID, ControlKind: kind, Iterations: len(iterations),
		MaxConcurrency: maxConcurrency, FailFast: failFast, Timeout: timeout,
		Started: startedIterations, Succeeded: succeededIterations,
		Duration: finishedAt.Sub(startedAt), Error: err,
	})
}

func controlCollect(workflowStep workflow.Step) string {
	if workflowStep.Batch != nil {
		return workflowStep.Batch.Collect
	}
	if workflowStep.Foreach != nil {
		return workflowStep.Foreach.Collect
	}
	if workflowStep.Matrix != nil {
		return workflowStep.Matrix.Collect
	}
	return ""
}

func controlDeclaration(workflowStep workflow.Step) (kind string, children []workflow.Step, expressions []controlExpression, maxConcurrency, maxIterations int, timeout time.Duration, failFast bool, err error) {
	switch {
	case workflowStep.Batch != nil:
		group := workflowStep.Batch
		expressions = append(expressions, controlExpression{label: "items", value: group.Items})
		if group.Size.Expression != "" {
			expressions = append(expressions, controlExpression{label: "size", value: group.Size.Expression})
		}
		return "batch", group.Steps, expressions, group.MaxConcurrency, effectiveMaxIterations(group.MaxIterations), durationValue(group.Timeout), group.FailFast, err
	case workflowStep.Foreach != nil:
		group := workflowStep.Foreach
		return "foreach", group.Steps, []controlExpression{{label: "items", value: group.Items}}, group.MaxConcurrency, effectiveMaxIterations(group.MaxIterations), durationValue(group.Timeout), group.FailFast, err
	case workflowStep.Matrix != nil:
		group := workflowStep.Matrix
		for _, axis := range group.Axes {
			if axis.Expression != "" {
				expressions = append(expressions, controlExpression{label: "axis " + axis.Name, value: axis.Expression})
			}
		}
		return "matrix", group.Steps, expressions, group.MaxConcurrency, effectiveMaxIterations(group.MaxIterations), durationValue(group.Timeout), group.FailFast, err
	default:
		return "", nil, nil, 0, 0, 0, false, fmt.Errorf("control declaration is missing")
	}
}

func expandControl(ctx context.Context, workflowStep workflow.Step, environment map[string]any, maxIterations int) ([]controlpkg.Iteration, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if workflowStep.Batch != nil {
		items, err := controlpkg.EvaluateList(workflowStep.Batch.Items, environment)
		if err != nil {
			return nil, fmt.Errorf("items: %w", err)
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		size, err := evaluateBatchSize(workflowStep.Batch.Size, environment)
		if err != nil {
			return nil, fmt.Errorf("size: %w", err)
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return controlpkg.BatchContext(ctx, items, size, maxIterations)
	}
	if workflowStep.Foreach != nil {
		items, err := controlpkg.EvaluateList(workflowStep.Foreach.Items, environment)
		if err != nil {
			return nil, fmt.Errorf("items: %w", err)
		}
		return controlpkg.ForeachContext(ctx, items, maxIterations)
	}
	axes := make([]controlpkg.Axis, 0, len(workflowStep.Matrix.Axes))
	for _, declaration := range workflowStep.Matrix.Axes {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		values := declaration.Values
		if declaration.Expression != "" {
			var err error
			values, err = controlpkg.EvaluateList(declaration.Expression, environment)
			if err != nil {
				return nil, fmt.Errorf("axis %q: %w", declaration.Name, err)
			}
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		axes = append(axes, controlpkg.Axis{Name: declaration.Name, Values: values})
	}
	return controlpkg.MatrixContext(ctx, axes, maxIterations)
}

func effectiveMaxIterations(value int) int {
	if value == 0 {
		return controlpkg.DefaultMaxIterations
	}
	return value
}

func validationBindings(workflowStep workflow.Step) map[string]any {
	if workflowStep.Batch != nil {
		return map[string]any{"batch": map[string]any{"index": 0, "items": []any{}}}
	}
	if workflowStep.Foreach != nil {
		return map[string]any{"foreach": map[string]any{"index": 0, "item": nil}}
	}
	matrix := make(map[string]any, len(workflowStep.Matrix.Axes))
	for _, axis := range workflowStep.Matrix.Axes {
		matrix[axis.Name] = nil
	}
	return map[string]any{"matrix": matrix}
}

func evaluateBatchSize(size workflow.BatchSize, environment map[string]any) (int, error) {
	if size.Literal != 0 {
		return size.Literal, nil
	}
	value, err := controlpkg.EvaluateExpression(size.Expression, environment)
	if err != nil {
		return 0, err
	}
	return positiveInteger(value)
}

func positiveInteger(value any) (int, error) {
	if number, ok := value.(json.Number); ok {
		if integer, err := number.Int64(); err == nil {
			return positiveInteger(integer)
		}
		floating, err := number.Float64()
		if err != nil {
			return 0, fmt.Errorf("expression returned invalid number %q", number)
		}
		return positiveInteger(floating)
	}
	reflected := reflect.ValueOf(value)
	if !reflected.IsValid() {
		return 0, fmt.Errorf("expression returned nil, want positive integer")
	}
	maxInt := uint64(^uint(0) >> 1)
	switch reflected.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		integer := reflected.Int()
		if integer > 0 && uint64(integer) <= maxInt {
			return int(integer), nil
		}
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		integer := reflected.Uint()
		if integer > 0 && integer <= maxInt {
			return int(integer), nil
		}
	case reflect.Float32, reflect.Float64:
		floating := reflected.Float()
		if floating > 0 && !math.IsInf(floating, 0) && !math.IsNaN(floating) && math.Trunc(floating) == floating && floating <= float64(maxInt) {
			integer := int(floating)
			if integer > 0 && float64(integer) == floating {
				return integer, nil
			}
		}
	}
	return 0, fmt.Errorf("expression returned %T, want positive integer", value)
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
		if children, transparent := transparentChildSequences(workflowStep); transparent {
			for _, child := range children {
				maps.Copy(result, selectedStepOutputs(state, child.Steps))
			}
			continue
		}
		if value, exists := state.Steps[workflowStep.ID]; exists {
			result[workflowStep.ID] = cloneAny(value)
		}
	}
	return result
}
