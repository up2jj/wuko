package engine

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"time"

	"github.com/up2jj/wuko/diagnostic"
	"github.com/up2jj/wuko/executor"
	"github.com/up2jj/wuko/workflow"
)

const executorCleanupTimeout = 5 * time.Second

func (e *Engine) validateExecutorBlock(ctx context.Context, definition *workflow.Definition, block workflow.Step, options Options, state *State) error {
	started := time.Now()
	fail := func(err error) error {
		trace(options, diagnostic.Event{Phase: diagnostic.PhaseValidation, Status: diagnostic.StatusFailed, Time: time.Now(), Duration: time.Since(started), WorkflowName: definition.Name, Location: block.Location, Error: err})
		return fmt.Errorf("executor %q: %w", block.Executor.Type, err)
	}
	trace(options, diagnostic.Event{Phase: diagnostic.PhaseValidation, Status: diagnostic.StatusStarted, Time: started, WorkflowName: definition.Name, Location: block.Location, Message: "validating executor " + block.Executor.Type})
	if err := validateTemplates(options.renderer, block.Executor.With, false); err != nil {
		return fail(fmt.Errorf("template: %w", err))
	}
	provider, err := e.executors.Build(block.Executor.Type, block.Executor.With)
	if err != nil {
		return fail(err)
	}
	if validator, ok := provider.(executor.Validator); ok && !options.deferContextValidation {
		request := executorRequest(definition, options, state)
		if err := validator.Validate(ctx, request); err != nil {
			return fail(err)
		}
	}
	childOptions := options
	childOptions.insideExecutor = true
	if err := e.validateSteps(ctx, definition, block.Steps, childOptions, state); err != nil {
		return fail(fmt.Errorf("steps: %w", err))
	}
	if err := e.validateSteps(ctx, definition, block.Finally, childOptions, state); err != nil {
		return fail(fmt.Errorf("finally: %w", err))
	}
	trace(options, diagnostic.Event{Phase: diagnostic.PhaseValidation, Status: diagnostic.StatusSucceeded, Time: time.Now(), Duration: time.Since(started), WorkflowName: definition.Name, Location: block.Location, Message: "validated executor " + block.Executor.Type})
	return nil
}

func (e *Engine) executeExecutorBlock(ctx context.Context, definition *workflow.Definition, block workflow.Step, options Options, state *State, stats *RunStats, firstIndex, total int) (runErr error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	rendered, err := renderValue(options.renderer, block.Executor.With, templateData(definition, options.RunDir, state), false)
	if err != nil {
		return fmt.Errorf("workflow %q executor %q: rendering configuration: %w", definition.Name, block.Executor.Type, err)
	}
	raw, ok := rendered.(map[string]any)
	if !ok {
		return fmt.Errorf("workflow %q executor %q: configuration is not an object", definition.Name, block.Executor.Type)
	}
	provider, err := e.executors.Build(block.Executor.Type, raw)
	if err != nil {
		return fmt.Errorf("workflow %q executor %q: %w", definition.Name, block.Executor.Type, err)
	}
	session, err := provider.Open(ctx, executorRequest(definition, options, state))
	if err != nil {
		return fmt.Errorf("workflow %q executor %q: opening session: %w", definition.Name, block.Executor.Type, err)
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), executorCleanupTimeout)
		defer cancel()
		if closeErr := session.Close(cleanupCtx); closeErr != nil {
			runErr = errors.Join(runErr, fmt.Errorf("workflow %q executor %q: closing session: %w", definition.Name, block.Executor.Type, closeErr))
		}
	}()

	childOptions := options
	childOptions.Executor = session
	childOptions.insideExecutor = true
	serviceScope := newBackgroundSupervisor(ctx)
	childOptions.services = serviceScope
	childOptions.defers = newDeferStack(block.Steps)
	statsStart := len(stats.Steps)
	mainErr := e.executeSequence(serviceScope.context(), definition, block.Steps, childOptions, state, stats, firstIndex, total)
	serviceScope.seal()
	if mainErr != nil || state.didReturn {
		serviceScope.stop(errBackgroundStopped)
	}
	serviceErr := serviceScope.wait()
	if serviceScope.endedScope() && cancellationOnly(mainErr) {
		mainErr = nil
	}
	mainErr = errors.Join(mainErr, serviceErr)
	returning := state.returning
	state.returning = false
	cleanupErrors := e.executeCleanupScope(context.WithoutCancel(ctx), definition, childOptions.defers, block.Finally, childOptions, state, stats, mainErr, stats.Steps[statsStart:], firstIndex+leafStepCount(block.Steps), total)
	state.returning = returning
	return errors.Join(append([]error{mainErr}, cleanupErrors...)...)
}

func executorRequest(definition *workflow.Definition, options Options, state *State) executor.Request {
	return executor.Request{
		WorkflowName: definition.Name,
		RunDir:       options.RunDir,
		Env:          maps.Clone(state.Env),
		Stdout:       options.Stdout,
		Stderr:       options.Stderr,
	}
}
