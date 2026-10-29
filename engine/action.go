package engine

import (
	"context"
	"fmt"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/expr-lang/expr"
	"github.com/up2jj/wuko/diagnostic"
	wukoexpr "github.com/up2jj/wuko/expression"
	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/workflow"
)

// actionLocalValueDir scopes an action's local key-value storage. A remotely fetched Wuko action
// is code the caller did not write, so it gets what a remote workflow gets: no store beside the
// caller's workflow. A plugin action is third-party executable content on the same footing. Path
// and command actions are workspace content the caller already trusts, and keep the caller's root.
func actionLocalValueDir(workflowStep workflow.Step, options Options) string {
	if workflowStep.Uses.URL != "" || workflowStep.Uses.GitHub != "" || workflowStep.Uses.Plugin != "" {
		return ""
	}
	return options.LocalValueDir
}

func actionElevationAllowed(workflowStep workflow.Step, options Options) bool {
	return options.ElevationAllowed && workflowStep.Uses.Path != ""
}

func (e *Engine) validateAction(ctx context.Context, definition *workflow.Definition, workflowStep workflow.Step, options Options, state *State) error {
	if workflowStep.Action == nil {
		return fmt.Errorf("action was not resolved by the workflow loader")
	}
	if err := validateActionBindings(workflowStep.Action, workflowStep.With, options.renderer); err != nil {
		return err
	}
	if err := workflowStep.Action.ValidateReturnContracts(); err != nil {
		return err
	}
	for name, output := range workflowStep.Action.Outputs {
		if _, err := wukoexpr.Compile(output.Value, expr.AllowUndefinedVariables()); err != nil {
			return fmt.Errorf("output %q: %w", name, err)
		}
	}
	dir, cleanup, err := workflowStep.Action.Materialize()
	if err != nil {
		return err
	}
	defer cleanup()
	inputs := actionValidationInputs(workflowStep.Action)
	inner := &workflow.Definition{Version: 1, Name: workflowStep.Action.Name, Timezone: definition.Timezone, Templates: workflowStep.Action.Templates, Plugins: definition.Plugins, Dir: dir, DirBorrowed: workflowStep.Action.DirBorrowed, Steps: workflowStep.Action.Steps, Finally: workflowStep.Action.Finally, Vars: map[string]any{}, Env: workflow.Environment{}, Location: workflowStep.Action.Location}
	inner.InheritSecretSession(definition)
	inner.InheritHelpers(definition)
	inner.InheritPluginResolution(definition)
	return e.Validate(ctx, inner, Options{
		InvocationID: options.InvocationID,
		inputs:       inputs, BaseEnv: state.Env, EnvironmentLoaders: slices.Clone(state.EnvironmentLoaders), RunDir: options.RunDir, Providers: state.Providers.Clone(),
		LocalValueDir: actionLocalValueDir(workflowStep, options), GlobalValueDir: options.GlobalValueDir,
		ElevatedExecutor: options.ElevatedExecutor, ElevationAllowed: actionElevationAllowed(workflowStep, options),
		Stdin: options.Stdin, Stdout: options.Stdout, Stderr: options.Stderr, Interactive: options.Interactive, Prompts: options.Prompts,
		Diagnostics: options.Diagnostics, runID: options.runID, parentRunID: options.parentRunID,
		parentStepRunID: options.parentStepRunID, depth: options.depth + 1, runtime: options.runtime,
		workflowEnvironment: options.workflowEnvironment,
		callerName:          options.callerName,
		callerPath:          options.callerPath,
		onceClaims:          options.onceClaims,
	})
}

