package engine

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"time"

	"github.com/up2jj/wuko/diagnostic"
	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/workflow"
)

func (e *Engine) validateTryCatch(ctx context.Context, definition *workflow.Definition, workflowStep workflow.Step, options Options, state *State) error {
	if workflowStep.If != "" {
		if _, err := compileCondition(workflowStep.If); err != nil {
			return fmt.Errorf("if: %w", err)
		}
	}
	private := cloneState(state)
	childOptions := options
	childOptions.depth += 2
	if err := e.validateSteps(ctx, definition, workflowStep.Try.Steps, childOptions, private); err != nil {
		return fmt.Errorf("try: %w", err)
	}
	if private.Bindings == nil {
		private.Bindings = make(map[string]any)
	}
	private.Bindings["error"] = recoveryErrorValue(StatusFailed, errors.New("validation failure"), nil)
	if err := e.validateSteps(ctx, definition, workflowStep.Catch.Steps, childOptions, private); err != nil {
		return fmt.Errorf("catch: %w", err)
	}
	return nil
}

func (e *Engine) executeTryCatch(ctx context.Context, definition *workflow.Definition, workflowStep workflow.Step, options Options, state *State, index, total int) stepOutcome {
	if err := ctx.Err(); err != nil {
		return stepOutcome{err: err}
	}
	startedAt := time.Now()
	outcome := stepOutcome{started: true}
	finish := func(status ExecutionStatus, err error, phases []IterationStats, nested RunStats) {
		outcome.stats = StepStats{
			StepRunID: options.stepRunID, ID: workflowStep.ID, Type: "try", Index: index,
			Status: status, StartedAt: startedAt, Duration: time.Since(startedAt), Error: err,
			Iterations: phases,
		}
		outcome.nested = &nested
		reportStepFinished(options, definition.Name, workflowStep.ID, "try", index, total, outcome.stats)
		if len(phases) == 0 {
			return
		}
		report(options, ProgressEvent{
			Kind: ControlFinished, Status: status, Time: time.Now(), WorkflowName: definition.Name,
			Depth: options.depth, StepID: workflowStep.ID, ControlKind: "try", Iterations: len(phases),
			Started: startedTryCatchPhases(phases), Succeeded: succeededTryCatchPhases(phases),
			Duration: time.Since(startedAt), Error: err,
		})
	}

	conditionStarted := time.Now()
	traceStep(options, definition, workflowStep, diagnostic.PhaseCondition, diagnostic.StatusStarted, time.Time{}, string(workflowStep.If), nil)
	run, err := evaluateCondition(workflowStep.If, makeConditionEnvironment(definition, options.RunDir, state))
	if err != nil {
		stepErr := fmt.Errorf("workflow %q step %q (try): evaluating if: %w", definition.Name, workflowStep.ID, err)
		traceStep(options, definition, workflowStep, diagnostic.PhaseCondition, diagnostic.StatusFailed, conditionStarted, "", err)
		finish(StatusFailed, stepErr, nil, RunStats{})
		outcome.err = stepErr
		return outcome
	}
	if !run {
		traceStep(options, definition, workflowStep, diagnostic.PhaseCondition, diagnostic.StatusSkipped, conditionStarted, "condition evaluated false", nil)
		finish(StatusSkipped, nil, nil, RunStats{})
		outcome.skipped = true
		return outcome
	}
	traceStep(options, definition, workflowStep, diagnostic.PhaseCondition, diagnostic.StatusSucceeded, conditionStarted, "condition evaluated true", nil)
	report(options, ProgressEvent{
		Kind: StepStarted, Status: StatusRunning, Time: startedAt, WorkflowName: definition.Name,
		Depth: options.depth, StepID: workflowStep.ID, StepType: "try", Index: index, Total: total,
	})
	report(options, ProgressEvent{
		Kind: ControlStarted, Status: StatusRunning, Time: startedAt, WorkflowName: definition.Name,
		Depth: options.depth, StepID: workflowStep.ID, ControlKind: "try", Iterations: 3, MaxConcurrency: 1,
	})

	private := cloneState(state)
	private.writtenVars = make(map[string]struct{})
	localSteps := append(append([]workflow.Step(nil), workflowStep.Try.Steps...), workflowStep.Catch.Steps...)
	localDefers := newDeferStack(localSteps)
	childOptions := options
	childOptions.depth += 2
	childOptions.stepRunID = ""
	childOptions.defers = localDefers

	tryStats, tryErr := e.executeTryPhase(ctx, definition, workflowStep.Try.Steps, childOptions, private)
	tryVars := controlWrittenVars(private)
	allWritten := maps.Clone(private.writtenVars)
	private.writtenVars = make(map[string]struct{})

	var catchStats RunStats
	catchStatus := StatusSkipped
	var catchErr error
	recovered := false
	var originalError any
	if shouldCatch(ctx, tryErr) {
		originalError = recoveryErrorValue(statusFromError(tryErr), tryErr, tryStats.Steps)
		restoreError := bindRecoveryError(private, originalError)
		catchStats, catchErr = e.executeCatchPhase(ctx, definition, workflowStep.Catch.Steps, childOptions, private)
		restoreError()
		catchStatus = catchPhaseStatus(catchErr, catchStats)
		// A catch whose every entry was skipped by its own condition never attempted a
		// recovery, so the original failure stands instead of being silently discarded.
		recovered = catchErr == nil && catchStatus != StatusSkipped
	} else {
		catchStats = skippedPhaseStats(definition, workflowStep.Catch.Steps, childOptions)
	}
	catchVars := controlWrittenVars(private)
	maps.Copy(allWritten, private.writtenVars)
	private.writtenVars = make(map[string]struct{})

	effectiveErr := tryErr
	if recovered {
		effectiveErr = nil
	} else if catchErr != nil {
		if ctx.Err() != nil || errors.Is(catchErr, context.Canceled) {
			effectiveErr = catchErr
		} else {
			effectiveErr = errors.Join(tryErr, catchErr)
		}
	}

	var cleanupMainStats []StepStats
	if effectiveErr != nil {
		cleanupMainStats = append(append([]StepStats(nil), tryStats.Steps...), catchStats.Steps...)
	}
	cleanupStats, cleanupErrors := e.executeTryCatchCleanup(ctx, definition, localDefers, childOptions, private, effectiveErr, cleanupMainStats)
	cleanupVars := controlWrittenVars(private)
	maps.Copy(allWritten, private.writtenVars)
	cleanupErr := errors.Join(cleanupErrors...)
	if effectiveErr == nil && cleanupErr != nil {
		effectiveErr = cleanupErr
	} else if effectiveErr != nil && cleanupErr != nil {
		effectiveErr = errors.Join(effectiveErr, cleanupErr)
	}

	phases := []IterationStats{
		phaseIteration(0, "try", statusFromError(tryErr), tryErr, tryStats),
		phaseIteration(1, "catch", catchStatus, catchErr, catchStats),
		phaseIteration(2, "cleanup", cleanupPhaseStatus(localDefers, cleanupErr, cleanupStats), cleanupErr, cleanupStats),
	}
	nested := RunStats{}
	for _, stats := range []RunStats{tryStats, catchStats, cleanupStats} {
		rollupNestedMetrics(&nested, stats)
	}
	if effectiveErr != nil {
		stepErr := fmt.Errorf("workflow %q step %q (try): %w", definition.Name, workflowStep.ID, effectiveErr)
		finish(statusFromError(effectiveErr), stepErr, phases, nested)
		outcome.err = stepErr
		return outcome
	}

	variables := make(map[string]any, len(allWritten))
	for name := range allWritten {
		variables[name] = cloneAny(private.Vars[name])
	}
	outputs := map[string]any{
		"ok": true, "recovered": recovered, "status": string(StatusSucceeded), "error": nil,
		"try":   phaseOutput(statusFromError(tryErr), originalError, workflowStep.Try.Steps, private, tryStats, tryVars),
		"catch": phaseOutput(catchStatus, nil, workflowStep.Catch.Steps, private, catchStats, catchVars),
		"cleanup": map[string]any{
			"status": string(cleanupPhaseStatus(localDefers, nil, cleanupStats)), "error": nil,
			"steps": controlStepRecords(deferredDeclarations(localDefers), private, cleanupStats, false), "vars": cleanupVars,
		},
		"vars": controlVariables(private, allWritten),
	}
	outcome.result = step.Result{Outputs: outputs, Variables: variables}
	finish(StatusSucceeded, nil, phases, nested)
	return outcome
}

