package engine

import (
	"context"
	"errors"
	"fmt"
	"time"

	controlpkg "github.com/up2jj/wuko/control"
	"github.com/up2jj/wuko/diagnostic"
	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/workflow"
)

type cancelOnParticipant struct {
	declaration []workflow.Step
	state       *State
	stats       RunStats
	err         error
	skipped     bool
	kind        string
	label       string
}

func (e *Engine) validateCancelOn(ctx context.Context, definition *workflow.Definition, workflowStep workflow.Step, options Options, state *State) error {
	group := workflowStep.CancelOn
	if workflowStep.If != "" {
		if _, err := e.compileCondition(workflowStep.If); err != nil {
			return fmt.Errorf("if: %w", err)
		}
	}
	if group.Collect != "" {
		if err := controlpkg.ValidateExpression(group.Collect); err != nil {
			return fmt.Errorf("cancel_on collect: %w", err)
		}
	}
	// Every participant validates against the same pre-control state, and a validator that
	// needs to mutate clones for itself first (see validateTryCatch), so one copy serves them
	// all: cloning per monitor would deep-copy the whole state once per participant.
	private := cloneState(state)
	monitorOptions := options
	monitorOptions.depth += 2
	monitorOptions.Interactive = false
	monitorOptions.Stdin = nil
	for index, monitor := range group.Monitors {
		declaration := group.MonitorDeclaration(index)
		if err := e.validateSteps(ctx, definition, []workflow.Step{declaration}, monitorOptions, private); err != nil {
			return fmt.Errorf("cancel_on monitor %q: %w", monitor.ID, err)
		}
	}
	bodyOptions := options
	bodyOptions.depth += 2
	if err := e.validateSteps(ctx, definition, group.Steps, bodyOptions, private); err != nil {
		return fmt.Errorf("cancel_on body: %w", err)
	}
	return nil
}

func (e *Engine) executeCancelOn(ctx context.Context, definition *workflow.Definition, workflowStep workflow.Step, options Options, state *State, index, total int) stepOutcome {
	if err := ctx.Err(); err != nil {
		return stepOutcome{err: err}
	}
	group := workflowStep.CancelOn
	startedAt := time.Now()
	outcome := stepOutcome{started: true}
	finish := func(status ExecutionStatus, err error, participants []IterationStats, nested RunStats) {
		outcome.stats = StepStats{
			StepRunID: options.stepRunID, ID: workflowStep.ID, Type: "cancel_on", Index: index,
			Status: status, StartedAt: startedAt, Duration: time.Since(startedAt), Error: err,
			Iterations: participants,
		}
		outcome.nested = &nested
		reportStepFinished(options, definition.Name, workflowStep.ID, "cancel_on", index, total, outcome.stats)
	}

	conditionStarted := time.Now()
	traceStep(options, definition, workflowStep, diagnostic.PhaseCondition, diagnostic.StatusStarted, time.Time{}, string(workflowStep.If), nil)
	run, err := e.evaluateCondition(workflowStep.If, makeConditionEnvironment(definition, options.RunDir, state))
	if err != nil {
		stepErr := fmt.Errorf("workflow %q step %q (cancel_on): evaluating if: %w", definition.Name, workflowStep.ID, err)
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
		Depth: options.depth, StepID: workflowStep.ID, StepType: "cancel_on", Index: index, Total: total,
	})
	participantCount := len(group.Monitors) + 1
	report(options, ProgressEvent{
		Kind: ControlStarted, Status: StatusRunning, Time: startedAt, WorkflowName: definition.Name,
		Depth: options.depth, StepID: workflowStep.ID, ControlKind: "cancel_on",
		Iterations: participantCount, MaxConcurrency: participantCount,
	})

	participants := make([]cancelOnParticipant, participantCount)
	participants[0] = cancelOnParticipant{declaration: group.Steps, kind: "body", label: "body"}
	for monitorIndex := range group.Monitors {
		declaration := group.MonitorDeclaration(monitorIndex)
		participants[monitorIndex+1] = cancelOnParticipant{
			declaration: []workflow.Step{declaration}, kind: cancelOnDeclarationKind(declaration), label: group.Monitors[monitorIndex].ID,
		}
	}

	winner, parentErr := controlpkg.Race(ctx, participantCount, func(participantCtx context.Context, participantIndex int) bool {
		participant := &participants[participantIndex]
		branchOptions := options
		branchOptions.depth += 2
		branchOptions.stepRunID = ""
		if participantIndex > 0 {
			branchOptions.Interactive = false
			branchOptions.Stdin = nil
		}
		participant.state = cloneState(state)
		participant.state.writtenVars = make(map[string]struct{})
		bodyTotal := leafStepCount(participant.declaration)
		participant.stats = RunStats{StartedAt: time.Now(), Total: bodyTotal, Steps: make([]StepStats, 0, bodyTotal)}
		participant.err = e.executeSequence(participantCtx, definition, participant.declaration, branchOptions, participant.state, &participant.stats, 1, bodyTotal)
		participant.stats.FinishedAt = time.Now()
		participant.stats.Duration = participant.stats.FinishedAt.Sub(participant.stats.StartedAt)
		participant.skipped = allStepsSkipped(participant.stats)
		// The body always ends the race: once it finishes there is nothing left to guard,
		// even when every one of its steps was skipped. A skipped monitor never triggered.
		return participantIndex == 0 || !participant.skipped
	})
	if parentErr != nil {
		stepErr := fmt.Errorf("workflow %q step %q (cancel_on): %w", definition.Name, workflowStep.ID, parentErr)
		finish(statusFromError(parentErr), stepErr, cancelOnParticipantStats(participants), cancelOnNestedStats(participants))
		outcome.err = stepErr
		reportCancelOnFinished(options, definition, workflowStep, startedAt, participants, stepErr)
		return outcome
	}
	if winner < 0 {
		stepErr := fmt.Errorf("workflow %q step %q (cancel_on): no participant produced an outcome", definition.Name, workflowStep.ID)
		finish(StatusFailed, stepErr, cancelOnParticipantStats(participants), cancelOnNestedStats(participants))
		outcome.err = stepErr
		reportCancelOnFinished(options, definition, workflowStep, startedAt, participants, stepErr)
		return outcome
	}

	outputs := cancelOnOutputs(group, participants, winner)
	if group.Collect != "" {
		collection, collectErr := collectCancelOn(definition, options.RunDir, state, outputs, group.Collect)
		if collectErr != nil {
			stepErr := fmt.Errorf("workflow %q step %q (cancel_on): collecting result: %w", definition.Name, workflowStep.ID, collectErr)
			finish(StatusFailed, stepErr, cancelOnParticipantStats(participants), cancelOnNestedStats(participants))
			outcome.err = stepErr
			reportCancelOnFinished(options, definition, workflowStep, startedAt, participants, stepErr)
			return outcome
		}
		outputs["result"] = collection
	}
	outcome.result = step.Result{Outputs: outputs}
	finish(StatusSucceeded, nil, cancelOnParticipantStats(participants), cancelOnNestedStats(participants))
	reportCancelOnFinished(options, definition, workflowStep, startedAt, participants, nil)
	return outcome
}

