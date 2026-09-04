package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/expr-lang/expr"
	"github.com/expr-lang/expr/vm"
	controlpkg "github.com/up2jj/wuko/control"
	"github.com/up2jj/wuko/diagnostic"
	wukoexpr "github.com/up2jj/wuko/expression"
	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/workflow"
)

// compileAttemptCondition compiles an until expression, which sees the ordinary condition roots
// plus the one-based poll counter. It has no error root: until is reached only after a pass has
// already succeeded, so failure eligibility belongs to when instead.
func (e *Engine) compileAttemptCondition(condition workflow.Condition) (*vm.Program, error) {
	shape := e.conditionEnvironmentShape()
	shape["poll"] = 0
	program, err := wukoexpr.Compile(string(condition), expr.Env(shape), expr.AsBool())
	if err != nil {
		return nil, fmt.Errorf("compiling until: %w", err)
	}
	return program, nil
}

func (e *Engine) validateAttempt(ctx context.Context, definition *workflow.Definition, workflowStep workflow.Step, options Options, state *State) error {
	control := workflowStep.Attempt
	if err := control.Validate(); err != nil {
		return err
	}
	if control.IsDelay() {
		return nil
	}
	if control.Until != "" {
		if _, err := e.compileAttemptCondition(control.Until); err != nil {
			return err
		}
	}
	if control.When != "" {
		if _, err := e.compileCondition(control.When); err != nil {
			return fmt.Errorf("compiling attempt when: %w", err)
		}
	}
	if control.OperationID != "" {
		if err := validateTemplates(options.renderer, control.OperationID, false); err != nil {
			return fmt.Errorf("attempt operation_id: %w", err)
		}
	}
	childOptions := options
	childOptions.depth++
	return e.validateSteps(ctx, definition, control.Steps, childOptions, cloneState(state))
}

