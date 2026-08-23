package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/expr-lang/expr"
	"github.com/expr-lang/expr/vm"
	"github.com/up2jj/wuko/diagnostic"
	wukoexpr "github.com/up2jj/wuko/expression"
	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/workflow"
)

const defaultPollInterval = 5 * time.Second

type waitConfig struct {
	Duration *workflow.Duration `yaml:"duration,omitempty"`
	Interval workflow.Duration  `yaml:"interval,omitempty"`
	Step     *waitNestedStep    `yaml:"step,omitempty"`
	Until    string             `yaml:"until,omitempty"`
}

type waitNestedStep struct {
	Type string         `yaml:"type"`
	With map[string]any `yaml:"with,omitempty"`
}

type waitEnvironment struct {
	Inputs       map[string]any            `expr:"inputs"`
	Vars         map[string]any            `expr:"vars"`
	Env          map[string]string         `expr:"env"`
	Steps        map[string]any            `expr:"steps"`
	Dependencies map[string]map[string]any `expr:"dependencies"`
	Batch        map[string]any            `expr:"batch"`
	Foreach      map[string]any            `expr:"foreach"`
	Matrix       map[string]any            `expr:"matrix"`
	Finally      map[string]any            `expr:"finally"`
	Workflow     conditionWorkflow         `expr:"workflow"`
	Run          conditionRun              `expr:"run"`
	Result       map[string]any            `expr:"result"`
	Error        any                       `expr:"error"`
	Poll         int                       `expr:"poll"`
}

type waitMetrics struct {
	polls    int
	pollWait time.Duration
}

func decodeWaitConfig(raw map[string]any) (waitConfig, error) {
	config := waitConfig{Interval: workflow.Duration(defaultPollInterval)}
	if err := step.DecodeConfig(raw, &config); err != nil {
		return waitConfig{}, err
	}
	_, hasDuration := raw["duration"]
	_, hasStep := raw["step"]
	_, hasUntil := raw["until"]
	_, hasInterval := raw["interval"]
	hasPolling := hasStep || hasUntil || hasInterval
	if hasDuration == hasPolling {
		return waitConfig{}, fmt.Errorf("exactly one of duration or polling fields (step, until, interval) is required")
	}
	if hasDuration {
		if config.Duration == nil || config.Duration.Value() <= 0 {
			return waitConfig{}, fmt.Errorf("duration must be greater than zero")
		}
		return config, nil
	}
	if config.Step == nil {
		return waitConfig{}, fmt.Errorf("step is required for polling")
	}
	if strings.TrimSpace(config.Step.Type) == "" {
		return waitConfig{}, fmt.Errorf("nested step type is required")
	}
	if config.Step.Type == "wait" {
		return waitConfig{}, fmt.Errorf("nested wait steps are not supported")
	}
	if config.Step.With == nil {
		config.Step.With = make(map[string]any)
	}
	if strings.TrimSpace(config.Until) == "" {
		return waitConfig{}, fmt.Errorf("until is required for polling")
	}
	if config.Interval.Value() <= 0 {
		return waitConfig{}, fmt.Errorf("interval must be greater than zero")
	}
	return config, nil
}

func compileWaitCondition(condition string) (*vm.Program, error) {
	program, err := wukoexpr.Compile(condition, expr.Env(waitEnvironment{}), expr.AsBool())
	if err != nil {
		return nil, fmt.Errorf("compiling until: %w", err)
	}
	return program, nil
}

func (e *Engine) validateWaitStep(ctx context.Context, definition *workflow.Definition, workflowStep workflow.Step, options Options, state *State, validateRunner bool) error {
	config, err := decodeWaitConfig(workflowStep.With)
	if err != nil {
		return fmt.Errorf("decoding wait step: %w", err)
	}
	if config.Step == nil {
		return nil
	}
	if workflowStep.Timeout == nil {
		return fmt.Errorf("polling wait requires a top-level timeout")
	}
	if _, err := compileWaitCondition(config.Until); err != nil {
		return err
	}
	if err := validateTemplates(options.renderer, config.Step.With, config.Step.Type == "lua"); err != nil {
		return fmt.Errorf("nested step template: %w", err)
	}
	runner, err := e.registry.Build(config.Step.Type, config.Step.With)
	if err != nil {
		return fmt.Errorf("nested step: %w", err)
	}
	validator, ok := runner.(step.Validator)
	if !ok || !validateRunner {
		return nil
	}
	request := makeRequest(definition, workflowStep.ID, options, state, 1, maxAttempts(workflowStep), "validation")
	if err := validator.Validate(ctx, request); err != nil {
		return fmt.Errorf("nested step: %w", err)
	}
	return nil
}

