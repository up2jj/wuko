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
)

type concurrentBranchOutcome struct {
	state   *State
	stats   RunStats
	err     error
	started bool
	skipped bool
}

type concurrentGraph struct {
	dependencies [][]int
	dependents   [][]int
	ancestors    [][]int
}

type concurrentCompletion struct {
	index   int
	outcome concurrentBranchOutcome
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

	e.runConcurrentGraph(groupCtx, definition, concurrent, childOptions, snapshot, indexes, total, outcomes)

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

func (e *Engine) runConcurrentGraph(ctx context.Context, definition *workflow.Definition, group *workflow.ConcurrentGroup, options Options, snapshot *State, indexes []int, total int, outcomes []concurrentBranchOutcome) {
	graph := newConcurrentGraph(group.Steps)
	remaining := make([]int, len(group.Steps))
	ready := make([]int, 0, len(group.Steps))
	for index, dependencies := range graph.dependencies {
		remaining[index] = len(dependencies)
		if len(dependencies) == 0 {
			ready = append(ready, index)
		}
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	completed := make(chan concurrentCompletion, len(group.Steps))
	status := make([]uint8, len(group.Steps)) // 0 pending, 1 running, 2 finished, 3 dependency-skipped
	running := 0
	terminal := 0
	stopAdmission := false

	for terminal < len(group.Steps) {
		for running < group.MaxConcurrency && len(ready) > 0 && !stopAdmission && runCtx.Err() == nil {
			index := ready[0]
			ready = ready[1:]
			if status[index] != 0 {
				continue
			}
			branchSnapshot := concurrentReadState(snapshot, group.Steps, outcomes, graph.ancestors[index])
			status[index] = 1
			running++
			go func() {
				outcome := e.executeConcurrentBranch(runCtx, definition, group.Steps[index], options, branchSnapshot, indexes[index], total)
				completed <- concurrentCompletion{index: index, outcome: outcome}
			}()
		}
		if running == 0 {
			break
		}

		completion := <-completed
		running--
		terminal++
		status[completion.index] = 2
		outcomes[completion.index] = completion.outcome
		if completion.outcome.err != nil {
			terminal += skipConcurrentDescendants(definition, group.Steps, graph.dependents, completion.index, options, indexes, total, outcomes, status)
			if group.FailFast {
				stopAdmission = true
				cancel()
			}
			continue
		}

		for _, dependent := range graph.dependents[completion.index] {
			if status[dependent] != 0 {
				continue
			}
			remaining[dependent]--
			if remaining[dependent] == 0 {
				ready = append(ready, dependent)
			}
		}
		slices.Sort(ready)
	}
}

func newConcurrentGraph(steps []workflow.Step) concurrentGraph {
	ids := make(map[string]int, len(steps))
	for index, workflowStep := range steps {
		if workflowStep.ID != "" {
			ids[workflowStep.ID] = index
		}
	}
	graph := concurrentGraph{
		dependencies: make([][]int, len(steps)),
		dependents:   make([][]int, len(steps)),
		ancestors:    make([][]int, len(steps)),
	}
	for index, workflowStep := range steps {
		for _, need := range workflowStep.Needs {
			// Unknown needs fail validation; ignore the edge rather than
			// letting the zero value fabricate a dependency on the first step.
			dependency, exists := ids[need]
			if !exists {
				continue
			}
			graph.dependencies[index] = append(graph.dependencies[index], dependency)
			graph.dependents[dependency] = append(graph.dependents[dependency], index)
		}
		slices.Sort(graph.dependencies[index])
	}
	for index := range steps {
		seen := make([]bool, len(steps))
		var visit func(int)
		visit = func(current int) {
			for _, dependency := range graph.dependencies[current] {
				if seen[dependency] {
					continue
				}
				seen[dependency] = true
				visit(dependency)
				// Append after recursing so an ancestor follows its own
				// ancestors: a later write in a chain overwrites the earlier
				// one. Sorted dependencies keep unordered ancestors in
				// declaration order.
				graph.ancestors[index] = append(graph.ancestors[index], dependency)
			}
		}
		visit(index)
	}
	return graph
}

func concurrentReadState(snapshot *State, steps []workflow.Step, outcomes []concurrentBranchOutcome, ancestors []int) *State {
	state := cloneState(snapshot)
	for _, index := range ancestors {
		outcome := outcomes[index]
		if outcome.state == nil || outcome.err != nil {
			continue
		}
		maps.Copy(state.Steps, selectedStepOutputs(outcome.state, []workflow.Step{steps[index]}))
		for variable := range outcome.state.writtenVars {
			state.Vars[variable] = cloneAny(outcome.state.Vars[variable])
		}
	}
	return state
}

func skipConcurrentDescendants(definition *workflow.Definition, steps []workflow.Step, dependents [][]int, failed int, options Options, indexes []int, total int, outcomes []concurrentBranchOutcome, status []uint8) int {
	skipped := 0
	queue := slices.Clone(dependents[failed])
	for len(queue) > 0 {
		index := queue[0]
		queue = queue[1:]
		if status[index] != 0 {
			continue
		}
		status[index] = 3
		stats := RunStats{StartedAt: time.Now(), Total: leafStepCount([]workflow.Step{steps[index]})}
		recordSkippedSteps(definition, []workflow.Step{steps[index]}, options, &stats, indexes[index], total)
		stats.FinishedAt = time.Now()
		stats.Duration = stats.FinishedAt.Sub(stats.StartedAt)
		outcomes[index] = concurrentBranchOutcome{stats: stats, skipped: true}
		skipped++
		queue = append(queue, dependents[index]...)
	}
	return skipped
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
		Inputs: cloneMap(state.Inputs), Vars: cloneMap(state.Vars), Env: maps.Clone(state.Env), EnvironmentLoaders: slices.Clone(state.EnvironmentLoaders),
		Steps: cloneMap(state.Steps), Outputs: cloneMap(state.Outputs), Dependencies: cloneDependencies(state.Dependencies), Bindings: cloneMap(state.Bindings),
		presetVars: state.presetVars,
	}
}

func concurrentTimeout(concurrent *workflow.ConcurrentGroup) time.Duration {
	if concurrent.Timeout == nil {
		return 0
	}
	return concurrent.Timeout.Value()
}
