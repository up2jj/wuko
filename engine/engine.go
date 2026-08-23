package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"strings"
	"time"

	"github.com/expr-lang/expr"
	"github.com/up2jj/wuko/diagnostic"
	"github.com/up2jj/wuko/executor"
	wukoexpr "github.com/up2jj/wuko/expression"
	"github.com/up2jj/wuko/process"
	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/workflow"
)

type Engine struct {
	registry  *step.Registry
	executors *executor.Registry
}

type Option func(*Engine)

func WithExecutors(registry *executor.Registry) Option {
	return func(engine *Engine) { engine.executors = registry }
}

type Options struct {
	Vars map[string]any
	Env  map[string]string
	// Dependencies contains outputs from direct prerequisite workflows keyed by alias.
	Dependencies map[string]map[string]any
	// BaseEnv overrides the current process environment when non-nil.
	BaseEnv map[string]string
	RunDir  string
	// LocalValueDir is empty when local persistence is unavailable, such as for a remote workflow.
	LocalValueDir  string
	GlobalValueDir string
	Stdin          io.Reader
	Stdout         io.Writer
	Stderr         io.Writer
	Interactive    bool
	DryRun         bool
	Executor       process.Executor
	// Progress receives structured workflow, step, attempt, retry, poll, and timing events.
	Progress func(ProgressEvent)
	// Diagnostics receives opt-in loading, validation, and execution phase events.
	Diagnostics            diagnostic.Reporter
	inputs                 map[string]any
	operationPrefix        string
	depth                  int
	runtime                *runRuntime
	defers                 *deferStack
	renderer               *workflow.Renderer
	deferContextValidation bool
	insideExecutor         bool
}

type State struct {
	Inputs map[string]any
	Vars   map[string]any
	Env    map[string]string
	Steps  map[string]any
	// Outputs contains values produced by a return control or declared output expressions.
	Outputs map[string]any
	// Dependencies contains immutable outputs from direct prerequisite workflows.
	Dependencies map[string]map[string]any
	// Bindings contains lifecycle and iteration-local roots such as batch, finally, foreach, and matrix.
	Bindings    map[string]any
	Stats       RunStats
	writtenVars map[string]struct{}
	returning   bool
	didReturn   bool
}

func New(registry *step.Registry, options ...Option) *Engine {
	engine := &Engine{registry: registry}
	for _, option := range options {
		option(engine)
	}
	return engine
}

func (e *Engine) Validate(ctx context.Context, definition *workflow.Definition, options Options) error {
	started := time.Now()
	trace(options, diagnostic.Event{Phase: diagnostic.PhaseValidation, Status: diagnostic.StatusStarted, Time: started, WorkflowName: definition.Name, Location: definition.Location, Message: "validating workflow"})
	if err := definition.ValidateStructure(); err != nil {
		trace(options, diagnostic.Event{Phase: diagnostic.PhaseValidation, Status: diagnostic.StatusFailed, WorkflowName: definition.Name, Location: definition.Location, Duration: time.Since(started), Error: err})
		return err
	}
	if err := validateWorkflowOutputExpressions(definition); err != nil {
		trace(options, diagnostic.Event{Phase: diagnostic.PhaseValidation, Status: diagnostic.StatusFailed, WorkflowName: definition.Name, Location: definition.Location, Duration: time.Since(started), Error: err})
		return err
	}
	if options.renderer == nil {
		var err error
		options.renderer, err = workflow.NewRenderer(definition.Templates)
		if err != nil {
			return err
		}
	}
	state, err := initialState(definition, options)
	if err != nil {
		trace(options, diagnostic.Event{Phase: diagnostic.PhaseValues, Status: diagnostic.StatusFailed, WorkflowName: definition.Name, Location: definition.Location, Error: err})
		trace(options, diagnostic.Event{Phase: diagnostic.PhaseValidation, Status: diagnostic.StatusFailed, WorkflowName: definition.Name, Location: definition.Location, Duration: time.Since(started)})
		return err
	}
	err = e.validateSteps(ctx, definition, definition.Steps, options, state)
	if err == nil && len(definition.Finally) > 0 {
		state.Bindings = map[string]any{"finally": map[string]any{
			"status": string(StatusSucceeded), "errors": []any{},
		}}
		err = e.validateSteps(ctx, definition, definition.Finally, options, state)
	}
	status := diagnostic.StatusSucceeded
	if err != nil {
		status = diagnostic.StatusFailed
	}
	trace(options, diagnostic.Event{Phase: diagnostic.PhaseValidation, Status: status, WorkflowName: definition.Name, Location: definition.Location, Duration: time.Since(started)})
	return err
}

