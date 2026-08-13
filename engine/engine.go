package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"strings"
	"time"

	"github.com/expr-lang/expr"
	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/workflow"
)

type Engine struct {
	registry *step.Registry
}

type Options struct {
	Vars map[string]any
	Env  map[string]string
	// BaseEnv overrides the current process environment when non-nil.
	BaseEnv     map[string]string
	RunDir      string
	Stdin       io.Reader
	Stdout      io.Writer
	Stderr      io.Writer
	Interactive bool
	DryRun      bool
	// Progress receives structured workflow, step, attempt, retry, and timing events.
	Progress        func(ProgressEvent)
	inputs          map[string]any
	operationPrefix string
	depth           int
}

type State struct {
	Inputs map[string]any
	Vars   map[string]any
	Env    map[string]string
	Steps  map[string]any
	Stats  RunStats
}

func New(registry *step.Registry) *Engine { return &Engine{registry: registry} }

func (e *Engine) Validate(ctx context.Context, definition *workflow.Definition, options Options) error {
	state, err := initialState(definition, options)
	if err != nil {
		return err
	}
	for _, workflowStep := range definition.Steps {
		if err := workflowStep.ValidateExecutionPolicy(); err != nil {
			return fmt.Errorf("step %q: %w", workflowStep.ID, err)
		}
		if workflowStep.If != "" {
			if _, err := compileCondition(workflowStep.If); err != nil {
				return fmt.Errorf("step %q if: %w", workflowStep.ID, err)
			}
		}
		if workflowStep.Retry != nil && workflowStep.Retry.OperationID != "" {
			if err := validateTemplates(workflowStep.Retry.OperationID, false); err != nil {
				return fmt.Errorf("step %q retry operation_id: %w", workflowStep.ID, err)
			}
		}
		if workflowStep.Action != nil {
			if err := e.validateAction(ctx, definition, workflowStep, options, state); err != nil {
				return fmt.Errorf("step %q: %w", workflowStep.ID, err)
			}
			continue
		}
		if err := validateTemplates(workflowStep.With, workflowStep.Type == "lua"); err != nil {
			return fmt.Errorf("step %q template: %w", workflowStep.ID, err)
		}
		runner, err := e.registry.Build(workflowStep.Type, workflowStep.With)
		if err != nil {
			return fmt.Errorf("step %q: %w", workflowStep.ID, err)
		}
		validator, ok := runner.(step.Validator)
		if !ok {
			continue
		}
		request := makeRequest(definition, workflowStep.ID, options, state, 1, maxAttempts(workflowStep), "validation")
		if err := validator.Validate(ctx, request); err != nil {
			return fmt.Errorf("step %q: %w", workflowStep.ID, err)
		}
	}
	return nil
}

