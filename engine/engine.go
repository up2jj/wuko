package engine

import (
	"context"
	"fmt"
	"io"
	"maps"
	"slices"

	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/workflow"
)

type Engine struct {
	registry *step.Registry
}

type Options struct {
	Vars        map[string]any
	Env         map[string]string
	RunDir      string
	Stdin       io.Reader
	Stdout      io.Writer
	Stderr      io.Writer
	Interactive bool
	DryRun      bool
}

type State struct {
	Vars  map[string]any
	Env   map[string]string
	Steps map[string]any
}

func New(registry *step.Registry) *Engine { return &Engine{registry: registry} }

func (e *Engine) Validate(ctx context.Context, definition *workflow.Definition, options Options) error {
	state, err := initialState(definition, options)
	if err != nil {
		return err
	}
	for _, workflowStep := range definition.Steps {
		if workflowStep.If != "" {
			if _, err := compileCondition(workflowStep.If); err != nil {
				return fmt.Errorf("step %q if: %w", workflowStep.ID, err)
			}
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
		request := makeRequest(definition, workflowStep.ID, options, state)
		if err := validator.Validate(ctx, request); err != nil {
			return fmt.Errorf("step %q: %w", workflowStep.ID, err)
		}
	}
	return nil
}

func (e *Engine) Run(ctx context.Context, definition *workflow.Definition, options Options) (*State, error) {
	state, err := initialState(definition, options)
	if err != nil {
		return nil, err
	}
	if err := e.Validate(ctx, definition, options); err != nil {
		return nil, err
	}
	if options.DryRun {
		for i, workflowStep := range definition.Steps {
			if workflowStep.If == "" {
				fmt.Fprintf(options.Stdout, "%d. %s (%s)\n", i+1, workflowStep.ID, workflowStep.Type)
				continue
			}
			fmt.Fprintf(options.Stdout, "%d. %s (%s) if: %s\n", i+1, workflowStep.ID, workflowStep.Type, workflowStep.If)
		}
		return state, nil
	}

	for i, workflowStep := range definition.Steps {
		run, err := evaluateCondition(workflowStep.If, makeConditionEnvironment(definition, options.RunDir, state))
		if err != nil {
			return nil, fmt.Errorf("workflow %q step %q (%s): evaluating if: %w", definition.Name, workflowStep.ID, workflowStep.Type, err)
		}
		if !run {
			fmt.Fprintf(options.Stdout, "[%d/%d] %s (%s) skipped\n", i+1, len(definition.Steps), workflowStep.ID, workflowStep.Type)
			continue
		}
		fmt.Fprintf(options.Stdout, "[%d/%d] %s (%s)\n", i+1, len(definition.Steps), workflowStep.ID, workflowStep.Type)
		data := templateData(definition, options.RunDir, state)
		rendered, err := renderValue(workflowStep.With, data, workflowStep.Type == "lua")
		if err != nil {
			return nil, fmt.Errorf("workflow %q step %q (%s): rendering configuration: %w", definition.Name, workflowStep.ID, workflowStep.Type, err)
		}
		raw, ok := rendered.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("workflow %q step %q (%s): configuration is not an object", definition.Name, workflowStep.ID, workflowStep.Type)
		}
		runner, err := e.registry.Build(workflowStep.Type, raw)
		if err != nil {
			return nil, fmt.Errorf("workflow %q step %q (%s): %w", definition.Name, workflowStep.ID, workflowStep.Type, err)
		}
		result, err := runner.Run(ctx, makeRequest(definition, workflowStep.ID, options, state))
		if err != nil {
			return nil, fmt.Errorf("workflow %q step %q (%s): %w", definition.Name, workflowStep.ID, workflowStep.Type, err)
		}
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

func initialState(definition *workflow.Definition, options Options) (*State, error) {
	vars := cloneMap(definition.Vars)
	for key, value := range options.Vars {
		vars[key] = cloneAny(value)
	}
	host := hostEnvironment()
	wfEnv := make(map[string]string, len(definition.Env))
	root := map[string]any{
		"vars":     vars,
		"env":      environmentAsAny(host),
		"steps":    map[string]any{},
		"workflow": map[string]any{"name": definition.Name, "dir": definition.Dir},
		"run":      map[string]any{"dir": options.RunDir},
	}
	keys := make([]string, 0, len(definition.Env))
	for key := range definition.Env {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		value, err := renderString(definition.Env[key], root)
		if err != nil {
			return nil, fmt.Errorf("rendering workflow environment %s: %w", key, err)
		}
		wfEnv[key] = value
	}
	return &State{Vars: vars, Env: mergeEnvironment(host, wfEnv, options.Env), Steps: make(map[string]any)}, nil
}

func templateData(definition *workflow.Definition, runDir string, state *State) map[string]any {
	return map[string]any{
		"vars":  state.Vars,
		"env":   environmentAsAny(state.Env),
		"steps": state.Steps,
		"workflow": map[string]any{
			"name": definition.Name,
			"dir":  definition.Dir,
		},
		"run": map[string]any{"dir": runDir},
	}
}

func makeRequest(definition *workflow.Definition, stepID string, options Options, state *State) step.Request {
	return step.Request{
		StepID: stepID, WorkflowName: definition.Name, WorkflowDir: definition.Dir,
		RunDir: options.RunDir, Vars: cloneMap(state.Vars), Env: maps.Clone(state.Env),
		Steps: cloneMap(state.Steps), Stdin: options.Stdin, Stdout: options.Stdout,
		Stderr: options.Stderr, Interactive: options.Interactive,
	}
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