func (e *Engine) executeTryPhase(ctx context.Context, definition *workflow.Definition, steps []workflow.Step, options Options, state *State) (RunStats, error) {
	started := time.Now()
	total := leafStepCount(steps)
	stats := RunStats{StartedAt: started, Total: total, Steps: make([]StepStats, 0, total)}
	err := e.executeSequence(ctx, definition, steps, options, state, &stats, 1, total)
	stats.FinishedAt = time.Now()
	stats.Duration = stats.FinishedAt.Sub(started)
	return stats, err
}

func (e *Engine) executeCatchPhase(ctx context.Context, definition *workflow.Definition, steps []workflow.Step, options Options, state *State) (RunStats, error) {
	started := time.Now()
	total := leafStepCount(steps)
	stats := RunStats{StartedAt: started, Total: total, Steps: make([]StepStats, 0, total)}
	var phaseErrors []error
	index := 1
	for position, declaration := range steps {
		err := e.executeSequence(ctx, definition, []workflow.Step{declaration}, options, state, &stats, index, total)
		if err != nil {
			phaseErrors = append(phaseErrors, err)
			if ctx.Err() != nil || errors.Is(err, context.Canceled) {
				recordSkippedSteps(definition, steps[position+1:], options, &stats, index+leafStepCount([]workflow.Step{declaration}), total)
				break
			}
		}
		index += leafStepCount([]workflow.Step{declaration})
	}
	stats.FinishedAt = time.Now()
	stats.Duration = stats.FinishedAt.Sub(started)
	return stats, errors.Join(phaseErrors...)
}