func (e *Engine) executeAttempt(ctx context.Context, definition *workflow.Definition, workflowStep workflow.Step, options Options, state *State, index, total int) stepOutcome {
	if err := ctx.Err(); err != nil {
		return stepOutcome{err: err}
	}
	control := workflowStep.Attempt
	startedAt := time.Now()
	outcome := stepOutcome{started: true}
	var attemptStats []AttemptStats
	var retryWait, pollWait time.Duration
	polls := 0
	finish := func(status ExecutionStatus, err error, nested RunStats) {
		outcome.stats = StepStats{
			StepRunID: options.stepRunID, ID: workflowStep.ID, Type: "attempt", Index: index,
			Status: status, StartedAt: startedAt, Duration: time.Since(startedAt), Error: err,
			Attempts: attemptStats, RetryWait: retryWait, Polls: polls, PollWait: pollWait,
		}
		outcome.nested = &nested
		reportStepFinished(options, definition.Name, workflowStep.ID, "attempt", index, total, outcome.stats)
	}
	fail := func(err error, nested RunStats) stepOutcome {
		stepErr := fmt.Errorf("workflow %q step %q (attempt): %w", definition.Name, workflowStep.ID, err)
		finish(statusFromError(err), stepErr, nested)
		outcome.err = stepErr
		return outcome
	}

	conditionStarted := time.Now()
	traceStep(options, definition, workflowStep, diagnostic.PhaseCondition, diagnostic.StatusStarted, time.Time{}, string(workflowStep.If), nil)
	run, err := e.evaluateCondition(workflowStep.If, makeConditionEnvironment(definition, options.RunDir, state))
	if err != nil {
		traceStep(options, definition, workflowStep, diagnostic.PhaseCondition, diagnostic.StatusFailed, conditionStarted, "", err)
		return fail(fmt.Errorf("evaluating if: %w", err), RunStats{})
	}
	if !run {
		traceStep(options, definition, workflowStep, diagnostic.PhaseCondition, diagnostic.StatusSkipped, conditionStarted, "condition evaluated false", nil)
		finish(StatusSkipped, nil, RunStats{})
		outcome.skipped = true
		return outcome
	}
	traceStep(options, definition, workflowStep, diagnostic.PhaseCondition, diagnostic.StatusSucceeded, conditionStarted, "condition evaluated true", nil)

	policy, err := control.Resolve(func(expression string) (any, error) {
		return controlpkg.EvaluateExpression(expression, templateData(definition, options.RunDir, state))
	})
	if err != nil {
		return fail(err, RunStats{})
	}

	report(options, ProgressEvent{
		Kind: StepStarted, Status: StatusRunning, Time: startedAt, WorkflowName: definition.Name,
		Depth: options.depth, StepID: workflowStep.ID, StepType: "attempt", Index: index, Total: total,
		MaxAttempts: policy.MaxAttempts, Timeout: policy.Timeout,
	})

	if control.IsDelay() {
		timer := time.NewTimer(policy.Duration)
		defer timer.Stop()
		select {
		case <-timer.C:
			outcome.result = step.Result{Outputs: map[string]any{}}
			finish(StatusSucceeded, nil, RunStats{})
			return outcome
		case <-ctx.Done():
			return fail(ctx.Err(), RunStats{})
		}
	}

	operationID, err := attemptOperationID(definition, workflowStep, options, state)
	if err != nil {
		traceStep(options, definition, workflowStep, diagnostic.PhaseAttempt, diagnostic.StatusFailed, time.Time{}, "preparing operation", err)
		return fail(err, RunStats{})
	}
	var untilProgram *vm.Program
	if control.Until != "" {
		if untilProgram, err = e.compileAttemptCondition(control.Until); err != nil {
			return fail(err, RunStats{})
		}
	}
	var whenProgram *vm.Program
	if control.When != "" {
		if whenProgram, err = e.compileCondition(control.When); err != nil {
			return fail(fmt.Errorf("compiling attempt when: %w", err), RunStats{})
		}
	}

	runCtx := ctx
	cancelRun := func() {}
	if policy.MaxElapsedTime > 0 {
		runCtx, cancelRun = context.WithTimeout(ctx, policy.MaxElapsedTime)
	}
	defer cancelRun()

	// Resources belong to the pass that created them. A discarded pass releases its own; only
	// the winning pass promotes them to the scope that owns the attempt.
	parentScope := options.cleanupScope()
	bodyTotal := leafStepCount(control.Steps)
	nested := RunStats{StartedAt: startedAt, Total: bodyTotal, Steps: make([]StepStats, 0, bodyTotal)}
	childOptions := options
	childOptions.depth++
	childOptions.stepRunID = ""
	childOptions.operationID = operationID
	childOptions.maxAttempts = policy.MaxAttempts

	attempt := 1
	var previousPass map[string]step.Result
	for {
		if err := executionContextError(ctx, runCtx, policy.MaxElapsedTime); err != nil {
			return fail(err, nested)
		}
		passCtx := runCtx
		cancelPass := func() {}
		if policy.HasTimeout {
			passCtx, cancelPass = context.WithTimeout(runCtx, policy.Timeout)
		}
		passOptions := childOptions
		passOptions.attempt = attempt
		passOptions.previousPass = previousPass
		failure := &passFailure{}
		passOptions.passFailure = failure
		passScope := &cleanupScope{}
		passOptions.cleanups = passScope
		// Cleanup must still run after Ctrl-C, so it is detached from the pass context.
		releasePass := func() error {
			return errors.Join(passScope.run(context.WithoutCancel(ctx))...)
		}

		private := cloneState(state)
		private.writtenVars = make(map[string]struct{})
		passStarted := time.Now()
		report(options, ProgressEvent{
			Kind: AttemptStarted, Status: StatusRunning, Time: passStarted, WorkflowName: definition.Name,
			Depth: options.depth, StepID: workflowStep.ID, StepType: "attempt",
			Attempt: attempt, MaxAttempts: policy.MaxAttempts,
		})
		traceStep(options, definition, workflowStep, diagnostic.PhaseAttempt, diagnostic.StatusStarted, time.Time{}, "executing attempt body", nil, attemptAttr(attempt, policy.MaxAttempts))

		passStats := RunStats{StartedAt: passStarted, Total: bodyTotal, Steps: make([]StepStats, 0, bodyTotal)}
		passErr := e.executeSequence(passCtx, definition, control.Steps, passOptions, private, &passStats, 1, bodyTotal)
		passContextErr := passCtx.Err()
		cancelPass()
		passStats.FinishedAt = time.Now()
		passStats.Duration = passStats.FinishedAt.Sub(passStats.StartedAt)
		mergeRunStats(&nested, passStats)

		if contextErr := executionContextError(ctx, runCtx, policy.MaxElapsedTime); contextErr != nil {
			passErr = contextErr
		} else if passContextErr == context.DeadlineExceeded && policy.HasTimeout {
			passErr = attemptTimeoutError{duration: policy.Timeout, cause: passErr}
		}
		stats := AttemptStats{
			Number: attempt, Status: statusFromError(passErr), StartedAt: passStarted,
			Duration: time.Since(passStarted), Error: passErr,
		}
		// Only a pass that spends the failure budget is an attempt. A pass that succeeded but
		// left until false is a poll: it is counted by Polls, and recording it here would report
		// phantom retries for an ordinary wait.
		recordAttempt := func() { attemptStats = append(attemptStats, stats) }
		diagnosticStatus := diagnostic.StatusSucceeded
		if passErr != nil {
			diagnosticStatus = diagnostic.StatusFailed
		}
		traceStep(options, definition, workflowStep, diagnostic.PhaseAttempt, diagnosticStatus, passStarted, "", passErr, attemptAttr(attempt, policy.MaxAttempts))
		report(options, ProgressEvent{
			Kind: AttemptFinished, Status: stats.Status, Time: passStarted.Add(stats.Duration),
			WorkflowName: definition.Name, Depth: options.depth, StepID: workflowStep.ID, StepType: "attempt",
			Attempt: attempt, MaxAttempts: policy.MaxAttempts, Duration: stats.Duration, Error: passErr,
		})

		if passErr != nil {
			if releaseErr := releasePass(); releaseErr != nil {
				passErr = errors.Join(passErr, releaseErr)
			}
		}

		if passErr == nil {
			if untilProgram == nil {
				recordAttempt()
				passScope.adopt(parentScope)
				outcome.result = attemptResult(control, private, attempt, polls)
				finish(StatusSucceeded, nil, nested)
				return outcome
			}
			polls++
			report(options, ProgressEvent{
				Kind: PollStarted, Status: StatusRunning, Time: time.Now(), WorkflowName: definition.Name,
				Depth: options.depth, StepID: workflowStep.ID, StepType: "attempt", Poll: polls,
			})
			environment := makeConditionEnvironment(definition, options.RunDir, private)
			environment["poll"] = polls
			matched, err := evaluateConditionProgram(untilProgram, environment)
			if err != nil {
				report(options, ProgressEvent{
					Kind: PollFinished, Status: StatusFailed, Time: time.Now(), WorkflowName: definition.Name,
					Depth: options.depth, StepID: workflowStep.ID, StepType: "attempt", Poll: polls, Error: err,
				})
				recordAttempt()
				if releaseErr := releasePass(); releaseErr != nil {
					err = errors.Join(err, releaseErr)
				}
				return fail(fmt.Errorf("evaluating attempt until: %w", err), nested)
			}
			pollStatus := StatusRunning
			if matched {
				pollStatus = StatusSucceeded
			}
			report(options, ProgressEvent{
				Kind: PollFinished, Status: pollStatus, Time: time.Now(), WorkflowName: definition.Name,
				Depth: options.depth, StepID: workflowStep.ID, StepType: "attempt", Poll: polls, Matched: matched,
			})
			traceStep(options, definition, workflowStep, diagnostic.PhasePoll, diagnostic.StatusDetail, passStarted,
				attemptPollMessage(matched), nil, diagnostic.Attr("poll", fmt.Sprint(polls)))
			if matched {
				recordAttempt()
				passScope.adopt(parentScope)
				outcome.result = attemptResult(control, private, attempt, polls)
				finish(StatusSucceeded, nil, nested)
				return outcome
			}
			// A pass that succeeded without reaching readiness costs no attempt, but its
			// resources are still discarded with it. A leaked release is not recoverable, so
			// it ends the control rather than polling on.
			if releaseErr := releasePass(); releaseErr != nil {
				recordAttempt()
				return fail(releaseErr, nested)
			}
			previousPass = passResults(control.Steps, private, failure)
			report(options, ProgressEvent{
				Kind: PollScheduled, Status: StatusRunning, Time: time.Now(), WorkflowName: definition.Name,
				Depth: options.depth, StepID: workflowStep.ID, StepType: "attempt",
				Poll: polls + 1, PollDelay: policy.Interval,
			})
			waitStarted := time.Now()
			if err := waitForRetry(runCtx, policy.Interval); err != nil {
				pollWait += time.Since(waitStarted)
				return fail(executionContextError(ctx, runCtx, policy.MaxElapsedTime), nested)
			}
			pollWait += time.Since(waitStarted)
			continue
		}

		recordAttempt()
		if ctx.Err() != nil || runCtx.Err() != nil {
			return fail(passErr, nested)
		}
		if attempt == policy.MaxAttempts {
			return fail(fmt.Errorf("attempt %d/%d failed: %w", attempt, policy.MaxAttempts, passErr), nested)
		}
		if whenProgram != nil {
			environment := makeConditionEnvironment(definition, options.RunDir, state)
			environment["error"] = attemptErrorValue(control, workflowStep.ID, passErr, private, failure, attempt, polls)
			retry, err := evaluateConditionProgram(whenProgram, environment)
			if err != nil {
				return fail(fmt.Errorf("evaluating attempt when after %v: %w", passErr, err), nested)
			}
			if !retry {
				return fail(passErr, nested)
			}
		} else if !retryableError(control, passErr) {
			return fail(passErr, nested)
		}
		previousPass = passResults(control.Steps, private, failure)

		delay := retryDelayForError(policy, attempt, passErr)
		traceStep(options, definition, workflowStep, diagnostic.PhaseRetry, diagnostic.StatusDetail, time.Time{}, "retry scheduled", nil,
			attemptAttr(attempt+1, policy.MaxAttempts), diagnostic.Attr("delay", delay.String()))
		report(options, ProgressEvent{
			Kind: RetryScheduled, Status: StatusRunning, Time: time.Now(), WorkflowName: definition.Name,
			Depth: options.depth, StepID: workflowStep.ID, StepType: "attempt",
			Attempt: attempt + 1, MaxAttempts: policy.MaxAttempts, RetryDelay: delay, Error: passErr,
		})
		waitStarted := time.Now()
		if err := waitForRetry(runCtx, delay); err != nil {
			retryWait += time.Since(waitStarted)
			return fail(executionContextError(ctx, runCtx, policy.MaxElapsedTime), nested)
		}
		retryWait += time.Since(waitStarted)
		attempt++
	}
}