func (e *Engine) validateSteps(ctx context.Context, definition *workflow.Definition, steps []workflow.Step, options Options, state *State) error {
	for _, workflowStep := range steps {
		if workflowStep.IsExecutorBlock() {
			if err := e.validateExecutorBlock(ctx, definition, workflowStep, options, state); err != nil {
				return err
			}
			continue
		}
		if workflowStep.IsWorkingDirectoryBlock() {
			if err := e.validateWorkingDirectoryBlock(ctx, definition, workflowStep, options, state); err != nil {
				return err
			}
			continue
		}
		if workflowStep.IsConditionalBlock() {
			started := time.Now()
			trace(options, diagnostic.Event{
				Phase: diagnostic.PhaseValidation, Status: diagnostic.StatusStarted, Time: started,
				WorkflowName: definition.Name, Location: workflowStep.Location, Message: "validating conditional block",
			})
			fail := func(err error) error {
				trace(options, diagnostic.Event{
					Phase: diagnostic.PhaseValidation, Status: diagnostic.StatusFailed, Time: time.Now(), Duration: time.Since(started),
					WorkflowName: definition.Name, Location: workflowStep.Location, Error: err,
				})
				return err
			}
			if _, err := compileCondition(workflowStep.If); err != nil {
				return fail(fmt.Errorf("conditional block if: %w", err))
			}
			if err := e.validateSteps(ctx, definition, workflowStep.Steps, options, state); err != nil {
				return fail(fmt.Errorf("conditional block: %w", err))
			}
			trace(options, diagnostic.Event{
				Phase: diagnostic.PhaseValidation, Status: diagnostic.StatusSucceeded, Time: time.Now(), Duration: time.Since(started),
				WorkflowName: definition.Name, Location: workflowStep.Location,
			})
			continue
		}
		if workflowStep.Batch != nil || workflowStep.Foreach != nil || workflowStep.Matrix != nil {
			if err := e.validateControl(ctx, definition, workflowStep, options, state); err != nil {
				return fmt.Errorf("step %q: %w", workflowStep.ID, err)
			}
			continue
		}
		if workflowStep.Return != nil {
			if err := e.validateReturn(workflowStep); err != nil {
				return fmt.Errorf("return: %w", err)
			}
			continue
		}
		if workflowStep.Concurrent != nil {
			started := time.Now()
			trace(options, diagnostic.Event{Phase: diagnostic.PhaseConcurrent, Status: diagnostic.StatusStarted, Time: started, WorkflowName: definition.Name, Location: workflowStep.Location, Message: "validating concurrent group", Attributes: []diagnostic.Attribute{diagnostic.Attr("steps", fmt.Sprint(len(workflowStep.Concurrent.Steps)))}})
			childOptions := options
			childOptions.Interactive = false
			childOptions.Stdin = nil
			childOptions.depth++
			if err := e.validateSteps(ctx, definition, workflowStep.Concurrent.Steps, childOptions, state); err != nil {
				trace(options, diagnostic.Event{Phase: diagnostic.PhaseConcurrent, Status: diagnostic.StatusFailed, WorkflowName: definition.Name, Location: workflowStep.Location, Duration: time.Since(started)})
				return fmt.Errorf("concurrent group: %w", err)
			}
			trace(options, diagnostic.Event{Phase: diagnostic.PhaseConcurrent, Status: diagnostic.StatusSucceeded, WorkflowName: definition.Name, Location: workflowStep.Location, Duration: time.Since(started)})
			continue
		}
		started := time.Now()
		traceStep(options, definition, workflowStep, diagnostic.PhaseValidation, diagnostic.StatusStarted, time.Time{}, "validating step", nil)
		if workflowStep.If != "" {
			if _, err := compileCondition(workflowStep.If); err != nil {
				traceStep(options, definition, workflowStep, diagnostic.PhaseValidation, diagnostic.StatusFailed, started, "compiling condition", err)
				return fmt.Errorf("step %q if: %w", workflowStep.ID, err)
			}
		}
		if workflowStep.Retry != nil && workflowStep.Retry.OperationID != "" {
			if err := validateTemplates(options.renderer, workflowStep.Retry.OperationID, false); err != nil {
				traceStep(options, definition, workflowStep, diagnostic.PhaseValidation, diagnostic.StatusFailed, started, "validating retry operation ID", err)
				return fmt.Errorf("step %q retry operation_id: %w", workflowStep.ID, err)
			}
		}
		if err := e.validateDeferredSteps(ctx, definition, workflowStep, options, state); err != nil {
			traceStep(options, definition, workflowStep, diagnostic.PhaseValidation, diagnostic.StatusFailed, started, "validating defer", err)
			return err
		}
		if workflowStep.Action != nil {
			if options.deferContextValidation {
				traceStep(options, definition, workflowStep, diagnostic.PhaseValidation, diagnostic.StatusSucceeded, started, "deferring context-dependent action validation", nil)
				continue
			}
			if err := e.validateAction(ctx, definition, workflowStep, options, state); err != nil {
				traceStep(options, definition, workflowStep, diagnostic.PhaseValidation, diagnostic.StatusFailed, started, "validating action", err)
				return fmt.Errorf("step %q: %w", workflowStep.ID, err)
			}
			traceStep(options, definition, workflowStep, diagnostic.PhaseValidation, diagnostic.StatusSucceeded, started, "", nil)
			continue
		}
		if workflowStep.Type == "wait" {
			if options.insideExecutor {
				err := fmt.Errorf("step type %q is not supported inside executor blocks", workflowStep.Type)
				traceStep(options, definition, workflowStep, diagnostic.PhaseValidation, diagnostic.StatusFailed, started, "validating executor support", err)
				return fmt.Errorf("step %q: %w", workflowStep.ID, err)
			}
			if err := e.validateWaitStep(ctx, definition, workflowStep, options, state, !options.deferContextValidation); err != nil {
				traceStep(options, definition, workflowStep, diagnostic.PhaseValidation, diagnostic.StatusFailed, started, "validating wait", err)
				return fmt.Errorf("step %q: %w", workflowStep.ID, err)
			}
			traceStep(options, definition, workflowStep, diagnostic.PhaseValidation, diagnostic.StatusSucceeded, started, "", nil)
			continue
		}
		if err := validateTemplates(options.renderer, workflowStep.With, workflowStep.Type == "lua"); err != nil {
			traceStep(options, definition, workflowStep, diagnostic.PhaseValidation, diagnostic.StatusFailed, started, "validating templates", err)
			return fmt.Errorf("step %q template: %w", workflowStep.ID, err)
		}
		runner, err := e.registry.Build(workflowStep.Type, workflowStep.With)
		if err != nil {
			traceStep(options, definition, workflowStep, diagnostic.PhaseValidation, diagnostic.StatusFailed, started, "building runner", err)
			return fmt.Errorf("step %q: %w", workflowStep.ID, err)
		}
		if options.insideExecutor {
			if _, ok := runner.(step.ExecutorAware); !ok {
				err := fmt.Errorf("step type %q is not supported inside executor blocks", workflowStep.Type)
				traceStep(options, definition, workflowStep, diagnostic.PhaseValidation, diagnostic.StatusFailed, started, "validating executor support", err)
				return fmt.Errorf("step %q: %w", workflowStep.ID, err)
			}
		}
		validator, ok := runner.(step.Validator)
		if !ok || options.deferContextValidation {
			traceStep(options, definition, workflowStep, diagnostic.PhaseValidation, diagnostic.StatusSucceeded, started, "", nil)
			continue
		}
		request := makeRequest(definition, workflowStep.ID, options, state, 1, maxAttempts(workflowStep), "validation")
		if err := validator.Validate(ctx, request); err != nil {
			traceStep(options, definition, workflowStep, diagnostic.PhaseValidation, diagnostic.StatusFailed, started, "validating runner", err)
			return fmt.Errorf("step %q: %w", workflowStep.ID, err)
		}
		traceStep(options, definition, workflowStep, diagnostic.PhaseValidation, diagnostic.StatusSucceeded, started, "", nil)
	}
	return nil
}

