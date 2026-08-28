package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strconv"
	"strings"
	"time"

	"github.com/up2jj/wuko/diagnostic"
	storepkg "github.com/up2jj/wuko/keyvalue"
	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/workflow"
)

const (
	onceStoreName     = "once"
	onceRecordVersion = "wuko-once-v1"
)

func (e *Engine) validateOnce(ctx context.Context, definition *workflow.Definition, workflowStep workflow.Step, options Options, state *State) error {
	if workflowStep.If != "" {
		if _, err := compileCondition(workflowStep.If); err != nil {
			return fmt.Errorf("if: %w", err)
		}
	}
	if err := validateTemplates(options.renderer, workflowStep.Once.Key, false); err != nil {
		return fmt.Errorf("key: %w", err)
	}
	if _, err := storepkg.OpenScoped(options.LocalValueDir, options.GlobalValueDir, workflowStep.Once.Scope, onceStoreName); err != nil {
		return err
	}
	private := cloneState(state)
	private.writtenVars = make(map[string]struct{})
	childOptions := options
	childOptions.depth++
	if err := e.validateSteps(ctx, definition, workflowStep.Once.Steps, childOptions, private); err != nil {
		return fmt.Errorf("once body: %w", err)
	}
	return nil
}

func (e *Engine) executeOnce(ctx context.Context, definition *workflow.Definition, workflowStep workflow.Step, options Options, state *State, index, total int) stepOutcome {
	if err := ctx.Err(); err != nil {
		return stepOutcome{err: err}
	}
	startedAt := time.Now()
	outcome := stepOutcome{started: true}
	finish := func(status ExecutionStatus, err error, nested RunStats) {
		outcome.stats = StepStats{
			StepRunID: options.stepRunID, ID: workflowStep.ID, Type: "once", Index: index,
			Status: status, StartedAt: startedAt, Duration: time.Since(startedAt), Error: err,
		}
		outcome.nested = &nested
		reportStepFinished(options, definition.Name, workflowStep.ID, "once", index, total, outcome.stats)
	}

	conditionStarted := time.Now()
	traceStep(options, definition, workflowStep, diagnostic.PhaseCondition, diagnostic.StatusStarted, time.Time{}, string(workflowStep.If), nil)
	run, err := evaluateCondition(workflowStep.If, makeConditionEnvironment(definition, options.RunDir, state))
	if err != nil {
		stepErr := fmt.Errorf("workflow %q step %q (once): evaluating if: %w", definition.Name, workflowStep.ID, err)
		traceStep(options, definition, workflowStep, diagnostic.PhaseCondition, diagnostic.StatusFailed, conditionStarted, "", err)
		finish(StatusFailed, stepErr, RunStats{})
		outcome.err = stepErr
		return outcome
	}
	if !run {
		traceStep(options, definition, workflowStep, diagnostic.PhaseCondition, diagnostic.StatusSkipped, conditionStarted, "condition evaluated false", nil)
		finish(StatusSkipped, nil, RunStats{})
		outcome.skipped = true
		return outcome
	}
	traceStep(options, definition, workflowStep, diagnostic.PhaseCondition, diagnostic.StatusSucceeded, conditionStarted, "condition evaluated true", nil)

	key, err := options.renderer.Render(workflowStep.Once.Key, templateData(definition, options.RunDir, state))
	if err != nil {
		stepErr := fmt.Errorf("workflow %q step %q (once): rendering key: %w", definition.Name, workflowStep.ID, err)
		finish(StatusFailed, stepErr, RunStats{})
		outcome.err = stepErr
		return outcome
	}
	if strings.TrimSpace(key) == "" {
		stepErr := fmt.Errorf("workflow %q step %q (once): rendered key must not be empty", definition.Name, workflowStep.ID)
		finish(StatusFailed, stepErr, RunStats{})
		outcome.err = stepErr
		return outcome
	}
	store, err := storepkg.OpenScoped(options.LocalValueDir, options.GlobalValueDir, workflowStep.Once.Scope, onceStoreName)
	if err != nil {
		stepErr := fmt.Errorf("workflow %q step %q (once): %w", definition.Name, workflowStep.ID, err)
		finish(StatusFailed, stepErr, RunStats{})
		outcome.err = stepErr
		return outcome
	}
	if result, found, readErr := readOnceRecord(ctx, store, key); readErr != nil {
		stepErr := fmt.Errorf("workflow %q step %q (once): %w", definition.Name, workflowStep.ID, readErr)
		finish(StatusFailed, stepErr, RunStats{})
		outcome.err = stepErr
		return outcome
	} else if found {
		traceStep(options, definition, workflowStep, diagnostic.PhaseRunner, diagnostic.StatusSkipped, startedAt, "replaying recorded outcome", nil)
		outcome.result = result
		outcome.skipped = true
		outcome.commitSkipped = true
		finish(StatusSkipped, nil, RunStats{})
		return outcome
	}

	claimID := workflowStep.Once.Scope + "\x00" + key
	if _, active := options.onceClaims[claimID]; active {
		stepErr := fmt.Errorf("workflow %q step %q (once): recursive claim for key %q in %s scope", definition.Name, workflowStep.ID, key, workflowStep.Once.Scope)
		finish(StatusFailed, stepErr, RunStats{})
		outcome.err = stepErr
		return outcome
	}

	report(options, ProgressEvent{
		Kind: StepStarted, Status: StatusRunning, Time: startedAt, WorkflowName: definition.Name,
		Depth: options.depth, StepID: workflowStep.ID, StepType: "once", Index: index, Total: total,
	})
	wait := workflowStep.Once.OnBusy == workflow.OnceBusyWait
	// Announce this wait on every claim the current path already holds, so a contender
	// walking the wait-for graph sees the edge, and collect those claim paths so this
	// side sees the contender's. Whichever of the two looks second refuses; announcing
	// before looking is what stops both from missing each other and blocking.
	announce := wait && len(options.onceClaims) > 0
	held := make([]string, 0, len(options.onceClaims))
	if announce {
		claimPath := store.ClaimPath(key)
		for _, ancestor := range options.onceClaims {
			held = append(held, ancestor.Path())
			if announceErr := ancestor.SetWanted(key, claimPath); announceErr != nil {
				stepErr := fmt.Errorf("workflow %q step %q (once): %w", definition.Name, workflowStep.ID, announceErr)
				finish(StatusFailed, stepErr, RunStats{})
				outcome.err = stepErr
				return outcome
			}
		}
	}
	claim, err := store.ClaimKey(ctx, key, wait, held...)
	if announce {
		// Withdraw the edge as soon as the wait is over, however it ended, so the body
		// below does not run with ancestors still advertising a wait they finished.
		for _, ancestor := range options.onceClaims {
			_ = ancestor.SetWanted("", "")
		}
	}
	if errors.Is(err, storepkg.ErrClaimBusy) {
		result, found, readErr := readOnceRecord(ctx, store, key)
		if readErr != nil {
			stepErr := fmt.Errorf("workflow %q step %q (once): %w", definition.Name, workflowStep.ID, readErr)
			finish(StatusFailed, stepErr, RunStats{})
			outcome.err = stepErr
			return outcome
		}
		if found {
			outcome.result = result
			outcome.skipped = true
			outcome.commitSkipped = true
			finish(StatusSkipped, nil, RunStats{})
			return outcome
		}
		stepErr := fmt.Errorf("workflow %q step %q (once): key %q is busy", definition.Name, workflowStep.ID, key)
		finish(StatusFailed, stepErr, RunStats{})
		outcome.err = stepErr
		return outcome
	}
	if err != nil {
		stepErr := fmt.Errorf("workflow %q step %q (once): %w", definition.Name, workflowStep.ID, err)
		finish(statusFromError(err), stepErr, RunStats{})
		outcome.err = stepErr
		return outcome
	}
	released := false
	release := func() error {
		if released {
			return nil
		}
		released = true
		return claim.Release()
	}
	defer func() { _ = release() }()

	if result, found, readErr := readOnceRecord(ctx, store, key); readErr != nil {
		stepErr := fmt.Errorf("workflow %q step %q (once): %w", definition.Name, workflowStep.ID, readErr)
		finish(StatusFailed, stepErr, RunStats{})
		outcome.err = stepErr
		return outcome
	} else if found {
		if releaseErr := release(); releaseErr != nil {
			stepErr := fmt.Errorf("workflow %q step %q (once): releasing claim: %w", definition.Name, workflowStep.ID, releaseErr)
			finish(StatusFailed, stepErr, RunStats{})
			outcome.err = stepErr
			return outcome
		}
		outcome.result = result
		outcome.skipped = true
		outcome.commitSkipped = true
		finish(StatusSkipped, nil, RunStats{})
		return outcome
	}

	private := cloneState(state)
	private.writtenVars = make(map[string]struct{})
	childOptions := options
	childOptions.depth++
	childOptions.stepRunID = ""
	childOptions.onceClaims = maps.Clone(options.onceClaims)
	if childOptions.onceClaims == nil {
		childOptions.onceClaims = make(map[string]*storepkg.Claim)
	}
	childOptions.onceClaims[claimID] = claim
	bodyTotal := leafStepCount(workflowStep.Once.Steps)
	nested := RunStats{StartedAt: time.Now(), Total: bodyTotal, Steps: make([]StepStats, 0, bodyTotal)}
	if err := e.executeSequence(ctx, definition, workflowStep.Once.Steps, childOptions, private, &nested, 1, bodyTotal); err != nil {
		nested.FinishedAt = time.Now()
		nested.Duration = nested.FinishedAt.Sub(nested.StartedAt)
		stepErr := fmt.Errorf("workflow %q step %q (once): %w", definition.Name, workflowStep.ID, err)
		finish(statusFromError(err), stepErr, nested)
		outcome.err = stepErr
		return outcome
	}
	nested.FinishedAt = time.Now()
	nested.Duration = nested.FinishedAt.Sub(nested.StartedAt)
	variables := controlWrittenVars(private)
	result := step.Result{
		Outputs: map[string]any{
			"steps": controlStepRecords(workflowStep.Once.Steps, private, nested, false),
			"vars":  variables,
		},
		Variables: variables,
	}
	record := map[string]any{
		"version": onceRecordVersion,
		"steps":   result.Outputs["steps"],
		"vars":    result.Outputs["vars"],
	}
	stored, err := store.Set(ctx, key, record)
	if err != nil {
		stepErr := fmt.Errorf("workflow %q step %q (once): recording outcome: %w", definition.Name, workflowStep.ID, err)
		finish(StatusFailed, stepErr, nested)
		outcome.err = stepErr
		return outcome
	}
	result, err = decodeOnceRecord(key, stored)
	if err != nil {
		stepErr := fmt.Errorf("workflow %q step %q (once): recording outcome: %w", definition.Name, workflowStep.ID, err)
		finish(StatusFailed, stepErr, nested)
		outcome.err = stepErr
		return outcome
	}
	if err := release(); err != nil {
		stepErr := fmt.Errorf("workflow %q step %q (once): releasing claim: %w", definition.Name, workflowStep.ID, err)
		finish(StatusFailed, stepErr, nested)
		outcome.err = stepErr
		return outcome
	}
	outcome.result = result
	finish(StatusSucceeded, nil, nested)
	return outcome
}