func cancelOnOutputs(group *workflow.CancelOnGroup, participants []cancelOnParticipant, winner int) map[string]any {
	winnerStatus := statusFromError(participants[winner].err)
	winnerMonitor := ""
	if winner > 0 {
		winnerMonitor = group.Monitors[winner-1].ID
	}
	monitorRecords := make(map[string]any, len(group.Monitors))
	for index, monitor := range group.Monitors {
		participant := participants[index+1]
		lostRace := index+1 != winner
		record := map[string]any{
			"kind": participant.kind, "status": string(cancelOnParticipantStatus(participant)),
			"error": cancelOnParticipantError(participant.err, lostRace), "outputs": nil,
			"steps": cancelOnMonitorStepRecords(participant.declaration[0], participant.state, participant.stats, lostRace),
			"vars":  controlWrittenVars(participant.state),
		}
		if participant.state != nil {
			if value, exists := participant.state.Steps[monitor.ID]; exists && cancelOnParticipantStatus(participant) == StatusSucceeded {
				record["outputs"] = cloneAny(value)
			}
		}
		monitorRecords[monitor.ID] = record
	}
	body := participants[0]
	return map[string]any{
		"ok": winnerStatus == StatusSucceeded, "triggered": winner > 0,
		"status": string(winnerStatus), "error": cancelOnErrorValue(participants[winner].err),
		"winner": map[string]any{"monitor": winnerMonitor, "kind": participants[winner].kind},
		"steps":  controlStepRecords(group.Steps, body.state, body.stats, winner != 0),
		"vars":   controlWrittenVars(body.state), "monitors": monitorRecords, "result": nil,
	}
}

func cancelOnMonitorStepRecords(declaration workflow.Step, state *State, stats RunStats, lostRace bool) map[string]any {
	children, transparent := transparentChildSequences(declaration)
	if !transparent {
		return map[string]any{}
	}
	records := make(map[string]any)
	// Every child sequence is recorded against the same run, so the step index is built once
	// here rather than rebuilt from the whole step list for each of them.
	byID := indexStepStats(stats)
	for _, child := range children {
		collectControlStepRecords(records, child.Steps, state, byID, lostRace)
	}
	return records
}