func (e *Engine) Run(ctx context.Context, definition *workflow.Definition, options Options) (runState *State, runErr error) {
	rootRun := options.runtime == nil
	options = prepareRunOptions(options)
	if options.renderer == nil {
		options.renderer, runErr = workflow.NewRenderer(definition.Templates)
		if runErr != nil {
			return nil, runErr
		}
	}
	state, err := initialState(definition, options)
	if err != nil {
		trace(options, diagnostic.Event{Phase: diagnostic.PhaseValues, Status: diagnostic.StatusFailed, WorkflowName: definition.Name, Location: definition.Location, Error: err})
		return nil, err
	}
	if err := e.Validate(ctx, definition, options); err != nil {
		return nil, err
	}
	if options.DryRun {
		if err := writeDryRun(options.Stdout, definition.Steps, "", nil); err != nil {
			return nil, err
		}
		if err := writeDryRunFinally(options.Stdout, definition.Finally); err != nil {
			return nil, err
		}
		state.Outputs = workflow.OutputPlaceholders(definition.Outputs)
		return state, nil
	}
	options.defers = newDeferStack(definition.Steps)

	startedAt := time.Now()
	mainTotal := leafStepCount(definition.Steps) + nestedDeferScopeStepCount(definition.Steps)
	total := mainTotal + options.defers.stepCount() + leafStepCount(definition.Finally)
	stats := RunStats{StartedAt: startedAt, Total: total, Steps: make([]StepStats, 0, total)}
	report(options, ProgressEvent{
		Kind: WorkflowStarted, Status: StatusRunning, Time: startedAt,
		WorkflowName: definition.Name, Depth: options.depth, Total: total,
	})
	var mainErr error
	defer func() {
		finishedAt := time.Now()
		stats.FinishedAt = finishedAt
		stats.Duration = finishedAt.Sub(startedAt)
		state.Stats = stats
		status := statusFromError(runErr)
		if mainErr != nil {
			status = statusFromError(mainErr)
		}
		report(options, ProgressEvent{
			Kind: WorkflowFinished, Status: status, Time: finishedAt,
			WorkflowName: definition.Name, Depth: options.depth, Total: total,
			Duration: stats.Duration, Error: runErr, Stats: stats,
		})
	}()

	mainErr = e.executeSequence(ctx, definition, definition.Steps, options, state, &stats, 1, total)
	state.returning = false
	cleanupErrors := e.executeCleanupScope(context.WithoutCancel(ctx), definition, options.defers, definition.Finally, options, state, &stats, mainErr, stats.Steps, mainTotal+1, total)
	if rootRun {
		cleanupErrors = append(cleanupErrors, options.runtime.runCleanups()...)
	}
	runErr = errors.Join(append([]error{mainErr}, cleanupErrors...)...)
	if runErr != nil {
		return nil, runErr
	}
	if err := e.finishWorkflowOutputs(definition, options, state); err != nil {
		return nil, err
	}
	return state, nil
}

func (e *Engine) executeCleanupScope(ctx context.Context, definition *workflow.Definition, defers *deferStack, finally []workflow.Step, options Options, state *State, stats *RunStats, mainErr error, mainStats []StepStats, firstIndex, total int) []error {
	if (defers == nil || len(defers.groups) == 0) && len(finally) == 0 {
		return nil
	}
	bindingsWereNil := state.Bindings == nil
	if state.Bindings == nil {
		state.Bindings = make(map[string]any)
	}
	previousBinding, hadPreviousBinding := state.Bindings["finally"]
	errorsValue := finallyErrorRecords(mainStats)
	if mainErr != nil && len(errorsValue) == 0 {
		errorsValue = append(errorsValue, finallyErrorRecord(statusFromError(mainErr), "", "", mainErr))
	}
	state.Bindings["finally"] = map[string]any{
		"status": string(statusFromError(mainErr)), "errors": errorsValue,
	}
	defer func() {
		if hadPreviousBinding {
			state.Bindings["finally"] = previousBinding
		} else {
			delete(state.Bindings, "finally")
		}
		if bindingsWereNil && len(state.Bindings) == 0 {
			state.Bindings = nil
		}
	}()

	cleanupErrors, index := e.executeDeferred(ctx, definition, defers, options, state, stats, firstIndex, total)
	cleanupErrors = append(cleanupErrors, e.executeCleanupSteps(ctx, definition, finally, options, state, stats, index, total)...)
	return cleanupErrors
}

func (e *Engine) executeCleanupSteps(ctx context.Context, definition *workflow.Definition, cleanupSteps []workflow.Step, options Options, state *State, stats *RunStats, firstIndex, total int) []error {
	binding, _ := state.Bindings["finally"].(map[string]any)
	errorsValue, _ := binding["errors"].([]any)
	index := firstIndex
	cleanupErrors := make([]error, 0)
	for _, cleanupStep := range cleanupSteps {
		before := len(stats.Steps)
		err := e.executeSequence(ctx, definition, []workflow.Step{cleanupStep}, options, state, stats, index, total)
		if err != nil {
			cleanupErrors = append(cleanupErrors, err)
			newRecords := finallyErrorRecords(stats.Steps[before:])
			if len(newRecords) == 0 {
				newRecords = append(newRecords, finallyErrorRecord(statusFromError(err), cleanupStep.ID, executionKind(cleanupStep), err))
			}
			errorsValue = append(errorsValue, newRecords...)
			binding["errors"] = errorsValue
		}
		index += leafStepCount([]workflow.Step{cleanupStep})
	}
	return cleanupErrors
}