func (e *Engine) prepareWaitExecutor(definition *workflow.Definition, workflowStep workflow.Step, options Options, state *State, metrics *waitMetrics) (stepExecutor, error) {
	config, err := decodeWaitConfig(workflowStep.With)
	if err != nil {
		return nil, fmt.Errorf("decoding wait step: %w", err)
	}
	if config.Step == nil {
		return fixedWaitExecutor(config.Duration.Value()), nil
	}
	rendered, err := renderValue(options.renderer, config.Step.With, templateData(definition, options.RunDir, state), config.Step.Type == "lua")
	if err != nil {
		return nil, fmt.Errorf("rendering nested step configuration: %w", err)
	}
	raw, ok := rendered.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("nested step configuration is not an object")
	}
	runner, err := e.registry.Build(config.Step.Type, raw)
	if err != nil {
		return nil, fmt.Errorf("nested step: %w", err)
	}
	program, err := compileWaitCondition(config.Until)
	if err != nil {
		return nil, err
	}
	return e.pollExecutor(definition, workflowStep, options, config, runner, program, metrics), nil
}

func fixedWaitExecutor(duration time.Duration) stepExecutor {
	return func(ctx context.Context, _ step.Request) (step.Result, error) {
		timer := time.NewTimer(duration)
		defer timer.Stop()
		select {
		case <-timer.C:
			return step.Result{Outputs: map[string]any{}}, nil
		case <-ctx.Done():
			return step.Result{}, ctx.Err()
		}
	}
}