func (e *Engine) executeTryCatchCleanup(ctx context.Context, definition *workflow.Definition, stack *deferStack, options Options, state *State, mainErr error, mainStats []StepStats) (RunStats, []error) {
	started := time.Now()
	total := stack.stepCount()
	stats := RunStats{StartedAt: started, Total: total, Steps: make([]StepStats, 0, total)}
	cleanupErrors := e.executeCleanupScope(context.WithoutCancel(ctx), definition, stack, nil, options, state, &stats, mainErr, mainStats, 1, total)
	stats.FinishedAt = time.Now()
	stats.Duration = stats.FinishedAt.Sub(started)
	return stats, cleanupErrors
}

func skippedPhaseStats(definition *workflow.Definition, steps []workflow.Step, options Options) RunStats {
	started := time.Now()
	total := leafStepCount(steps)
	stats := RunStats{StartedAt: started, Total: total, Steps: make([]StepStats, 0, total)}
	recordSkippedSteps(definition, steps, options, &stats, 1, total)
	stats.FinishedAt = time.Now()
	stats.Duration = stats.FinishedAt.Sub(started)
	return stats
}

func shouldCatch(ctx context.Context, err error) bool {
	if err == nil || ctx.Err() != nil {
		return false
	}
	status := statusFromError(err)
	return status == StatusFailed || status == StatusTimedOut
}

func bindRecoveryError(state *State, value any) func() {
	bindingsWereNil := state.Bindings == nil
	if state.Bindings == nil {
		state.Bindings = make(map[string]any)
	}
	previous, hadPrevious := state.Bindings["error"]
	state.Bindings["error"] = value
	return func() {
		if hadPrevious {
			state.Bindings["error"] = previous
		} else {
			delete(state.Bindings, "error")
		}
		if bindingsWereNil && len(state.Bindings) == 0 {
			state.Bindings = nil
		}
	}
}