func finallyErrorRecords(steps []StepStats) []any {
	var records []any
	for _, stats := range steps {
		if len(stats.Iterations) > 0 {
			before := len(records)
			for _, iteration := range stats.Iterations {
				records = append(records, finallyErrorRecords(iteration.Steps)...)
			}
			if len(records) > before {
				continue
			}
		}
		if stats.Error == nil || stats.Status == StatusSucceeded || stats.Status == StatusSkipped {
			continue
		}
		records = append(records, finallyErrorRecord(stats.Status, stats.ID, stats.Type, stats.Error))
	}
	return records
}

func finallyErrorRecord(status ExecutionStatus, stepID, stepType string, err error) map[string]any {
	return map[string]any{
		"status": string(status), "message": err.Error(), "step_id": stepID, "step_type": stepType,
	}
}

func (e *Engine) executeSequence(ctx context.Context, definition *workflow.Definition, steps []workflow.Step, options Options, state *State, stats *RunStats, firstIndex, total int) error {
	index := firstIndex
	for position, workflowStep := range steps {
		if workflowStep.IsExecutorBlock() {
			if err := e.executeExecutorBlock(ctx, definition, workflowStep, options, state, stats, index, total); err != nil {
				return err
			}
			index += leafStepCount(workflowStep.Steps) + newDeferStack(workflowStep.Steps).stepCount() + leafStepCount(workflowStep.Finally)
			if state.returning {
				recordSkippedSteps(definition, steps[position+1:], options, stats, index, total)
				return nil
			}
			continue
		}
		if workflowStep.IsWorkingDirectoryBlock() {
			if err := e.executeWorkingDirectoryBlock(ctx, definition, workflowStep, options, state, stats, index, total); err != nil {
				return err
			}
			index += leafStepCount(workflowStep.Steps)
			if state.returning {
				recordSkippedSteps(definition, steps[position+1:], options, stats, index, total)
				return nil
			}
			continue
		}
		if workflowStep.IsConditionalBlock() {
			if err := ctx.Err(); err != nil {
				return err
			}
			run, err := evaluateConditionalBlock(definition, workflowStep, options, state)
			if err != nil {
				return err
			}
			if run {
				if err := e.executeSequence(ctx, definition, workflowStep.Steps, options, state, stats, index, total); err != nil {
					return err
				}
			} else {
				recordSkippedSteps(definition, workflowStep.Steps, options, stats, index, total)
			}
			index += leafStepCount(workflowStep.Steps)
			if state.returning {
				recordSkippedSteps(definition, steps[position+1:], options, stats, index, total)
				return nil
			}
			continue
		}
		if workflowStep.Return != nil {
			triggered, err := e.executeReturn(ctx, definition, workflowStep, options, state)
			if err != nil {
				return err
			}
			if triggered {
				recordSkippedSteps(definition, steps[position+1:], options, stats, index, total)
				return nil
			}
			continue
		}
		if workflowStep.Concurrent != nil {
			outcomes, groupErr := e.runConcurrent(ctx, definition, workflowStep.Concurrent, options, state, index, total)
			for _, outcome := range outcomes {
				mergeRunStats(stats, outcome.stats)
			}
			index += leafStepCount(workflowStep.Concurrent.Steps)
			if groupErr != nil {
				return groupErr
			}
			continue
		}
		var outcome stepOutcome
		if workflowStep.Batch != nil || workflowStep.Foreach != nil || workflowStep.Matrix != nil {
			outcome = e.executeControl(ctx, definition, workflowStep, options, state, index, total)
		} else {
			outcome = e.executeStep(ctx, definition, workflowStep, options, state, index, total)
		}
		if outcome.started {
			recordStep(stats, outcome.stats)
			if outcome.nested != nil {
				rollupNestedMetrics(stats, *outcome.nested)
			}
		}
		index++
		if outcome.err != nil {
			return outcome.err
		}
		if outcome.skipped {
			continue
		}
		commitStarted := time.Now()
		traceStep(options, definition, workflowStep, diagnostic.PhaseCommit, diagnostic.StatusStarted, time.Time{}, "committing step result", nil)
		commitStepResult(state, workflowStep.ID, outcome.result)
		traceStep(options, definition, workflowStep, diagnostic.PhaseCommit, diagnostic.StatusSucceeded, commitStarted, "", nil,
			diagnostic.Attr("outputs", fmt.Sprint(len(outcome.result.Outputs))), diagnostic.Attr("variables", fmt.Sprint(len(outcome.result.Variables))))
		if len(workflowStep.Defer) > 0 {
			options.defers.register(workflowStep.ID, options)
		}
	}
	return nil
}

func evaluateConditionalBlock(definition *workflow.Definition, workflowStep workflow.Step, options Options, state *State) (bool, error) {
	started := time.Now()
	trace(options, diagnostic.Event{
		Phase: diagnostic.PhaseCondition, Status: diagnostic.StatusStarted, Time: started,
		WorkflowName: definition.Name, Location: workflowStep.Location,
		Message: string(workflowStep.If),
	})
	run, err := evaluateCondition(workflowStep.If, makeConditionEnvironment(definition, options.RunDir, state))
	if err != nil {
		trace(options, diagnostic.Event{
			Phase: diagnostic.PhaseCondition, Status: diagnostic.StatusFailed, Time: time.Now(), Duration: time.Since(started),
			WorkflowName: definition.Name, Location: workflowStep.Location, Error: err,
		})
		return false, fmt.Errorf("workflow %q conditional block: evaluating if: %w", definition.Name, err)
	}
	status := diagnostic.StatusSucceeded
	message := "condition evaluated true"
	if !run {
		status = diagnostic.StatusSkipped
		message = "condition evaluated false"
	}
	trace(options, diagnostic.Event{
		Phase: diagnostic.PhaseCondition, Status: status, Time: time.Now(), Duration: time.Since(started),
		WorkflowName: definition.Name, Location: workflowStep.Location, Message: message,
	})
	return run, nil
}