func (e *Engine) Run(ctx context.Context, definition *workflow.Definition, options Options) (runState *State, runErr error) {
	state, err := initialState(definition, options)
	if err != nil {
		return nil, err
	}
	if err := e.Validate(ctx, definition, options); err != nil {
		return nil, err
	}
	if options.DryRun {
		for i, workflowStep := range definition.Steps {
			kind := workflowStep.Type
			if workflowStep.Action != nil {
				kind = "uses " + workflowStep.Uses.Display()
			}
			policy := executionPolicySuffix(workflowStep)
			if workflowStep.If == "" {
				fmt.Fprintf(options.Stdout, "%d. %s (%s)%s\n", i+1, workflowStep.ID, kind, policy)
			} else {
				fmt.Fprintf(options.Stdout, "%d. %s (%s)%s if: %s\n", i+1, workflowStep.ID, kind, policy, workflowStep.If)
			}
			if workflowStep.Action != nil {
				for j, inner := range workflowStep.Action.Steps {
					fmt.Fprintf(options.Stdout, "   %d.%d %s (%s)%s\n", i+1, j+1, inner.ID, inner.Type, executionPolicySuffix(inner))
				}
			}
		}
		return state, nil
	}

	startedAt := time.Now()
	stats := RunStats{StartedAt: startedAt, Total: len(definition.Steps), Steps: make([]StepStats, 0, len(definition.Steps))}
	report(options, ProgressEvent{
		Kind: WorkflowStarted, Status: StatusRunning, Time: startedAt,
		WorkflowName: definition.Name, Depth: options.depth, Total: len(definition.Steps),
	})
	defer func() {
		finishedAt := time.Now()
		stats.FinishedAt = finishedAt
		stats.Duration = finishedAt.Sub(startedAt)
		state.Stats = stats
		status := statusFromError(runErr)
		report(options, ProgressEvent{
			Kind: WorkflowFinished, Status: status, Time: finishedAt,
			WorkflowName: definition.Name, Depth: options.depth, Total: len(definition.Steps),
			Duration: stats.Duration, Error: runErr, Stats: stats,
		})
	}()

	for i, workflowStep := range definition.Steps {
		kind := executionKind(workflowStep)
		stepStartedAt := time.Now()
		finishStep := func(status ExecutionStatus, stepErr error, attempts []AttemptStats, retryWait time.Duration) {
			stepStats := StepStats{
				ID: workflowStep.ID, Type: kind, Index: i + 1, Status: status,
				StartedAt: stepStartedAt, Duration: time.Since(stepStartedAt),
				RetryWait: retryWait, Attempts: attempts, Error: stepErr,
			}
			recordStep(&stats, stepStats)
			reportStepFinished(options, definition.Name, workflowStep.ID, kind, i+1, len(definition.Steps), stepStats)
		}
		run, err := evaluateCondition(workflowStep.If, makeConditionEnvironment(definition, options.RunDir, state))
		if err != nil {
			stepErr := fmt.Errorf("workflow %q step %q (%s): evaluating if: %w", definition.Name, workflowStep.ID, kind, err)
			finishStep(StatusFailed, stepErr, nil, 0)
			return nil, stepErr
		}
		if !run {
			finishStep(StatusSkipped, nil, nil, 0)
			continue
		}
		report(options, ProgressEvent{
			Kind: StepStarted, Status: StatusRunning, Time: stepStartedAt,
			WorkflowName: definition.Name, Depth: options.depth, StepID: workflowStep.ID,
			StepType: kind, Index: i + 1, Total: len(definition.Steps), MaxAttempts: maxAttempts(workflowStep),
			Timeout: stepTimeout(workflowStep),
		})
		var execute stepExecutor
		cleanup := func() {}
		if workflowStep.Action != nil {
			execute, cleanup, err = e.prepareActionExecutor(definition, workflowStep, options, state)
			if err != nil {
				stepErr := fmt.Errorf("workflow %q step %q (%s): %w", definition.Name, workflowStep.ID, kind, err)
				finishStep(statusFromError(stepErr), stepErr, nil, 0)
				return nil, stepErr
			}
		} else {
			data := templateData(definition, options.RunDir, state)
			rendered, err := renderValue(workflowStep.With, data, workflowStep.Type == "lua")
			if err != nil {
				stepErr := fmt.Errorf("workflow %q step %q (%s): rendering configuration: %w", definition.Name, workflowStep.ID, workflowStep.Type, err)
				finishStep(StatusFailed, stepErr, nil, 0)
				return nil, stepErr
			}
			raw, ok := rendered.(map[string]any)
			if !ok {
				stepErr := fmt.Errorf("workflow %q step %q (%s): configuration is not an object", definition.Name, workflowStep.ID, workflowStep.Type)
				finishStep(StatusFailed, stepErr, nil, 0)
				return nil, stepErr
			}
			runner, err := e.registry.Build(workflowStep.Type, raw)
			if err != nil {
				stepErr := fmt.Errorf("workflow %q step %q (%s): %w", definition.Name, workflowStep.ID, workflowStep.Type, err)
				finishStep(StatusFailed, stepErr, nil, 0)
				return nil, stepErr
			}
			execute = runner.Run
		}
		execution := e.runWithRetry(ctx, definition, workflowStep, options, state, execute)
		cleanup()
		finishStep(statusFromError(execution.err), execution.err, execution.attempts, execution.retryWait)
		if execution.err != nil {
			return nil, fmt.Errorf("workflow %q step %q (%s): %w", definition.Name, workflowStep.ID, kind, execution.err)
		}
		result := execution.result
		if result.Outputs == nil {
			result.Outputs = make(map[string]any)
		}
		state.Steps[workflowStep.ID] = cloneAny(result.Outputs)
		for key, value := range result.Variables {
			state.Vars[key] = cloneAny(value)
		}
	}
	return state, nil
}

func executionKind(workflowStep workflow.Step) string {
	if workflowStep.Action != nil {
		return "uses"
	}
	return workflowStep.Type
}

func stepTimeout(workflowStep workflow.Step) time.Duration {
	if workflowStep.Timeout == nil {
		return 0
	}
	return workflowStep.Timeout.Value()
}

func reportStepFinished(options Options, workflowName, stepID, stepType string, index, total int, stats StepStats) {
	report(options, ProgressEvent{
		Kind: StepFinished, Status: stats.Status, Time: stats.StartedAt.Add(stats.Duration),
		WorkflowName: workflowName, Depth: options.depth, StepID: stepID, StepType: stepType,
		Index: index, Total: total, Attempt: len(stats.Attempts), Duration: stats.Duration, Error: stats.Error,
	})
}