// passFailure captures the partial result of the step that ended a pass. A failed step never
// commits, so its outputs are absent from the pass state -- yet error.exit_code and friends are
// exactly what a when expression needs. executeStep records them here on its way out.
type passFailure struct {
	mu      sync.Mutex
	stepID  string
	outputs map[string]any
}

func (failure *passFailure) record(stepID string, outputs map[string]any) {
	if failure == nil {
		return
	}
	failure.mu.Lock()
	defer failure.mu.Unlock()
	// Last write wins: a concurrent group inside the body may report several failures, and any
	// of them fairly describes why the pass ended.
	failure.stepID, failure.outputs = stepID, outputs
}

func (failure *passFailure) read() (string, map[string]any) {
	if failure == nil {
		return "", nil
	}
	failure.mu.Lock()
	defer failure.mu.Unlock()
	return failure.stepID, failure.outputs
}

func attemptPollMessage(matched bool) string {
	if matched {
		return "condition matched"
	}
	return "condition did not match"
}

// attemptResult publishes the winning pass. The body is isolated, so its step outputs and
// variable writes reach the workflow only through this value -- never as top-level steps or
// vars entries. Variables is deliberately empty for the same reason.
func attemptResult(control *workflow.AttemptControl, private *State, attempts, polls int) step.Result {
	return step.Result{Outputs: map[string]any{
		"attempts": attempts,
		"polls":    polls,
		"steps":    selectedStepOutputs(private, control.Steps),
		"vars":     controlWrittenVars(private),
	}}
}

