package engine

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/up2jj/wuko/diagnostic"
	"github.com/up2jj/wuko/workflow"
	"golang.org/x/sync/errgroup"
)

func (e *Engine) runConcurrent(ctx context.Context, definition *workflow.Definition, concurrent *workflow.ConcurrentGroup, options Options, state *State, firstIndex, total int) ([]stepOutcome, error) {
	startedAt := time.Now()
	trace(options, diagnostic.Event{Phase: diagnostic.PhaseConcurrent, Status: diagnostic.StatusStarted, Time: startedAt, WorkflowName: definition.Name, Message: "running concurrent group", Attributes: []diagnostic.Attribute{
		diagnostic.Attr("steps", fmt.Sprint(len(concurrent.Steps))), diagnostic.Attr("max_concurrency", fmt.Sprint(concurrent.MaxConcurrency)), diagnostic.Attr("fail_fast", fmt.Sprint(concurrent.FailFast)),
	}})
	report(options, ProgressEvent{
		Kind: ConcurrentStarted, Status: StatusRunning, Time: startedAt,
		WorkflowName: definition.Name, Depth: options.depth, Total: total,
		GroupSize: len(concurrent.Steps), MaxConcurrency: concurrent.MaxConcurrency,
		Timeout: concurrentTimeout(concurrent), FailFast: concurrent.FailFast,
	})

	groupCtx := ctx
	cancel := func() {}
	if concurrent.Timeout != nil {
		groupCtx, cancel = context.WithTimeout(ctx, concurrent.Timeout.Value())
	}
	defer cancel()

	outcomes := make([]stepOutcome, len(concurrent.Steps))
	snapshot := cloneState(state)
	childOptions := options
	childOptions.Interactive = false
	childOptions.Stdin = nil
	childOptions.depth++

	if concurrent.FailFast {
		group, runCtx := errgroup.WithContext(groupCtx)
		group.SetLimit(min(concurrent.MaxConcurrency, len(concurrent.Steps)))
		for i, workflowStep := range concurrent.Steps {
			group.Go(func() error {
				if runCtx.Err() != nil {
					return nil
				}
				childState := cloneState(snapshot)
				outcomes[i] = e.executeStep(runCtx, definition, workflowStep, childOptions, childState, firstIndex+i, total)
				return outcomes[i].err
			})
		}
		_ = group.Wait()
	} else {
		var group errgroup.Group
		group.SetLimit(min(concurrent.MaxConcurrency, len(concurrent.Steps)))
		for i, workflowStep := range concurrent.Steps {
			group.Go(func() error {
				if groupCtx.Err() != nil {
					return nil
				}
				childState := cloneState(snapshot)
				outcomes[i] = e.executeStep(groupCtx, definition, workflowStep, childOptions, childState, firstIndex+i, total)
				return nil
			})
		}
		_ = group.Wait()
	}

	groupErr := concurrentExecutionError(ctx, groupCtx, concurrent, outcomes)
	if groupErr == nil {
		trace(options, diagnostic.Event{Phase: diagnostic.PhaseCommit, Status: diagnostic.StatusStarted, WorkflowName: definition.Name, Message: "committing concurrent results"})
		groupErr = commitConcurrentResults(state, concurrent.Steps, outcomes)
		status := diagnostic.StatusSucceeded
		if groupErr != nil {
			status = diagnostic.StatusFailed
		}
		trace(options, diagnostic.Event{Phase: diagnostic.PhaseCommit, Status: status, WorkflowName: definition.Name, Message: "concurrent results", Error: groupErr})
	}
	finishedAt := time.Now()
	report(options, ProgressEvent{
		Kind: ConcurrentFinished, Status: statusFromError(groupErr), Time: finishedAt,
		WorkflowName: definition.Name, Depth: options.depth, Total: total,
		GroupSize: len(concurrent.Steps), MaxConcurrency: concurrent.MaxConcurrency,
		Timeout: concurrentTimeout(concurrent), FailFast: concurrent.FailFast,
		Duration: finishedAt.Sub(startedAt), Error: groupErr,
	})
	diagnosticStatus := diagnostic.StatusSucceeded
	if groupErr != nil {
		diagnosticStatus = diagnostic.StatusFailed
	}
	trace(options, diagnostic.Event{Phase: diagnostic.PhaseConcurrent, Status: diagnosticStatus, Time: finishedAt, Duration: finishedAt.Sub(startedAt), WorkflowName: definition.Name})
	if groupErr != nil {
		return outcomes, fmt.Errorf("workflow %q concurrent group: %w", definition.Name, groupErr)
	}
	return outcomes, nil
}

func concurrentExecutionError(parent, groupCtx context.Context, concurrent *workflow.ConcurrentGroup, outcomes []stepOutcome) error {
	if err := parent.Err(); err != nil {
		return err
	}
	if groupCtx.Err() == context.DeadlineExceeded && concurrent.Timeout != nil {
		return fmt.Errorf("timed out after %s: %w", concurrent.Timeout, context.DeadlineExceeded)
	}
	if !concurrent.FailFast {
		joined := make([]error, 0, len(outcomes))
		for _, outcome := range outcomes {
			if outcome.err != nil {
				joined = append(joined, outcome.err)
			}
		}
		return errors.Join(joined...)
	}
	var primary []error
	var canceled []error
	for _, outcome := range outcomes {
		if outcome.err == nil {
			continue
		}
		if errors.Is(outcome.err, context.Canceled) {
			canceled = append(canceled, outcome.err)
			continue
		}
		primary = append(primary, outcome.err)
	}
	if len(primary) > 0 {
		return errors.Join(primary...)
	}
	return errors.Join(canceled...)
}

func commitConcurrentResults(state *State, steps []workflow.Step, outcomes []stepOutcome) error {
	writers := make(map[string]string)
	for i, outcome := range outcomes {
		if outcome.skipped {
			continue
		}
		for _, variable := range slices.Sorted(maps.Keys(outcome.result.Variables)) {
			if previous, exists := writers[variable]; exists {
				return fmt.Errorf("steps %q and %q both write variable %q", previous, steps[i].ID, variable)
			}
			writers[variable] = steps[i].ID
		}
	}
	for i, outcome := range outcomes {
		if outcome.skipped {
			continue
		}
		commitStepResult(state, steps[i].ID, outcome.result)
	}
	return nil
}

func cloneState(state *State) *State {
	return &State{
		Inputs: cloneMap(state.Inputs), Vars: cloneMap(state.Vars), Env: maps.Clone(state.Env),
		Steps: cloneMap(state.Steps),
	}
}

func concurrentTimeout(concurrent *workflow.ConcurrentGroup) time.Duration {
	if concurrent.Timeout == nil {
		return 0
	}
	return concurrent.Timeout.Value()
}