func recordSkippedSteps(definition *workflow.Definition, steps []workflow.Step, options Options, stats *RunStats, firstIndex, total int) {
	index := firstIndex
	for _, workflowStep := range steps {
		if workflowStep.IsExecutorBlock() {
			recordSkippedSteps(definition, workflowStep.Steps, options, stats, index, total)
			index += leafStepCount(workflowStep.Steps)
			stack := newDeferStack(workflowStep.Steps)
			for groupIndex := len(stack.groups) - 1; groupIndex >= 0; groupIndex-- {
				recordSkippedSteps(definition, stack.groups[groupIndex].steps, options, stats, index, total)
				index += leafStepCount(stack.groups[groupIndex].steps)
			}
			recordSkippedSteps(definition, workflowStep.Finally, options, stats, index, total)
			index += leafStepCount(workflowStep.Finally)
			continue
		}
		if children, transparent := transparentChildSequences(workflowStep); transparent {
			for _, child := range children {
				recordSkippedSteps(definition, child.Steps, options, stats, index, total)
				index += leafStepCount(child.Steps)
			}
			continue
		}
		if workflowStep.Return != nil {
			continue
		}
		started := time.Now()
		stepStats := StepStats{
			ID: workflowStep.ID, Type: skippedStepKind(workflowStep), Index: index,
			Status: StatusSkipped, StartedAt: started,
		}
		reportStepFinished(options, definition.Name, workflowStep.ID, stepStats.Type, index, total, stepStats)
		recordStep(stats, stepStats)
		index++
	}
}

func skippedStepKind(workflowStep workflow.Step) string {
	if workflowStep.Batch != nil {
		return "batch"
	}
	if workflowStep.Foreach != nil {
		return "foreach"
	}
	if workflowStep.Matrix != nil {
		return "matrix"
	}
	return executionKind(workflowStep)
}

type stepOutcome struct {
	result  step.Result
	stats   StepStats
	err     error
	started bool
	skipped bool
	nested  *RunStats
}

func (e *Engine) executeStep(ctx context.Context, definition *workflow.Definition, workflowStep workflow.Step, options Options, state *State, index, total int) stepOutcome {
	if err := ctx.Err(); err != nil {
		return stepOutcome{err: err}
	}
	kind := executionKind(workflowStep)
	stepStartedAt := time.Now()
	outcome := stepOutcome{started: true}
	metrics := waitMetrics{}
	finishStep := func(status ExecutionStatus, stepErr error, attempts []AttemptStats, retryWait time.Duration) {
		outcome.stats = StepStats{
			ID: workflowStep.ID, Type: kind, Index: index, Status: status,
			StartedAt: stepStartedAt, Duration: time.Since(stepStartedAt),
			RetryWait: retryWait, Polls: metrics.polls, PollWait: metrics.pollWait,
			Attempts: attempts, Error: stepErr,
		}
		reportStepFinished(options, definition.Name, workflowStep.ID, kind, index, total, outcome.stats)
	}
	conditionStarted := time.Now()
	traceStep(options, definition, workflowStep, diagnostic.PhaseCondition, diagnostic.StatusStarted, time.Time{}, string(workflowStep.If), nil)
	run, err := evaluateCondition(workflowStep.If, makeConditionEnvironment(definition, options.RunDir, state))
	if err != nil {
		traceStep(options, definition, workflowStep, diagnostic.PhaseCondition, diagnostic.StatusFailed, conditionStarted, "", err)
		stepErr := fmt.Errorf("workflow %q step %q (%s): evaluating if: %w", definition.Name, workflowStep.ID, kind, err)
		finishStep(StatusFailed, stepErr, nil, 0)
		outcome.err = stepErr
		return outcome
	}
	if !run {
		traceStep(options, definition, workflowStep, diagnostic.PhaseCondition, diagnostic.StatusSkipped, conditionStarted, "condition evaluated false", nil)
		finishStep(StatusSkipped, nil, nil, 0)
		outcome.skipped = true
		return outcome
	}
	traceStep(options, definition, workflowStep, diagnostic.PhaseCondition, diagnostic.StatusSucceeded, conditionStarted, "condition evaluated true", nil)
	report(options, ProgressEvent{
		Kind: StepStarted, Status: StatusRunning, Time: stepStartedAt,
		WorkflowName: definition.Name, Depth: options.depth, StepID: workflowStep.ID,
		StepType: kind, Index: index, Total: total, MaxAttempts: maxAttempts(workflowStep),
		Timeout: stepTimeout(workflowStep),
	})
	var execute stepExecutor
	cleanup := func() {}
	if workflowStep.Action != nil {
		prepareStarted := time.Now()
		traceStep(options, definition, workflowStep, diagnostic.PhaseActionInputs, diagnostic.StatusStarted, time.Time{}, "resolving action inputs", nil)
		execute, cleanup, err = e.prepareActionExecutor(definition, workflowStep, options, state)
		if err != nil {
			traceStep(options, definition, workflowStep, diagnostic.PhaseActionInputs, diagnostic.StatusFailed, prepareStarted, "", err)
			stepErr := fmt.Errorf("workflow %q step %q (%s): %w", definition.Name, workflowStep.ID, kind, err)
			finishStep(statusFromError(stepErr), stepErr, nil, 0)
			outcome.err = stepErr
			return outcome
		}
		traceStep(options, definition, workflowStep, diagnostic.PhaseActionInputs, diagnostic.StatusSucceeded, prepareStarted, "", nil)
	} else if workflowStep.Type == "wait" {
		prepareStarted := time.Now()
		traceStep(options, definition, workflowStep, diagnostic.PhaseRunner, diagnostic.StatusStarted, time.Time{}, "preparing wait", nil)
		execute, err = e.prepareWaitExecutor(definition, workflowStep, options, state, &metrics)
		if err != nil {
			traceStep(options, definition, workflowStep, diagnostic.PhaseRunner, diagnostic.StatusFailed, prepareStarted, "preparing wait", err)
			stepErr := fmt.Errorf("workflow %q step %q (%s): %w", definition.Name, workflowStep.ID, kind, err)
			finishStep(StatusFailed, stepErr, nil, 0)
			outcome.err = stepErr
			return outcome
		}
		traceStep(options, definition, workflowStep, diagnostic.PhaseRunner, diagnostic.StatusSucceeded, prepareStarted, "prepared wait", nil)
	} else {
		renderStarted := time.Now()
		traceStep(options, definition, workflowStep, diagnostic.PhaseRender, diagnostic.StatusStarted, time.Time{}, "rendering step configuration", nil)
		data := templateData(definition, options.RunDir, state)
		rendered, err := renderValue(options.renderer, workflowStep.With, data, workflowStep.Type == "lua")
		if err != nil {
			traceStep(options, definition, workflowStep, diagnostic.PhaseRender, diagnostic.StatusFailed, renderStarted, "", err)
			stepErr := fmt.Errorf("workflow %q step %q (%s): rendering configuration: %w", definition.Name, workflowStep.ID, workflowStep.Type, err)
			finishStep(StatusFailed, stepErr, nil, 0)
			outcome.err = stepErr
			return outcome
		}
		var configuration []diagnostic.Attribute
		if options.Diagnostics != nil {
			configuration = []diagnostic.Attribute{diagnostic.Attr("config", diagnostic.RedactedJSON(rendered))}
		}
		traceStep(options, definition, workflowStep, diagnostic.PhaseRender, diagnostic.StatusSucceeded, renderStarted, "", nil, configuration...)
		raw, ok := rendered.(map[string]any)
		if !ok {
			stepErr := fmt.Errorf("workflow %q step %q (%s): configuration is not an object", definition.Name, workflowStep.ID, workflowStep.Type)
			finishStep(StatusFailed, stepErr, nil, 0)
			outcome.err = stepErr
			return outcome
		}
		runnerStarted := time.Now()
		traceStep(options, definition, workflowStep, diagnostic.PhaseRunner, diagnostic.StatusStarted, time.Time{}, "building step runner", nil)
		runner, err := e.registry.Build(workflowStep.Type, raw)
		if err != nil {
			traceStep(options, definition, workflowStep, diagnostic.PhaseRunner, diagnostic.StatusFailed, runnerStarted, "", err)
			stepErr := fmt.Errorf("workflow %q step %q (%s): %w", definition.Name, workflowStep.ID, workflowStep.Type, err)
			finishStep(StatusFailed, stepErr, nil, 0)
			outcome.err = stepErr
			return outcome
		}
		traceStep(options, definition, workflowStep, diagnostic.PhaseRunner, diagnostic.StatusSucceeded, runnerStarted, "", nil)
		execute = managedExecutor(options, workflowStep.ID, runner)
	}
	execution := e.runWithRetry(ctx, definition, workflowStep, options, state, execute)
	cleanup()
	finishStep(statusFromError(execution.err), execution.err, execution.attempts, execution.retryWait)
	if execution.err != nil {
		outcome.err = fmt.Errorf("workflow %q step %q (%s): %w", definition.Name, workflowStep.ID, kind, execution.err)
	}
	outcome.result = execution.result
	return outcome
}

