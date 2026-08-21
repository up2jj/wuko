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

type concurrentBranchOutcome struct {
	state   *State
	stats   RunStats
	err     error
	started bool
	skipped bool
}

func (e *Engine) runConcurrent(ctx context.Context, definition *workflow.Definition, concurrent *workflow.ConcurrentGroup, options Options, state *State, firstIndex, total int) ([]concurrentBranchOutcome, error) {
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

	outcomes := make([]concurrentBranchOutcome, len(concurrent.Steps))
	indexes := concurrentBranchIndexes(concurrent.Steps, firstIndex)
	snapshot := cloneState(state)
	childOptions := options
	childOptions.Interactive = false
	childOptions.Stdin = nil
	childOptions.depth++

	if concurrent.FailFast {
		group, runCtx := errgroup.WithContext(groupCtx)
		slots := make(chan struct{}, min(concurrent.MaxConcurrency, len(concurrent.Steps)))
	failFastLoop:
		for i, workflowStep := range concurrent.Steps {
			select {
			case slots <- struct{}{}:
				if runCtx.Err() != nil {
					<-slots
					break failFastLoop
				}
			case <-runCtx.Done():
				break failFastLoop
			}
			group.Go(func() error {
				defer func() { <-slots }()
				if runCtx.Err() != nil {
					return nil
				}
				outcomes[i] = e.executeConcurrentBranch(runCtx, definition, workflowStep, childOptions, snapshot, indexes[i], total)
				return outcomes[i].err
			})
		}
		_ = group.Wait()
	} else {
		var group errgroup.Group
		slots := make(chan struct{}, min(concurrent.MaxConcurrency, len(concurrent.Steps)))
	collectLoop:
		for i, workflowStep := range concurrent.Steps {
			select {
			case slots <- struct{}{}:
				if groupCtx.Err() != nil {
					<-slots
					break collectLoop
				}
			case <-groupCtx.Done():
				break collectLoop
			}
			group.Go(func() error {
				defer func() { <-slots }()
				if groupCtx.Err() != nil {
					return nil
				}
				outcomes[i] = e.executeConcurrentBranch(groupCtx, definition, workflowStep, childOptions, snapshot, indexes[i], total)
				return nil
			})
		}
		_ = group.Wait()
	}

	groupErr := concurrentExecutionError(ctx, groupCtx, concurrent, outcomes)
	if groupErr == nil {
		trace(options, diagnostic.Event{Phase: diagnostic.PhaseCommit, Status: diagnostic.StatusStarted, WorkflowName: definition.Name, Message: "committing concurrent results"})
		groupErr = commitConcurrentBranches(state, concurrent.Steps, outcomes)
		status := diagnostic.StatusSucceeded
		if groupErr != nil {
			status = diagnostic.StatusFailed
		}
		trace(options, diagnostic.Event{Phase: diagnostic.PhaseCommit, Status: status, WorkflowName: definition.Name, Message: "concurrent results", Error: groupErr})
	}
	started, succeeded := concurrentOutcomeCounts(outcomes)
	finishedAt := time.Now()
	report(options, ProgressEvent{
		Kind: ConcurrentFinished, Status: statusFromError(groupErr), Time: finishedAt,
		WorkflowName: definition.Name, Depth: options.depth, Total: total,
		GroupSize: len(concurrent.Steps), MaxConcurrency: concurrent.MaxConcurrency,
		Started: started, Succeeded: succeeded,
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

func (e *Engine) executeConcurrentBranch(ctx context.Context, definition *workflow.Definition, declaration workflow.Step, options Options, snapshot *State, firstIndex, total int) concurrentBranchOutcome {
	if ctx.Err() != nil {
		return concurrentBranchOutcome{}
	}
	branchState := cloneState(snapshot)
	branchState.writtenVars = make(map[string]struct{})
	branchTotal := leafStepCount([]workflow.Step{declaration})
	stats := RunStats{StartedAt: time.Now(), Total: branchTotal, Steps: make([]StepStats, 0, branchTotal)}
	err := e.executeSequence(ctx, definition, []workflow.Step{declaration}, options, branchState, &stats, firstIndex, total)
	stats.FinishedAt = time.Now()
	stats.Duration = stats.FinishedAt.Sub(stats.StartedAt)
	skipped := len(stats.Steps) > 0
	for _, stepStats := range stats.Steps {
		if stepStats.Status != StatusSkipped {
			skipped = false
			break
		}
	}
	return concurrentBranchOutcome{state: branchState, stats: stats, err: err, started: true, skipped: skipped}
}

func concurrentBranchIndexes(steps []workflow.Step, firstIndex int) []int {
	indexes := make([]int, len(steps))
	next := firstIndex
	for i, workflowStep := range steps {
		indexes[i] = next
		next += leafStepCount([]workflow.Step{workflowStep})
	}
	return indexes
}

func concurrentOutcomeCounts(outcomes []concurrentBranchOutcome) (started, succeeded int) {
	for _, outcome := range outcomes {
		if !outcome.started {
			continue
		}
		started++
		if outcome.err == nil && !outcome.skipped {
			succeeded++
		}
	}
	return started, succeeded
}

func concurrentExecutionError(parent, groupCtx context.Context, concurrent *workflow.ConcurrentGroup, outcomes []concurrentBranchOutcome) error {
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

func commitConcurrentBranches(state *State, steps []workflow.Step, outcomes []concurrentBranchOutcome) error {
	writers := make(map[string]string)
	for i, outcome := range outcomes {
		if !outcome.started || outcome.state == nil {
			continue
		}
		for _, variable := range slices.Sorted(maps.Keys(outcome.state.writtenVars)) {
			if previous, exists := writers[variable]; exists {
				return fmt.Errorf("steps %q and %q both write variable %q", previous, concurrentBranchLabel(steps[i], i), variable)
			}
			writers[variable] = concurrentBranchLabel(steps[i], i)
		}
	}
	for i, outcome := range outcomes {
		if !outcome.started || outcome.state == nil {
			continue
		}
		maps.Copy(state.Steps, selectedStepOutputs(outcome.state, []workflow.Step{steps[i]}))
		for _, variable := range slices.Sorted(maps.Keys(outcome.state.writtenVars)) {
			state.Vars[variable] = cloneAny(outcome.state.Vars[variable])
			if state.writtenVars != nil {
				state.writtenVars[variable] = struct{}{}
			}
		}
	}
	return nil
}

func concurrentBranchLabel(declaration workflow.Step, index int) string {
	if declaration.ID != "" {
		return declaration.ID
	}
	return fmt.Sprintf("branch %d", index+1)
}

func cloneState(state *State) *State {
	return &State{
		Inputs: cloneMap(state.Inputs), Vars: cloneMap(state.Vars), Env: maps.Clone(state.Env),
		Steps: cloneMap(state.Steps), Bindings: cloneMap(state.Bindings),
	}
}

func concurrentTimeout(concurrent *workflow.ConcurrentGroup) time.Duration {
	if concurrent.Timeout == nil {
		return 0
	}
	return concurrent.Timeout.Value()
}
