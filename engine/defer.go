package engine

import (
	"context"
	"fmt"
	"maps"

	"github.com/up2jj/wuko/workflow"
)

func (e *Engine) validateDeferredSteps(ctx context.Context, definition *workflow.Definition, owner workflow.Step, options Options, state *State) error {
	if len(owner.Defer) == 0 {
		return nil
	}
	bindingsWereNil := state.Bindings == nil
	if state.Bindings == nil {
		state.Bindings = make(map[string]any)
	}
	previous, hadPrevious := state.Bindings["finally"]
	state.Bindings["finally"] = map[string]any{"status": string(StatusSucceeded), "errors": []any{}}
	err := e.validateSteps(ctx, definition, owner.Defer, options, state)
	if hadPrevious {
		state.Bindings["finally"] = previous
	} else {
		delete(state.Bindings, "finally")
	}
	if bindingsWereNil && len(state.Bindings) == 0 {
		state.Bindings = nil
	}
	if err != nil {
		return fmt.Errorf("step %q defer: %w", owner.ID, err)
	}
	return nil
}

type deferredGroup struct {
	ownerID string
	steps   []workflow.Step
	options Options
	active  bool
}

// deferStack is shared by every step in a scope and carries no lock. That is sound
// only because defer: is rejected inside concurrent groups and fan-out controls
// (validateSteps in workflow/types.go, scopeConcurrent and scopeControl), so register
// is never reached from more than one goroutine. Relaxing that validation rule
// without adding synchronization here would introduce a data race.
type deferStack struct {
	groups  []*deferredGroup
	byOwner map[string]*deferredGroup
}

func newDeferStack(steps []workflow.Step) *deferStack {
	stack := &deferStack{byOwner: make(map[string]*deferredGroup)}
	stack.collect(steps)
	return stack
}

func (stack *deferStack) collect(steps []workflow.Step) {
	for _, workflowStep := range steps {
		switch {
		case workflowStep.IsExecutorBlock():
			// Executor blocks own an independent defer scope.
		case workflowStep.IsWorktreeBlock():
			// Worktree blocks own an independent defer scope that must run before
			// the worktree is removed.
		case workflowStep.IsEnvironmentBlock(), workflowStep.IsWorkingDirectoryBlock(), workflowStep.IsConditionalBlock():
			stack.collect(workflowStep.Steps)
		default:
			if len(workflowStep.Defer) == 0 {
				continue
			}
			group := &deferredGroup{ownerID: workflowStep.ID, steps: workflowStep.Defer}
			stack.groups = append(stack.groups, group)
			stack.byOwner[workflowStep.ID] = group
		}
	}
}

func (stack *deferStack) register(ownerID string, options Options) {
	if stack == nil {
		return
	}
	group := stack.byOwner[ownerID]
	if group == nil {
		return
	}
	group.options = options
	group.active = true
}

func (stack *deferStack) stepCount() int {
	if stack == nil {
		return 0
	}
	total := 0
	for _, group := range stack.groups {
		total += leafStepCount(group.steps)
	}
	return total
}

func nestedDeferScopeStepCount(steps []workflow.Step) int {
	total := 0
	for _, workflowStep := range steps {
		switch {
		case workflowStep.IsExecutorBlock():
			total += newDeferStack(workflowStep.Steps).stepCount()
		case workflowStep.IsEnvironmentBlock(), workflowStep.IsWorkingDirectoryBlock(), workflowStep.IsConditionalBlock():
			total += nestedDeferScopeStepCount(workflowStep.Steps)
		}
	}
	return total
}

func (e *Engine) executeDeferred(ctx context.Context, definition *workflow.Definition, stack *deferStack, fallback Options, state *State, stats *RunStats, firstIndex, total int) (cleanupErrors []error, nextIndex int) {
	index := firstIndex
	if stack == nil {
		return nil, index
	}
	for groupIndex := len(stack.groups) - 1; groupIndex >= 0; groupIndex-- {
		group := stack.groups[groupIndex]
		options := fallback
		if group.active {
			options = group.options
			previousEnv := state.Env
			if options.scopedEnv != nil {
				state.Env = maps.Clone(options.scopedEnv)
			}
			cleanupErrors = append(cleanupErrors, e.executeCleanupSteps(ctx, definition, group.steps, options, state, stats, index, total)...)
			state.Env = previousEnv
		} else {
			recordSkippedSteps(definition, group.steps, options, stats, index, total)
		}
		index += leafStepCount(group.steps)
	}
	return cleanupErrors, index
}