func (e *Engine) prepareActionExecutor(definition *workflow.Definition, workflowStep workflow.Step, options Options, state *State) (stepExecutor, func(), error) {
	inputs, err := resolveActionInputs(workflowStep.Action, workflowStep.With, options.renderer, templateData(definition, options.RunDir, options.Interactive, state))
	if err != nil {
		return nil, nil, err
	}
	if options.Diagnostics != nil {
		configText := diagnostic.RedactedJSON(inputs)
		if session := definition.SecretSession(); session != nil {
			configText = session.Redact(configText)
		}
		traceStep(options, definition, workflowStep, diagnostic.PhaseActionInputs, diagnostic.StatusDetail, time.Time{}, "resolved action inputs", nil, diagnostic.Attr("config", configText))
	}
	dir, cleanup, err := workflowStep.Action.Materialize()
	if err != nil {
		return nil, nil, err
	}
	inner := &workflow.Definition{Version: 1, Name: workflowStep.Action.Name, Timezone: definition.Timezone, Templates: workflowStep.Action.Templates, Plugins: definition.Plugins, Dir: dir, DirBorrowed: workflowStep.Action.DirBorrowed, Steps: workflowStep.Action.Steps, Finally: workflowStep.Action.Finally, Vars: map[string]any{}, Env: workflow.Environment{}, Location: workflowStep.Action.Location}
	inner.InheritSecretSession(definition)
	inner.InheritHelpers(definition)
	inner.InheritPluginResolution(definition)
	execute := func(ctx context.Context, request step.Request) (step.Result, error) {
		innerState, err := e.Run(ctx, inner, Options{
			InvocationID: options.InvocationID,
			inputs:       inputs, BaseEnv: state.Env, EnvironmentLoaders: slices.Clone(state.EnvironmentLoaders), RunDir: options.RunDir, Providers: state.Providers.Clone(),
			LocalValueDir: actionLocalValueDir(workflowStep, options), GlobalValueDir: options.GlobalValueDir,
			ElevatedExecutor: options.ElevatedExecutor, ElevationAllowed: actionElevationAllowed(workflowStep, options),
			Stdin: options.Stdin, Stdout: options.Stdout, Stderr: options.Stderr,
			Interactive: options.Interactive, Prompts: options.Prompts, Progress: options.Progress,
			Diagnostics:     options.Diagnostics,
			operationPrefix: request.OperationID, parentRunID: options.runID,
			parentStepRunID: options.stepRunID, depth: options.depth + 1, runtime: options.runtime,
			workflowEnvironment: options.workflowEnvironment,
			callerName:          options.callerName,
			callerPath:          options.callerPath,
			cleanups:            options.cleanups,
			onceClaims:          options.onceClaims,
		})
		if err != nil {
			return step.Result{}, err
		}
		if innerState.didReturn {
			return step.Result{Outputs: cloneMap(innerState.Outputs)}, nil
		}
		environment := map[string]any{"inputs": innerState.Inputs, "vars": innerState.Vars, "steps": innerState.Steps, "env": innerState.Env, "workflow": map[string]any{"name": inner.Name, "dir": inner.Dir, "timezone": inner.Timezone}, "run": step.RunValue{Dir: options.RunDir, EnvironmentLoaders: slices.Clone(state.EnvironmentLoaders), OS: runtime.GOOS, Arch: runtime.GOARCH, Interactive: options.Interactive}}
		for name, value := range innerState.Providers.Values {
			environment[name] = cloneMap(value)
		}
		for name, function := range definition.Helpers().Functions(definition.HelperContext()) {
			environment[name] = function
		}
		outputs := make(map[string]any, len(workflowStep.Action.Outputs))
		outputsStarted := time.Now()
		traceStep(options, definition, workflowStep, diagnostic.PhaseActionOutputs, diagnostic.StatusStarted, time.Time{}, "evaluating action outputs", nil)
		for name, output := range workflowStep.Action.Outputs {
			value, err := wukoexpr.Eval(output.Value, environment)
			if err != nil {
				traceStep(options, definition, workflowStep, diagnostic.PhaseActionOutputs, diagnostic.StatusFailed, outputsStarted, "", err)
				return step.Result{}, fmt.Errorf("evaluating output %q: %w", name, err)
			}
			if !workflow.ActionDataValue(value) {
				err := fmt.Errorf("output %q is not a YAML/JSON-compatible value", name)
				traceStep(options, definition, workflowStep, diagnostic.PhaseActionOutputs, diagnostic.StatusFailed, outputsStarted, "", err)
				return step.Result{}, err
			}
			outputs[name] = cloneAny(value)
		}
		traceStep(options, definition, workflowStep, diagnostic.PhaseActionOutputs, diagnostic.StatusSucceeded, outputsStarted, "", nil, diagnostic.Attr("outputs", fmt.Sprint(len(outputs))))
		return step.Result{Outputs: outputs}, nil
	}
	return execute, cleanup, nil
}

func validateActionBindings(action *workflow.Action, bindings map[string]any, renderer *workflow.Renderer) error {
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
				if _, err := wukoexpr.Compile(text, expr.AllowUndefinedVariables()); err != nil {
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
		if err := validateTemplates(renderer, value, false); err != nil {
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

func resolveActionInputs(action *workflow.Action, bindings map[string]any, renderer *workflow.Renderer, data map[string]any) (map[string]any, error) {
	if err := validateActionBindings(action, bindings, renderer); err != nil {
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
				value, err = wukoexpr.Eval(expression, data)
				if err != nil {
					return nil, fmt.Errorf("evaluating input %q: %w", name, err)
				}
			} else if literal, ok := mapping["literal"]; ok {
				value = literal
			}
		} else {
			value, err = renderValue(renderer, value, data, "")
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
