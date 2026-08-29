package engine

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/up2jj/wuko/diagnostic"
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
	for _, name := range slices.Sorted(maps.Keys(block.Env)) {
		if err := validateTemplates(options.renderer, block.Env[name], false); err != nil {
			return fail(fmt.Errorf("env %q template: %w", name, err))
		}
	}
	environment, resolved, renderErr := renderEnvironmentOverlay(options.renderer, block.Env, templateData(definition, options.RunDir, state), state.Env)
	childOptions := options
	if renderErr != nil {
		environment = maps.Clone(state.Env)
		if environment == nil {
			environment = make(map[string]string, len(block.Env))
		}
		maps.Copy(environment, block.Env)
		resolved = false
	}
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
	environment, _, err := renderEnvironmentOverlay(options.renderer, block.Env, templateData(definition, options.RunDir, state), state.Env)
	if err != nil {
		trace(options, diagnostic.Event{
			Phase: diagnostic.PhaseRender, Status: diagnostic.StatusFailed, Time: time.Now(), Duration: time.Since(started),
			WorkflowName: definition.Name, Location: block.Location, Error: err,
		})
		return fmt.Errorf("workflow %q env block: %w", definition.Name, err)
	}
	trace(options, diagnostic.Event{
		Phase: diagnostic.PhaseRender, Status: diagnostic.StatusSucceeded, Time: time.Now(), Duration: time.Since(started),
		WorkflowName: definition.Name, Location: block.Location, Message: fmt.Sprintf("%d variables", len(block.Env)),
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

func renderEnvironmentOverlay(renderer *workflow.Renderer, overlay workflow.Environment, data map[string]any, base map[string]string) (map[string]string, bool, error) {
	rendered := make(map[string]string, len(overlay))
	resolved := true
	for _, name := range slices.Sorted(maps.Keys(overlay)) {
		value, err := renderer.Render(overlay[name], data)
		if err != nil {
			return nil, false, fmt.Errorf("rendering environment %q: %w", name, err)
		}
		if strings.Contains(value, "<no value>") {
			resolved = false
		}
		rendered[name] = value
	}
	environment := maps.Clone(base)
	if environment == nil {
		environment = make(map[string]string, len(rendered))
	}
	maps.Copy(environment, rendered)
	return environment, resolved, nil
}