func managedExecutor(options Options, stepID string, runner step.Runner) stepExecutor {
	return func(ctx context.Context, request step.Request) (step.Result, error) {
		result, err := runner.Run(ctx, request)
		if err != nil {
			return result, err
		}
		cleaner, ok := runner.(step.Cleaner)
		if !ok {
			return result, nil
		}
		cleanupResult := step.Result{
			Outputs:   cloneMap(result.Outputs),
			Variables: cloneMap(result.Variables),
		}
		options.runtime.registerCleanup(func() error {
			if err := cleaner.Cleanup(cleanupResult); err != nil {
				return fmt.Errorf("cleaning managed resources for step %q: %w", stepID, err)
			}
			return nil
		})
		return result, nil
	}
}

func commitStepResult(state *State, stepID string, result step.Result) {
	if result.Outputs == nil {
		result.Outputs = make(map[string]any)
	}
	state.Steps[stepID] = cloneAny(result.Outputs)
	for key, value := range result.Variables {
		state.Vars[key] = cloneAny(value)
		if state.writtenVars != nil {
			state.writtenVars[key] = struct{}{}
		}
	}
}

func leafStepCount(steps []workflow.Step) int {
	total := 0
	for _, workflowStep := range steps {
		if children, transparent := transparentChildSequences(workflowStep); transparent {
			for _, child := range children {
				total += leafStepCount(child.Steps)
			}
			continue
		}
		if workflowStep.Return != nil {
			continue
		}
		if workflowStep.Batch != nil || workflowStep.Foreach != nil || workflowStep.Matrix != nil {
			total++
			continue
		}
		total++
	}
	return total
}

func transparentChildSequences(workflowStep workflow.Step) ([]workflow.ChildSequence, bool) {
	if workflowStep.IsExecutorBlock() || workflowStep.IsWorkingDirectoryBlock() || workflowStep.IsConditionalBlock() || workflowStep.Concurrent != nil {
		return workflowStep.ChildSequences(), true
	}
	return nil, false
}

func rollupNestedMetrics(stats *RunStats, nested RunStats) {
	stats.Attempts += nested.Attempts
	stats.Retries += nested.Retries
	stats.RetryWait += nested.RetryWait
	stats.Polls += nested.Polls
	stats.PollWait += nested.PollWait
	stats.TimedOut += nested.TimedOut
}

func mergeRunStats(target *RunStats, source RunStats) {
	target.Steps = append(target.Steps, source.Steps...)
	target.Succeeded += source.Succeeded
	target.Failed += source.Failed
	target.Skipped += source.Skipped
	target.Canceled += source.Canceled
	target.TimedOut += source.TimedOut
	target.Attempts += source.Attempts
	target.Retries += source.Retries
	target.RetryWait += source.RetryWait
	target.Polls += source.Polls
	target.PollWait += source.PollWait
}

func bindingRoot(bindings map[string]any, name string) map[string]any {
	value, _ := bindings[name].(map[string]any)
	if value == nil {
		return map[string]any{}
	}
	return value
}

func executionKind(workflowStep workflow.Step) string {
	if workflowStep.Action != nil {
		return "uses"
	}
	return workflowStep.Type
}

func stepTimeout(workflowStep workflow.Step) time.Duration {
	if workflowStep.Timeout == nil {
		return 0
	}
	return workflowStep.Timeout.Value()
}

func reportStepFinished(options Options, workflowName, stepID, stepType string, index, total int, stats StepStats) {
	report(options, ProgressEvent{
		Kind: StepFinished, Status: stats.Status, Time: stats.StartedAt.Add(stats.Duration),
		WorkflowName: workflowName, Depth: options.depth, StepID: stepID, StepType: stepType,
		Index: index, Total: total, Attempt: len(stats.Attempts), Duration: stats.Duration,
		Polls: stats.Polls, PollWait: stats.PollWait, Error: stats.Error,
	})
}