func (e *Engine) pollExecutor(definition *workflow.Definition, workflowStep workflow.Step, options Options, config waitConfig, runner step.Runner, program *vm.Program, metrics *waitMetrics) stepExecutor {
	execute := managedExecutor(options, workflowStep.ID, runner)
	return func(ctx context.Context, request step.Request) (step.Result, error) {
		for poll := 1; ; poll++ {
			if err := ctx.Err(); err != nil {
				return step.Result{}, err
			}
			metrics.polls++
			started := time.Now()
			report(options, ProgressEvent{
				Kind: PollStarted, Status: StatusRunning, Time: started,
				WorkflowName: definition.Name, Depth: options.depth, StepID: workflowStep.ID,
				StepType: "wait", Poll: poll,
			})
			traceStep(options, definition, workflowStep, diagnostic.PhasePoll, diagnostic.StatusStarted, time.Time{}, "executing nested "+config.Step.Type+" step", nil, diagnostic.Attr("poll", fmt.Sprint(poll)))

			result, runErr := execute(ctx, request)
			duration := time.Since(started)
			if err := ctx.Err(); err != nil {
				reportPollFinished(options, definition, workflowStep, poll, duration, statusFromError(err), false, err)
				traceStep(options, definition, workflowStep, diagnostic.PhasePoll, diagnostic.StatusFailed, started, "poll canceled", err, diagnostic.Attr("poll", fmt.Sprint(poll)))
				return step.Result{}, err
			}
			if runErr != nil && config.Step.Type == "http" {
				var observation step.ObservationError
				if !errors.As(runErr, &observation) || !observation.ObservationAvailable() {
					reportPollFinished(options, definition, workflowStep, poll, duration, statusFromError(runErr), false, runErr)
					traceStep(options, definition, workflowStep, diagnostic.PhasePoll, diagnostic.StatusFailed, started, "HTTP poll failed before producing a response", runErr, diagnostic.Attr("poll", fmt.Sprint(poll)))
					return step.Result{}, runErr
				}
			}
			if result.Outputs == nil {
				result.Outputs = make(map[string]any)
			}
			var errorValue any
			if runErr != nil {
				errorValue = runErr.Error()
			}
			matched, err := evaluateWaitCondition(program, request, result.Outputs, errorValue, poll)
			if err != nil {
				reportPollFinished(options, definition, workflowStep, poll, duration, StatusFailed, false, err)
				traceStep(options, definition, workflowStep, diagnostic.PhasePoll, diagnostic.StatusFailed, started, "evaluating until", err, diagnostic.Attr("poll", fmt.Sprint(poll)))
				return step.Result{}, err
			}
			pollStatus := StatusRunning
			if matched {
				pollStatus = StatusSucceeded
			}
			reportPollFinished(options, definition, workflowStep, poll, duration, pollStatus, matched, runErr)
			status := diagnostic.StatusDetail
			message := "condition did not match"
			if matched {
				status = diagnostic.StatusSucceeded
				message = "condition matched"
			}
			traceStep(options, definition, workflowStep, diagnostic.PhasePoll, status, started, message, nil,
				diagnostic.Attr("poll", fmt.Sprint(poll)), diagnostic.Attr("nested_error", fmt.Sprint(errorValue)))
			if matched {
				return result, nil
			}

			next := poll + 1
			report(options, ProgressEvent{
				Kind: PollScheduled, Status: StatusRunning, Time: time.Now(),
				WorkflowName: definition.Name, Depth: options.depth, StepID: workflowStep.ID,
				StepType: "wait", Poll: next, PollDelay: config.Interval.Value(), Error: runErr,
			})
			traceStep(options, definition, workflowStep, diagnostic.PhasePoll, diagnostic.StatusDetail, time.Time{}, "next poll scheduled", nil,
				diagnostic.Attr("poll", fmt.Sprint(next)), diagnostic.Attr("delay", config.Interval.String()))
			waitStarted := time.Now()
			if err := waitForRetry(ctx, config.Interval.Value()); err != nil {
				metrics.pollWait += time.Since(waitStarted)
				return step.Result{}, err
			}
			metrics.pollWait += time.Since(waitStarted)
		}
	}
}

func evaluateWaitCondition(program *vm.Program, request step.Request, result map[string]any, errorValue any, poll int) (bool, error) {
	value, err := expr.Run(program, waitEnvironment{
		Inputs: request.Inputs, Vars: request.Vars, Env: request.Env, Steps: request.Steps, Dependencies: request.Dependencies,
		Batch:   bindingRoot(request.Bindings, "batch"),
		Foreach: bindingRoot(request.Bindings, "foreach"), Matrix: bindingRoot(request.Bindings, "matrix"),
		Finally:  bindingRoot(request.Bindings, "finally"),
		Workflow: conditionWorkflow{Name: request.WorkflowName, Dir: request.WorkflowDir},
		Run:      conditionRun{Dir: request.RunDir}, Result: result, Error: errorValue, Poll: poll,
	})
	if err != nil {
		return false, fmt.Errorf("evaluating until: %w", err)
	}
	matched, ok := value.(bool)
	if !ok {
		return false, fmt.Errorf("until returned %T, want bool", value)
	}
	return matched, nil
}

func reportPollFinished(options Options, definition *workflow.Definition, workflowStep workflow.Step, poll int, duration time.Duration, status ExecutionStatus, matched bool, err error) {
	report(options, ProgressEvent{
		Kind: PollFinished, Status: status, Time: time.Now(),
		WorkflowName: definition.Name, Depth: options.depth, StepID: workflowStep.ID,
		StepType: "wait", Poll: poll, Duration: duration, Matched: matched, Error: err,
	})
}

func waitPolicyDescription(workflowStep workflow.Step) string {
	config, err := decodeWaitConfig(workflowStep.With)
	if err != nil {
		return ""
	}
	if config.Step == nil {
		return "duration " + config.Duration.String()
	}
	return "poll " + config.Step.Type + " every " + config.Interval.String()
}