// passResults collects what each body step produced during one pass, so the next pass can hand
// each step its own PreviousAttempt.
func passResults(steps []workflow.Step, private *State, failure *passFailure) map[string]step.Result {
	outputs := selectedStepOutputs(private, steps)
	failedStep, failedOutputs := failure.read()
	if len(outputs) == 0 && failedStep == "" {
		return nil
	}
	results := make(map[string]step.Result, len(outputs)+1)
	for id, value := range outputs {
		if mapping, ok := value.(map[string]any); ok {
			results[id] = step.Result{Outputs: mapping}
		}
	}
	if failedStep != "" && failedOutputs != nil {
		results[failedStep] = step.Result{Outputs: failedOutputs}
	}
	return results
}

// attemptErrorValue describes a failed pass. The failing body step's outputs still flatten to the
// top, so error.exit_code keeps working and a one-step body reads exactly as a bare step did
// before the unification. The pass context sits alongside under reserved names.
func attemptErrorValue(control *workflow.AttemptControl, stepID string, err error, private *State, failure *passFailure, attempts, polls int) map[string]any {
	failedStep, failedOutputs := failure.read()
	value := retryErrorValue(statusFromError(err), stepID, "attempt", err, retryConditionOutputs(err, failedOutputs))
	value["failed_step"] = failedStep
	value["steps"] = selectedStepOutputs(private, control.Steps)
	value["vars"] = controlWrittenVars(private)
	value["attempts"] = attempts
	value["polls"] = polls
	return value
}