func recordStep(stats *RunStats, stepStats StepStats) {
	stats.Steps = append(stats.Steps, stepStats)
	switch stepStats.Status {
	case StatusSucceeded:
		stats.Succeeded++
	case StatusSkipped:
		stats.Skipped++
	case StatusCanceled:
		stats.Canceled++
	default:
		stats.Failed++
	}
	stats.Attempts += len(stepStats.Attempts)
	stats.Retries += max(0, len(stepStats.Attempts)-1)
	stats.RetryWait += stepStats.RetryWait
	stats.Polls += stepStats.Polls
	stats.PollWait += stepStats.PollWait
	timedOutAttempts := 0
	for _, attempt := range stepStats.Attempts {
		if attempt.Status == StatusTimedOut {
			timedOutAttempts++
		}
	}
	stats.TimedOut += timedOutAttempts
	if stepStats.Status == StatusTimedOut && timedOutAttempts == 0 {
		stats.TimedOut++
	}
}

func statusFromError(err error) ExecutionStatus {
	if err == nil {
		return StatusSucceeded
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return StatusTimedOut
	}
	if errors.Is(err, context.Canceled) {
		return StatusCanceled
	}
	return StatusFailed
}

func initialState(definition *workflow.Definition, options Options) (*State, error) {
	vars, environment, err := workflow.PrepareValues(definition, workflow.LoadOptions{Vars: options.Vars, Env: options.Env, BaseEnv: options.BaseEnv, RunDir: options.RunDir})
	if err != nil {
		return nil, err
	}
	return &State{
		Inputs: cloneMap(options.inputs), Vars: vars, Env: environment, Steps: make(map[string]any), Outputs: make(map[string]any),
		Dependencies: cloneDependencies(options.Dependencies),
	}, nil
}

func templateData(definition *workflow.Definition, runDir string, state *State) map[string]any {
	return workflow.TemplateDataWithDependencies(definition, runDir, state.Inputs, state.Vars, state.Env, state.Steps, state.Dependencies, state.Bindings)
}

func makeRequest(definition *workflow.Definition, stepID string, options Options, state *State, attempt, maxAttempts int, operationID string) step.Request {
	return step.Request{
		StepID: stepID, WorkflowName: definition.Name, WorkflowSource: definition.Location.Source, WorkflowDir: definition.Dir,
		RunDir: options.RunDir, LocalValueDir: options.LocalValueDir, GlobalValueDir: options.GlobalValueDir,
		Inputs: cloneMap(state.Inputs), Vars: cloneMap(state.Vars), Env: maps.Clone(state.Env),
		Steps: cloneMap(state.Steps), Dependencies: cloneDependencies(state.Dependencies), Bindings: cloneMap(state.Bindings), Stdin: options.Stdin, Stdout: options.Stdout,
		Stderr: options.Stderr, Interactive: options.Interactive,
		Attempt: attempt, MaxAttempts: maxAttempts, OperationID: operationID,
		Executor: options.Executor,
	}
}

func (e *Engine) validateAction(ctx context.Context, definition *workflow.Definition, workflowStep workflow.Step, options Options, state *State) error {
	if workflowStep.Action == nil {
		return fmt.Errorf("action was not resolved by the workflow loader")
	}
	if err := validateActionBindings(workflowStep.Action, workflowStep.With, options.renderer); err != nil {
		return err
	}
	if err := workflowStep.Action.ValidateReturnContracts(); err != nil {
		return err
	}
	for name, output := range workflowStep.Action.Outputs {
		if _, err := wukoexpr.Compile(output.Value, expr.AllowUndefinedVariables()); err != nil {
			return fmt.Errorf("output %q: %w", name, err)
		}
	}
	dir, cleanup, err := workflowStep.Action.Materialize()
	if err != nil {
		return err
	}
	defer cleanup()
	inputs := actionValidationInputs(workflowStep.Action)
	inner := &workflow.Definition{Version: 1, Name: workflowStep.Action.Name, Templates: workflowStep.Action.Templates, Dir: dir, Steps: workflowStep.Action.Steps, Finally: workflowStep.Action.Finally, Vars: map[string]any{}, Env: workflow.Environment{}, Location: workflowStep.Action.Location}
	return e.Validate(ctx, inner, Options{
		inputs: inputs, BaseEnv: state.Env, RunDir: options.RunDir,
		LocalValueDir: options.LocalValueDir, GlobalValueDir: options.GlobalValueDir,
		Stdin: options.Stdin, Stdout: options.Stdout, Stderr: options.Stderr, Interactive: options.Interactive,
		Diagnostics: options.Diagnostics, depth: options.depth + 1, runtime: options.runtime,
	})
}