func recordStep(stats *RunStats, stepStats StepStats) {
	stats.Steps = append(stats.Steps, stepStats)
	switch stepStats.Status {
	case StatusSucceeded:
		stats.Succeeded++
	case StatusSkipped:
		stats.Skipped++
	case StatusCanceled:
		stats.Canceled++
	default:
		stats.Failed++
	}
	stats.Attempts += len(stepStats.Attempts)
	stats.Retries += max(0, len(stepStats.Attempts)-1)
	stats.RetryWait += stepStats.RetryWait
	timedOutAttempts := 0
	for _, attempt := range stepStats.Attempts {
		if attempt.Status == StatusTimedOut {
			timedOutAttempts++
		}
	}
	stats.TimedOut += timedOutAttempts
	if stepStats.Status == StatusTimedOut && timedOutAttempts == 0 {
		stats.TimedOut++
	}
}

func statusFromError(err error) ExecutionStatus {
	if err == nil {
		return StatusSucceeded
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return StatusTimedOut
	}
	if errors.Is(err, context.Canceled) {
		return StatusCanceled
	}
	return StatusFailed
}

func initialState(definition *workflow.Definition, options Options) (*State, error) {
	vars, environment, err := workflow.PrepareValues(definition, workflow.LoadOptions{Vars: options.Vars, Env: options.Env, BaseEnv: options.BaseEnv, RunDir: options.RunDir})
	if err != nil {
		return nil, err
	}
	return &State{Inputs: cloneMap(options.inputs), Vars: vars, Env: environment, Steps: make(map[string]any)}, nil
}

func templateData(definition *workflow.Definition, runDir string, state *State) map[string]any {
	return workflow.TemplateData(definition, runDir, state.Inputs, state.Vars, state.Env, state.Steps)
}

func makeRequest(definition *workflow.Definition, stepID string, options Options, state *State, attempt, maxAttempts int, operationID string) step.Request {
	return step.Request{
		StepID: stepID, WorkflowName: definition.Name, WorkflowDir: definition.Dir,
		RunDir: options.RunDir, Inputs: cloneMap(state.Inputs), Vars: cloneMap(state.Vars), Env: maps.Clone(state.Env),
		Steps: cloneMap(state.Steps), Stdin: options.Stdin, Stdout: options.Stdout,
		Stderr: options.Stderr, Interactive: options.Interactive,
		Attempt: attempt, MaxAttempts: maxAttempts, OperationID: operationID,
	}
}

func (e *Engine) validateAction(ctx context.Context, definition *workflow.Definition, workflowStep workflow.Step, options Options, state *State) error {
	if workflowStep.Action == nil {
		return fmt.Errorf("action was not resolved by the workflow loader")
	}
	if err := validateActionBindings(workflowStep.Action, workflowStep.With); err != nil {
		return err
	}
	for name, output := range workflowStep.Action.Outputs {
		if _, err := expr.Compile(output.Value, expr.AllowUndefinedVariables()); err != nil {
			return fmt.Errorf("output %q: %w", name, err)
		}
	}
	dir, cleanup, err := workflowStep.Action.Materialize()
	if err != nil {
		return err
	}
	defer cleanup()
	inputs := actionValidationInputs(workflowStep.Action)
	inner := &workflow.Definition{Version: 1, Name: workflowStep.Action.Name, Dir: dir, Steps: workflowStep.Action.Steps, Vars: map[string]any{}, Env: workflow.Environment{}}
	return e.Validate(ctx, inner, Options{inputs: inputs, BaseEnv: state.Env, RunDir: options.RunDir, Stdin: options.Stdin, Stdout: options.Stdout, Stderr: options.Stderr, Interactive: options.Interactive})
}

func (e *Engine) prepareActionExecutor(definition *workflow.Definition, workflowStep workflow.Step, options Options, state *State) (stepExecutor, func(), error) {
	inputs, err := resolveActionInputs(workflowStep.Action, workflowStep.With, templateData(definition, options.RunDir, state))
	if err != nil {
		return nil, nil, err
	}
	dir, cleanup, err := workflowStep.Action.Materialize()
	if err != nil {
		return nil, nil, err
	}
	inner := &workflow.Definition{Version: 1, Name: workflowStep.Action.Name, Dir: dir, Steps: workflowStep.Action.Steps, Vars: map[string]any{}, Env: workflow.Environment{}}
	execute := func(ctx context.Context, request step.Request) (step.Result, error) {
		innerState, err := e.Run(ctx, inner, Options{
			inputs: inputs, BaseEnv: state.Env, RunDir: options.RunDir,
			Stdin: options.Stdin, Stdout: options.Stdout, Stderr: options.Stderr,
			Interactive: options.Interactive, Progress: options.Progress,
			operationPrefix: request.OperationID, depth: options.depth + 1,
		})
		if err != nil {
			return step.Result{}, err
		}
		environment := map[string]any{"inputs": innerState.Inputs, "vars": innerState.Vars, "steps": innerState.Steps, "env": innerState.Env, "workflow": map[string]any{"name": inner.Name, "dir": inner.Dir}, "run": map[string]any{"dir": options.RunDir}}
		outputs := make(map[string]any, len(workflowStep.Action.Outputs))
		for name, output := range workflowStep.Action.Outputs {
			value, err := expr.Eval(output.Value, environment)
			if err != nil {
				return step.Result{}, fmt.Errorf("evaluating output %q: %w", name, err)
			}
			if !workflow.ActionDataValue(value) {
				return step.Result{}, fmt.Errorf("output %q is not a YAML/JSON-compatible value", name)
			}
			outputs[name] = cloneAny(value)
		}
		return step.Result{Outputs: outputs}, nil
	}
	return execute, cleanup, nil
}

