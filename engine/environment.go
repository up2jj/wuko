package engine

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/expr-lang/expr"
	"github.com/up2jj/wuko/diagnostic"
	wukoexpr "github.com/up2jj/wuko/expression"
	"github.com/up2jj/wuko/workflow"
)

func (e *Engine) validateEnvironmentBlock(ctx context.Context, definition *workflow.Definition, block workflow.Step, options Options, state *State) error {
	started := time.Now()
	trace(options, diagnostic.Event{
		Phase: diagnostic.PhaseValidation, Status: diagnostic.StatusStarted, Time: started,
		WorkflowName: definition.Name, Location: block.Location, Message: "validating env block",
	})
	fail := func(err error) error {
		trace(options, diagnostic.Event{
			Phase: diagnostic.PhaseValidation, Status: diagnostic.StatusFailed, Time: time.Now(), Duration: time.Since(started),
			WorkflowName: definition.Name, Location: block.Location, Error: err,
		})
		return err
	}
	if err := validateEnvironmentTemplates(options.renderer, block.Env); err != nil {
		return fail(err)
	}
	environment, resolved, err := renderEnvironmentOverlay(options.renderer, block.Env, templateData(definition, options.RunDir, options.Interactive, state), state.Env, true)
	if err != nil {
		return fail(err)
	}
	childOptions := options
	childOptions.scopedEnv = maps.Clone(environment)
	if !resolved {
		childOptions.deferContextValidation = true
	}
	previous := state.Env
	state.Env = environment
	defer func() { state.Env = previous }()
	if err := e.validateSteps(ctx, definition, block.Steps, childOptions, state); err != nil {
		return fail(fmt.Errorf("env block: %w", err))
	}
	trace(options, diagnostic.Event{
		Phase: diagnostic.PhaseValidation, Status: diagnostic.StatusSucceeded, Time: time.Now(), Duration: time.Since(started),
		WorkflowName: definition.Name, Location: block.Location,
	})
	return nil
}

func (e *Engine) executeEnvironmentBlock(ctx context.Context, definition *workflow.Definition, block workflow.Step, options Options, state *State, stats *RunStats, firstIndex, total int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	started := time.Now()
	trace(options, diagnostic.Event{
		Phase: diagnostic.PhaseRender, Status: diagnostic.StatusStarted, Time: started,
		WorkflowName: definition.Name, Location: block.Location, Message: "rendering env block",
	})
	environment, _, err := renderEnvironmentOverlay(options.renderer, block.Env, templateData(definition, options.RunDir, options.Interactive, state), state.Env, false)
	if err != nil {
		trace(options, diagnostic.Event{
			Phase: diagnostic.PhaseRender, Status: diagnostic.StatusFailed, Time: time.Now(), Duration: time.Since(started),
			WorkflowName: definition.Name, Location: block.Location, Error: err,
		})
		return fmt.Errorf("workflow %q env block: %w", definition.Name, err)
	}
	trace(options, diagnostic.Event{
		Phase: diagnostic.PhaseRender, Status: diagnostic.StatusSucceeded, Time: time.Now(), Duration: time.Since(started),
		WorkflowName: definition.Name, Location: block.Location, Message: fmt.Sprintf("%d environment operations", len(block.Env)),
	})

	childOptions := options
	childOptions.scopedEnv = maps.Clone(environment)
	childOptions.deferContextValidation = false
	previous := state.Env
	state.Env = environment
	defer func() { state.Env = previous }()
	if err := e.validateSteps(ctx, definition, block.Steps, childOptions, state); err != nil {
		return fmt.Errorf("workflow %q env block: validating scoped steps: %w", definition.Name, err)
	}
	return e.executeSequence(ctx, definition, block.Steps, childOptions, state, stats, firstIndex, total)
}

// validateEnvironmentTemplates checks syntax without evaluating runtime values.
func validateEnvironmentTemplates(renderer *workflow.Renderer, overlay workflow.ScopedEnvironment) error {
	return overlay.WalkSources(func(label, source string, isExpression bool) error {
		if isExpression {
			if _, err := wukoexpr.Compile(source, expr.AllowUndefinedVariables()); err != nil {
				return fmt.Errorf("%s: %w", label, err)
			}
			return nil
		}
		if err := renderer.Validate(source); err != nil {
			return fmt.Errorf("%s template: %w", label, err)
		}
		return nil
	})
}

