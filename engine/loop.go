package engine

import (
	"context"
	"fmt"
	"time"

	controlpkg "github.com/up2jj/wuko/control"
	"github.com/up2jj/wuko/diagnostic"
	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/workflow"
)

func (e *Engine) validateLoop(ctx context.Context, definition *workflow.Definition, workflowStep workflow.Step, options Options, state *State) error {
	group := workflowStep.Loop
	if err := group.Validate(); err != nil {
		return err
	}
	childOptions := options
	childOptions.depth++
	return e.validateSteps(ctx, definition, group.Steps, childOptions, state)
}

func (e *Engine) executeLoop(ctx context.Context, definition *workflow.Definition, workflowStep workflow.Step, options Options, state *State, index, total int) stepOutcome {
	group := workflowStep.Loop
	startedAt := time.Now()
	outcome := stepOutcome{started: true}
	finish := func(status ExecutionStatus, err error, nested RunStats) {
		outcome.stats = StepStats{
			StepRunID: options.stepRunID, ID: workflowStep.ID, Type: "loop", Index: index, Status: status,
			Location: workflowStep.Location, StartedAt: startedAt, Duration: time.Since(startedAt), Error: err,
		}
		outcome.nested = &nested
		reportStepFinished(options, definition.Name, workflowStep.ID, "loop", index, total, outcome.stats)
	}

	loopCtx := ctx
	cancel := func() {}
	if group.Timeout != nil {
		loopCtx, cancel = context.WithTimeout(ctx, group.Timeout.Value())
	}
	defer cancel()

	started := time.Now()
	report(options, ProgressEvent{
		Kind: StepStarted, Status: StatusRunning, Time: started,
		WorkflowName: definition.Name, Depth: options.depth, StepID: workflowStep.ID,
		StepType: "loop", Index: index, Total: total, Timeout: durationValue(group.Timeout),
	})
	report(options, ProgressEvent{
		Kind: ControlStarted, Status: StatusRunning, Time: started,
		WorkflowName: definition.Name, Depth: options.depth, StepID: workflowStep.ID,
		ControlKind: "loop", MaxConcurrency: 1,
		Timeout: durationValue(group.Timeout),
	})

	nested := RunStats{}
	iterations := 0
	for iterations < group.MaxIterations {
		if err := loopCtx.Err(); err != nil {
			finish(statusFromError(err), err, nested)
			reportLoopFinished(options, definition, workflowStep, group, started, iterations, err)
			outcome.err = err
			return outcome
		}

		iterations++
		bodyStarted := time.Now()
		bodyTotal := leafStepCount(group.Steps)
		bodyStats := RunStats{StartedAt: bodyStarted, Total: bodyTotal, Steps: make([]StepStats, 0, bodyTotal)}
		bodyErr := e.executeSequence(loopCtx, definition, group.Steps, options, state, &bodyStats, 1, bodyTotal)
		bodyStats.FinishedAt = time.Now()
		bodyStats.Duration = bodyStats.FinishedAt.Sub(bodyStats.StartedAt)
		mergeRunStats(&nested, bodyStats)
		if bodyErr != nil {
			finish(statusFromError(bodyErr), bodyErr, nested)
			reportLoopFinished(options, definition, workflowStep, group, started, iterations, bodyErr)
			outcome.err = bodyErr
			return outcome
		}

		matched, err := e.evaluateCondition(group.Until, makeConditionEnvironment(definition, options.RunDir, state))
		if err != nil {
			err = fmt.Errorf("evaluating loop until: %w", err)
			finish(StatusFailed, err, nested)
			reportLoopFinished(options, definition, workflowStep, group, started, iterations, err)
			outcome.err = err
			return outcome
		}
		if matched {
			outputs := map[string]any{
				"iterations": iterations,
				"count":      iterations,
				"last":       selectedStepOutputs(state, group.Steps),
			}
			outcome.result = step.Result{Outputs: outputs}
			finish(StatusSucceeded, nil, nested)
			reportLoopFinished(options, definition, workflowStep, group, started, iterations, nil)
			return outcome
		}

		if iterations == group.MaxIterations {
			err := fmt.Errorf("loop exceeded max_iterations %d", group.MaxIterations)
			finish(StatusFailed, err, nested)
			reportLoopFinished(options, definition, workflowStep, group, started, iterations, err)
			outcome.err = err
			return outcome
		}

		delay, err := loopDelay(group.Delay, definition, options, state)
		if err != nil {
			finish(StatusFailed, err, nested)
			reportLoopFinished(options, definition, workflowStep, group, started, iterations, err)
			outcome.err = err
			return outcome
		}
		if delay > 0 {
			// No drain on the cancellation path. Since Go 1.23 timer channels are
			// unbuffered: Stop reports false only once the value has been received,
			// which is exactly when a drain would block forever. Reaching here means
			// the value was not received, so there is nothing to drain and an
			// unreferenced timer is collected.
			timer := time.NewTimer(delay)
			select {
			case <-timer.C:
			case <-loopCtx.Done():
				err := loopCtx.Err()
				finish(statusFromError(err), err, nested)
				reportLoopFinished(options, definition, workflowStep, group, started, iterations, err)
				outcome.err = err
				return outcome
			}
		}
	}
	panic("unreachable")
}

func loopDelay(delay workflow.LoopDelay, definition *workflow.Definition, options Options, state *State) (time.Duration, error) {
	if delay.Literal > 0 {
		return delay.Literal.Value(), nil
	}
	if delay.Expression == "" {
		return 0, nil
	}
	value, err := controlpkg.EvaluateExpression(delay.Expression, templateData(definition, options.RunDir, state))
	if err != nil {
		return 0, fmt.Errorf("evaluating loop delay: %w", err)
	}
	text, ok := value.(string)
	if !ok {
		return 0, fmt.Errorf("loop delay expression returned %T, want duration string", value)
	}
	parsed, err := time.ParseDuration(text)
	if err != nil || parsed <= 0 {
		if err == nil {
			err = fmt.Errorf("duration must be greater than zero")
		}
		return 0, fmt.Errorf("parsing loop delay %q: %w", text, err)
	}
	return parsed, nil
}

func reportLoopFinished(options Options, definition *workflow.Definition, workflowStep workflow.Step, group *workflow.LoopGroup, started time.Time, iterations int, err error) {
	finished := time.Now()
	report(options, ProgressEvent{
		Kind: ControlFinished, Status: statusFromError(err), Time: finished,
		WorkflowName: definition.Name, Depth: options.depth, StepID: workflowStep.ID,
		ControlKind: "loop", Iterations: iterations, Started: iterations,
		Succeeded: iterations, MaxConcurrency: 1,
		Timeout: durationValue(group.Timeout), Duration: finished.Sub(started), Error: err,
	})
	if err != nil {
		trace(options, diagnostic.Event{Phase: diagnostic.PhaseControl, Status: diagnostic.StatusFailed, Time: finished, WorkflowName: definition.Name, Error: err})
	}
}