func validateActionBindings(action *workflow.Action, bindings map[string]any) error {
	for name, value := range bindings {
		input, ok := action.Inputs[name]
		if !ok {
			return fmt.Errorf("unknown input %q", name)
		}
		if mapping, ok := value.(map[string]any); ok && len(mapping) == 1 {
			if expression, exists := mapping["expr"]; exists {
				text, ok := expression.(string)
				if !ok || strings.TrimSpace(text) == "" {
					return fmt.Errorf("input %q expr must be a non-empty string", name)
				}
				if _, err := expr.Compile(text, expr.AllowUndefinedVariables()); err != nil {
					return fmt.Errorf("input %q expr: %w", name, err)
				}
				continue
			}
			if literal, exists := mapping["literal"]; exists && !workflow.ActionValueMatches(input.Type, literal) {
				return fmt.Errorf("input %q literal does not match type %s", name, input.Type)
			}
			if literal, exists := mapping["literal"]; exists {
				value = literal
			}
		}
		if !workflow.ActionValueMatches(input.Type, value) {
			return fmt.Errorf("input %q value does not match type %s", name, input.Type)
		}
		if !workflow.ActionDataValue(value) {
			return fmt.Errorf("input %q is not a YAML/JSON-compatible value", name)
		}
		if err := validateTemplates(value, false); err != nil {
			return fmt.Errorf("input %q template: %w", name, err)
		}
	}
	for name, input := range action.Inputs {
		if _, ok := bindings[name]; !ok && input.Required {
			return fmt.Errorf("required input %q is missing", name)
		}
	}
	return nil
}

func actionValidationInputs(action *workflow.Action) map[string]any {
	values := make(map[string]any, len(action.Inputs))
	for name, input := range action.Inputs {
		if input.HasDefault {
			values[name] = cloneAny(input.Default)
			continue
		}
		switch input.Type {
		case "string":
			values[name] = ""
		case "boolean":
			values[name] = false
		case "number":
			values[name] = 0
		case "array":
			values[name] = []any{}
		case "object":
			values[name] = map[string]any{}
		}
	}
	return values
}

func resolveActionInputs(action *workflow.Action, bindings map[string]any, data map[string]any) (map[string]any, error) {
	if err := validateActionBindings(action, bindings); err != nil {
		return nil, err
	}
	values := actionValidationInputs(action)
	for name, input := range action.Inputs {
		value, exists := bindings[name]
		if !exists {
			if input.HasDefault {
				values[name] = cloneAny(input.Default)
			}
			continue
		}
		var err error
		if mapping, ok := value.(map[string]any); ok && len(mapping) == 1 {
			if expression, ok := mapping["expr"].(string); ok {
				value, err = expr.Eval(expression, data)
				if err != nil {
					return nil, fmt.Errorf("evaluating input %q: %w", name, err)
				}
			} else if literal, ok := mapping["literal"]; ok {
				value = literal
			}
		} else {
			value, err = renderValue(value, data, false)
			if err != nil {
				return nil, fmt.Errorf("rendering input %q: %w", name, err)
			}
		}
		if !workflow.ActionValueMatches(input.Type, value) {
			return nil, fmt.Errorf("input %q value does not match type %s", name, input.Type)
		}
		if !workflow.ActionDataValue(value) {
			return nil, fmt.Errorf("input %q is not a YAML/JSON-compatible value", name)
		}
		values[name] = cloneAny(value)
	}
	return values, nil
}

func cloneMap(source map[string]any) map[string]any {
	result := make(map[string]any, len(source))
	for key, value := range source {
		result[key] = cloneAny(value)
	}
	return result
}

func cloneAny(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneMap(typed)
	case []any:
		result := make([]any, len(typed))
		for i, item := range typed {
			result[i] = cloneAny(item)
		}
		return result
	default:
		return value
	}
}