func (e *Engine) prepareActionExecutor(definition *workflow.Definition, workflowStep workflow.Step, options Options, state *State) (stepExecutor, func(), error) {
	inputs, err := resolveActionInputs(workflowStep.Action, workflowStep.With, options.renderer, templateData(definition, options.RunDir, state))
	if err != nil {
		return nil, nil, err
	}
	if options.Diagnostics != nil {
		traceStep(options, definition, workflowStep, diagnostic.PhaseActionInputs, diagnostic.StatusDetail, time.Time{}, "resolved action inputs", nil, diagnostic.Attr("config", diagnostic.RedactedJSON(inputs)))
	}
	dir, cleanup, err := workflowStep.Action.Materialize()
	if err != nil {
		return nil, nil, err
	}
	inner := &workflow.Definition{Version: 1, Name: workflowStep.Action.Name, Templates: workflowStep.Action.Templates, Dir: dir, Steps: workflowStep.Action.Steps, Finally: workflowStep.Action.Finally, Vars: map[string]any{}, Env: workflow.Environment{}, Location: workflowStep.Action.Location}
	execute := func(ctx context.Context, request step.Request) (step.Result, error) {
		innerState, err := e.Run(ctx, inner, Options{
			inputs: inputs, BaseEnv: state.Env, RunDir: options.RunDir,
			LocalValueDir: options.LocalValueDir, GlobalValueDir: options.GlobalValueDir,
			Stdin: options.Stdin, Stdout: options.Stdout, Stderr: options.Stderr,
			Interactive: options.Interactive, Progress: options.Progress,
			Diagnostics:     options.Diagnostics,
			operationPrefix: request.OperationID, depth: options.depth + 1, runtime: options.runtime,
		})
		if err != nil {
			return step.Result{}, err
		}
		if innerState.didReturn {
			return step.Result{Outputs: cloneMap(innerState.Outputs)}, nil
		}
		environment := map[string]any{"inputs": innerState.Inputs, "vars": innerState.Vars, "steps": innerState.Steps, "env": innerState.Env, "workflow": map[string]any{"name": inner.Name, "dir": inner.Dir}, "run": map[string]any{"dir": options.RunDir}}
		outputs := make(map[string]any, len(workflowStep.Action.Outputs))
		outputsStarted := time.Now()
		traceStep(options, definition, workflowStep, diagnostic.PhaseActionOutputs, diagnostic.StatusStarted, time.Time{}, "evaluating action outputs", nil)
		for name, output := range workflowStep.Action.Outputs {
			value, err := wukoexpr.Eval(output.Value, environment)
			if err != nil {
				traceStep(options, definition, workflowStep, diagnostic.PhaseActionOutputs, diagnostic.StatusFailed, outputsStarted, "", err)
				return step.Result{}, fmt.Errorf("evaluating output %q: %w", name, err)
			}
			if !workflow.ActionDataValue(value) {
				err := fmt.Errorf("output %q is not a YAML/JSON-compatible value", name)
				traceStep(options, definition, workflowStep, diagnostic.PhaseActionOutputs, diagnostic.StatusFailed, outputsStarted, "", err)
				return step.Result{}, err
			}
			outputs[name] = cloneAny(value)
		}
		traceStep(options, definition, workflowStep, diagnostic.PhaseActionOutputs, diagnostic.StatusSucceeded, outputsStarted, "", nil, diagnostic.Attr("outputs", fmt.Sprint(len(outputs))))
		return step.Result{Outputs: outputs}, nil
	}
	return execute, cleanup, nil
}

func validateActionBindings(action *workflow.Action, bindings map[string]any, renderer *workflow.Renderer) error {
	for name, value := range bindings {
		input, ok := action.Inputs[name]
		if !ok {
			return fmt.Errorf("unknown input %q", name)
		}
		if mapping, ok := value.(map[string]any); ok && len(mapping) == 1 {
			if expression, exists := mapping["expr"]; exists {
				text, ok := expression.(string)
				if !ok || strings.TrimSpace(text) == "" {
					return fmt.Errorf("input %q expr must be a non-empty string", name)
				}
				if _, err := wukoexpr.Compile(text, expr.AllowUndefinedVariables()); err != nil {
					return fmt.Errorf("input %q expr: %w", name, err)
				}
				continue
			}
			if literal, exists := mapping["literal"]; exists && !workflow.ActionValueMatches(input.Type, literal) {
				return fmt.Errorf("input %q literal does not match type %s", name, input.Type)
			}
			if literal, exists := mapping["literal"]; exists {
				value = literal
			}
		}
		if !workflow.ActionValueMatches(input.Type, value) {
			return fmt.Errorf("input %q value does not match type %s", name, input.Type)
		}
		if !workflow.ActionDataValue(value) {
			return fmt.Errorf("input %q is not a YAML/JSON-compatible value", name)
		}
		if err := validateTemplates(renderer, value, false); err != nil {
			return fmt.Errorf("input %q template: %w", name, err)
		}
	}
	for name, input := range action.Inputs {
		if _, ok := bindings[name]; !ok && input.Required {
			return fmt.Errorf("required input %q is missing", name)
		}
	}
	return nil
}

func actionValidationInputs(action *workflow.Action) map[string]any {
	values := make(map[string]any, len(action.Inputs))
	for name, input := range action.Inputs {
		if input.HasDefault {
			values[name] = cloneAny(input.Default)
			continue
		}
		switch input.Type {
		case "string":
			values[name] = ""
		case "boolean":
			values[name] = false
		case "number":
			values[name] = 0
		case "array":
			values[name] = []any{}
		case "object":
			values[name] = map[string]any{}
		}
	}
	return values
}

func resolveActionInputs(action *workflow.Action, bindings map[string]any, renderer *workflow.Renderer, data map[string]any) (map[string]any, error) {
	if err := validateActionBindings(action, bindings, renderer); err != nil {
		return nil, err
	}
	values := actionValidationInputs(action)
	for name, input := range action.Inputs {
		value, exists := bindings[name]
		if !exists {
			if input.HasDefault {
				values[name] = cloneAny(input.Default)
			}
			continue
		}
		var err error
		if mapping, ok := value.(map[string]any); ok && len(mapping) == 1 {
			if expression, ok := mapping["expr"].(string); ok {
				value, err = wukoexpr.Eval(expression, data)
				if err != nil {
					return nil, fmt.Errorf("evaluating input %q: %w", name, err)
				}
			} else if literal, ok := mapping["literal"]; ok {
				value = literal
			}
		} else {
			value, err = renderValue(renderer, value, data, false)
			if err != nil {
				return nil, fmt.Errorf("rendering input %q: %w", name, err)
			}
		}
		if !workflow.ActionValueMatches(input.Type, value) {
			return nil, fmt.Errorf("input %q value does not match type %s", name, input.Type)
		}
		if !workflow.ActionDataValue(value) {
			return nil, fmt.Errorf("input %q is not a YAML/JSON-compatible value", name)
		}
		values[name] = cloneAny(value)
	}
	return values, nil
}

func cloneMap(source map[string]any) map[string]any {
	result := make(map[string]any, len(source))
	for key, value := range source {
		result[key] = cloneAny(value)
	}
	return result
}

func cloneDependencies(source map[string]map[string]any) map[string]map[string]any {
	return workflow.CloneDependencies(source)
}

func cloneAny(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneMap(typed)
	case []any:
		result := make([]any, len(typed))
		for i, item := range typed {
			result[i] = cloneAny(item)
		}
		return result
	default:
		return value
	}
}