func readOnceRecord(ctx context.Context, store *storepkg.Store, key string) (step.Result, bool, error) {
	value, found, err := store.Get(ctx, key)
	if err != nil || !found {
		return step.Result{}, found, err
	}
	result, err := decodeOnceRecord(key, value)
	return result, err == nil, err
}

func decodeOnceRecord(key string, value any) (step.Result, error) {
	record, ok := value.(map[string]any)
	if !ok || record["version"] != onceRecordVersion {
		return step.Result{}, fmt.Errorf("record for key %q has an unsupported format", key)
	}
	steps, stepsOK := record["steps"].(map[string]any)
	variables, varsOK := record["vars"].(map[string]any)
	if !stepsOK || !varsOK {
		return step.Result{}, fmt.Errorf("record for key %q has an invalid outcome", key)
	}
	decodedSteps, _ := onceRuntimeValue(steps).(map[string]any)
	decodedVars, _ := onceRuntimeValue(variables).(map[string]any)
	return step.Result{
		Outputs:   map[string]any{"steps": decodedSteps, "vars": decodedVars},
		Variables: decodedVars,
	}, nil
}

// onceRuntimeValue turns the json.Number values a store round-trip produces back into
// the plain numbers workflow state carries. Both the recording run and every replay
// decode through here, so a recorded outcome supports the same numeric conditions the
// body's own variables did.
func onceRuntimeValue(value any) any {
	switch typed := value.(type) {
	case json.Number:
		if integer, err := typed.Int64(); err == nil {
			return integer
		}
		if unsigned, err := strconv.ParseUint(string(typed), 10, 64); err == nil {
			return unsigned
		}
		number, err := typed.Float64()
		if err != nil {
			// A magnitude no Go number holds still has to leave as a JSON shape,
			// because json.Number is not one of the types workflow state carries.
			return typed.String()
		}
		return number
	case []any:
		converted := make([]any, len(typed))
		for i, item := range typed {
			converted[i] = onceRuntimeValue(item)
		}
		return converted
	case map[string]any:
		converted := make(map[string]any, len(typed))
		for name, item := range typed {
			converted[name] = onceRuntimeValue(item)
		}
		return converted
	default:
		return value
	}
}