// renderEnvironmentOverlay resolves operations on a private snapshot. Validation
// keeps known assignments/removals while deferring values not yet available.
func renderEnvironmentOverlay(renderer *workflow.Renderer, overlay workflow.ScopedEnvironment, data map[string]any, base map[string]string, deferUnresolved bool) (map[string]string, bool, error) {
	environment := maps.Clone(base)
	if environment == nil {
		environment = make(map[string]string)
	}
	data = maps.Clone(data)
	resolved := true
	// The template view of env is rebuilt only when an operation reads it after a change.
	stale := true
	snapshot := func() {
		if stale {
			data["env"] = workflow.EnvironmentValues(environment)
			stale = false
		}
	}
	for index, operation := range overlay {
		if operation.Set != nil {
			snapshot()
			// Every entry sees the same pre-operation snapshot.
			for _, name := range slices.Sorted(maps.Keys(operation.Set)) {
				declaration := operation.Set[name]
				var value any
				var err error
				switch {
				case declaration.Expression != "":
					value, err = wukoexpr.Eval(declaration.Expression, data)
				case declaration.Literal != nil:
					var rendered string
					rendered, err = renderer.Render(*declaration.Literal, data)
					if err == nil && deferUnresolved && strings.Contains(rendered, "<no value>") {
						err = errUnresolvedEnvironment
					}
					value = rendered
				}
				if err != nil {
					if !deferUnresolved {
						return nil, false, fmt.Errorf("env operation %d set %q: %w", index+1, name, err)
					}
					resolved = false
					environment[name] = ""
					continue
				}
				switch typed := value.(type) {
				case nil:
					delete(environment, name)
				case string:
					environment[name] = typed
				default:
					return nil, false, fmt.Errorf("env operation %d set %q: expression must return a string or nil", index+1, name)
				}
			}
			stale = true
			continue
		}
		if operation.Unset.Expression != "" || slices.ContainsFunc(operation.Unset.Patterns, isEnvironmentTemplate) {
			snapshot()
		}
		patterns, unsetResolved, err := resolveEnvironmentUnset(renderer, operation.Unset, data, deferUnresolved)
		if err != nil {
			return nil, false, fmt.Errorf("env operation %d unset: %w", index+1, err)
		}
		resolved = resolved && unsetResolved
		for item, pattern := range patterns {
			if err := workflow.ValidateEnvironmentPattern(pattern); err != nil {
				if deferUnresolved && !unsetResolved {
					continue
				}
				return nil, false, fmt.Errorf("env operation %d unset item %d: %w", index+1, item+1, err)
			}
			if !strings.ContainsAny(pattern, `*?[\`) {
				if _, ok := environment[pattern]; ok {
					delete(environment, pattern)
					stale = true
				}
				continue
			}
			for name := range environment {
				if matches, _ := path.Match(pattern, name); matches {
					delete(environment, name)
					stale = true
				}
			}
		}
	}
	return environment, resolved, nil
}

var errUnresolvedEnvironment = errors.New("value depends on data not available during validation")

func isEnvironmentTemplate(pattern string) bool { return strings.Contains(pattern, "{{") }

func resolveEnvironmentUnset(renderer *workflow.Renderer, unset *workflow.EnvironmentUnset, data map[string]any, deferUnresolved bool) ([]string, bool, error) {
	if unset.Expression != "" {
		value, err := wukoexpr.Eval(unset.Expression, data)
		if err != nil {
			if deferUnresolved {
				return nil, false, nil
			}
			return nil, false, err
		}
		switch typed := value.(type) {
		case []string:
			return typed, true, nil
		case []any:
			patterns := make([]string, len(typed))
			for i, item := range typed {
				pattern, ok := item.(string)
				if !ok {
					return nil, true, fmt.Errorf("expression must return a list of strings (item %d is not a string)", i+1)
				}
				patterns[i] = pattern
			}
			return patterns, true, nil
		default:
			return nil, true, fmt.Errorf("expression must return a list of strings")
		}
	}
	patterns := make([]string, 0, len(unset.Patterns))
	resolved := true
	for i, pattern := range unset.Patterns {
		rendered, err := renderer.Render(pattern, data)
		if err != nil {
			if deferUnresolved {
				resolved = false
				continue
			}
			return nil, false, fmt.Errorf("item %d template: %w", i+1, err)
		}
		patterns = append(patterns, rendered)
	}
	return patterns, resolved, nil
}