func recoveryErrorValue(status ExecutionStatus, err error, stats []StepStats) map[string]any {
	records := finallyErrorRecords(stats)
	converted := make([]any, 0, len(records))
	stepID, stepType := "", ""
	for _, value := range records {
		record := value.(map[string]any)
		item := map[string]any{
			"status": record["status"], "message": record["message"],
			"step": record["step_id"], "type": record["step_type"],
		}
		if stepID == "" {
			stepID, _ = item["step"].(string)
			stepType, _ = item["type"].(string)
		}
		converted = append(converted, item)
	}
	if len(converted) == 0 {
		converted = append(converted, map[string]any{"status": string(status), "message": err.Error(), "step": "", "type": ""})
	}
	return structuredErrorValue(status, err, stepID, stepType, converted)
}

func structuredErrorValue(status ExecutionStatus, err error, stepID, stepType string, records []any) map[string]any {
	return map[string]any{
		"status": string(status), "message": err.Error(), "step": stepID, "type": stepType, "errors": records,
	}
}

var retryErrorReservedFields = map[string]struct{}{
	"status": {}, "message": {}, "step": {}, "type": {}, "errors": {}, "outputs": {},
}

func retryErrorValue(status ExecutionStatus, stepID, stepType string, err error, outputs map[string]any) map[string]any {
	record := map[string]any{
		"status": string(status), "message": err.Error(), "step": stepID, "type": stepType,
	}
	value := structuredErrorValue(status, err, stepID, stepType, []any{record})
	value["outputs"] = cloneMap(outputs)
	for name, output := range outputs {
		if _, reserved := retryErrorReservedFields[name]; reserved {
			continue
		}
		value[name] = cloneAny(output)
	}
	return value
}

func phaseOutput(status ExecutionStatus, err any, declarations []workflow.Step, state *State, stats RunStats, vars map[string]any) map[string]any {
	return map[string]any{
		"status": string(status), "error": err,
		"steps": controlStepRecords(declarations, state, stats, false), "vars": vars,
	}
}

func phaseIteration(index int, label string, status ExecutionStatus, err error, stats RunStats) IterationStats {
	return IterationStats{Index: index, Label: label, Status: status, StartedAt: stats.StartedAt, Duration: stats.Duration, Error: err, Steps: stats.Steps}
}

func catchPhaseStatus(err error, stats RunStats) ExecutionStatus {
	if err == nil && allStepsSkipped(stats) {
		return StatusSkipped
	}
	return statusFromError(err)
}

func cleanupPhaseStatus(stack *deferStack, err error, stats RunStats) ExecutionStatus {
	if stack == nil || stack.stepCount() == 0 {
		return StatusSkipped
	}
	if err == nil && allStepsSkipped(stats) {
		return StatusSkipped
	}
	return statusFromError(err)
}

func deferredDeclarations(stack *deferStack) []workflow.Step {
	if stack == nil {
		return nil
	}
	var declarations []workflow.Step
	for index := len(stack.groups) - 1; index >= 0; index-- {
		declarations = append(declarations, stack.groups[index].steps...)
	}
	return declarations
}

func controlVariables(state *State, names map[string]struct{}) map[string]any {
	result := make(map[string]any, len(names))
	for name := range names {
		result[name] = cloneAny(state.Vars[name])
	}
	return result
}

func startedTryCatchPhases(phases []IterationStats) int {
	started := 0
	for _, phase := range phases {
		if phase.Status != StatusSkipped {
			started++
		}
	}
	return started
}

func succeededTryCatchPhases(phases []IterationStats) int {
	succeeded := 0
	for _, phase := range phases {
		if phase.Status == StatusSucceeded {
			succeeded++
		}
	}
	return succeeded
}