func cancelOnParticipantStatus(participant cancelOnParticipant) ExecutionStatus {
	if participant.skipped && participant.err == nil {
		return StatusSkipped
	}
	return statusFromError(participant.err)
}

func cancelOnErrorValue(err error) any {
	if err == nil {
		return nil
	}
	return err.Error()
}

func cancelOnParticipantError(err error, lostRace bool) any {
	if lostRace && cancellationOnly(err) {
		return nil
	}
	return cancelOnErrorValue(err)
}

// cancellationOnly reports whether err carries nothing but cancellation. Joined errors are
// walked branch by branch because errors.Is answers "any", not "all": a join of a cancellation
// and a real failure must stay a failure. Everything else defers to errors.Is at the leaf, so
// the answer matches statusFromError even for an error that reports cancellation through its
// own Is method rather than by wrapping the sentinel.
func cancellationOnly(err error) bool {
	if err == nil {
		return false
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		children := joined.Unwrap()
		if len(children) == 0 {
			return false
		}
		for _, child := range children {
			if !cancellationOnly(child) {
				return false
			}
		}
		return true
	}
	if wrapped := errors.Unwrap(err); wrapped != nil {
		return cancellationOnly(wrapped)
	}
	return errors.Is(err, context.Canceled)
}

func cancelOnDeclarationKind(declaration workflow.Step) string {
	switch {
	case declaration.IsExecutorBlock():
		return "executor"
	case declaration.IsEnvironmentBlock():
		return "env"
	case declaration.IsWorkingDirectoryBlock():
		return "working_directory"
	case declaration.IsConditionalBlock():
		return "if"
	case declaration.Concurrent != nil:
		return "concurrent"
	case declaration.Batch != nil:
		return "batch"
	case declaration.Foreach != nil:
		return "foreach"
	case declaration.Matrix != nil:
		return "matrix"
	case declaration.Loop != nil:
		return "loop"
	default:
		return executionKind(declaration)
	}
}

func cancelOnParticipantStats(participants []cancelOnParticipant) []IterationStats {
	result := make([]IterationStats, 0, len(participants))
	for index, participant := range participants {
		result = append(result, IterationStats{
			Index: index, Label: participant.label, Status: cancelOnParticipantStatus(participant), StartedAt: participant.stats.StartedAt,
			Duration: participant.stats.Duration, Error: participant.err, Steps: participant.stats.Steps,
		})
	}
	return result
}

func cancelOnNestedStats(participants []cancelOnParticipant) RunStats {
	nested := RunStats{}
	for _, participant := range participants {
		rollupNestedMetrics(&nested, participant.stats)
	}
	return nested
}

func collectCancelOn(definition *workflow.Definition, runDir string, outer *State, outputs map[string]any, expression string) (any, error) {
	environment := templateData(definition, runDir, outer)
	environment["steps"] = outputs["steps"]
	environment["vars"] = outputs["vars"]
	environment["monitors"] = outputs["monitors"]
	environment["cancel_on"] = map[string]any{
		"ok": outputs["ok"], "triggered": outputs["triggered"], "status": outputs["status"],
		"error": outputs["error"], "winner": outputs["winner"],
	}
	value, err := controlpkg.EvaluateExpression(expression, environment)
	if err != nil {
		return nil, err
	}
	if !workflow.ActionDataValue(value) {
		return nil, fmt.Errorf("expression returned %T, want YAML/JSON-compatible value", value)
	}
	return cloneAny(value), nil
}

func reportCancelOnFinished(options Options, definition *workflow.Definition, workflowStep workflow.Step, started time.Time, participants []cancelOnParticipant, err error) {
	startedParticipants := 0
	succeededParticipants := 0
	for _, participant := range participants {
		if !participant.stats.StartedAt.IsZero() {
			startedParticipants++
		}
		if cancelOnParticipantStatus(participant) == StatusSucceeded {
			succeededParticipants++
		}
	}
	report(options, ProgressEvent{
		Kind: ControlFinished, Status: statusFromError(err), Time: time.Now(), WorkflowName: definition.Name,
		Depth: options.depth, StepID: workflowStep.ID, ControlKind: "cancel_on", Iterations: len(participants),
		Started: startedParticipants, Succeeded: succeededParticipants, MaxConcurrency: len(participants), Duration: time.Since(started), Error: err,
	})
}