func attemptOperationID(definition *workflow.Definition, workflowStep workflow.Step, options Options, state *State) (string, error) {
	template := ""
	if workflowStep.Attempt != nil {
		template = workflowStep.Attempt.OperationID
	}
	return executionOperationID(definition, workflowStep.ID, template, options, state)
}

// attemptPolicyDescription renders the declared policy for dry runs and the tree view. It reads
// the declaration rather than a resolved policy: nothing has run yet, so an expression-backed
// option is shown as its expression.
func attemptPolicyDescription(control *workflow.AttemptControl) string {
	if control == nil {
		return ""
	}
	if control.IsDelay() {
		return "duration " + attemptOptionText(control.Duration)
	}
	var parts []string
	if control.Timeout.Set() {
		parts = append(parts, "timeout "+attemptOptionText(control.Timeout))
	}
	if control.MaxAttempts.Expression != "" {
		parts = append(parts, control.MaxAttempts.Expression+" attempts")
	} else if control.MaxAttempts.Literal > 1 {
		parts = append(parts, fmt.Sprintf("%d attempts", control.MaxAttempts.Literal))
	}
	if control.Until != "" {
		parts = append(parts, "poll every "+attemptOptionText(control.Interval))
	}
	if control.MaxElapsedTime.Set() {
		parts = append(parts, "within "+attemptOptionText(control.MaxElapsedTime))
	}
	return strings.Join(parts, ", ")
}

func attemptOptionText(option workflow.AttemptDuration) string {
	if option.Expression != "" {
		return option.Expression
	}
	return option.Literal.String()
}
