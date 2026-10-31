package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"time"

	controlpkg "github.com/up2jj/wuko/control"
	"github.com/up2jj/wuko/correlation"
	"github.com/up2jj/wuko/diagnostic"
	"github.com/up2jj/wuko/executor"
	storepkg "github.com/up2jj/wuko/keyvalue"
	"github.com/up2jj/wuko/process"
	"github.com/up2jj/wuko/provider"
	"github.com/up2jj/wuko/secret"
	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/workflow"
)

type Engine struct {
	registry           *step.Registry
	executors          *executor.Registry
	elevatedExecutor   process.Executor
	workflowInvoker    WorkflowInvoker
	backgroundControls []BackgroundControl
}

type stepValidationError struct {
	step workflow.Step
	err  error
}

func (err *stepValidationError) Error() string { return err.err.Error() }
func (err *stepValidationError) Unwrap() error { return err.err }

type Option func(*Engine)

func WithExecutors(registry *executor.Registry) Option {
	return func(engine *Engine) { engine.executors = registry }
}

// WithElevatedExecutor supplies the host-only executor used by explicitly elevated steps.
func WithElevatedExecutor(executor process.Executor) Option {
	return func(engine *Engine) { engine.elevatedExecutor = executor }
}

// WithWorkflowInvoker supplies the host capability used by the run_workflow step.
func WithWorkflowInvoker(invoker WorkflowInvoker) Option {
	return func(engine *Engine) { engine.workflowInvoker = invoker }
}

type Options struct {
	// InvocationID correlates this run with its surrounding reporting session. When empty, the
	// run remains valid and reporters may supply an invocation identity at delivery time.
	InvocationID correlation.InvocationID
	Vars         map[string]any
	// DeclaredVars names caller-supplied variables whose values are not yet known, so that
	// validating a workflow written to be driven by its caller does not report them unknown.
	// A run resolves every variable and leaves this empty.
	DeclaredVars []string
	Env          map[string]string
	// Dependencies contains outputs from direct prerequisite workflows keyed by alias.
	Dependencies map[string]map[string]any
	// Providers contains registered execution-provider schemas and active values.
	Providers provider.Set
	// BaseEnv overrides the current process environment when non-nil.
	BaseEnv            map[string]string
	EnvironmentLoaders []string
	RunDir             string
	// LocalValueDir is empty when local persistence is unavailable, such as for a remote workflow.
	LocalValueDir  string
	GlobalValueDir string
	Stdin          io.Reader
	Stdout         io.Writer
	Stderr         io.Writer
	Interactive    bool
	// Prompts answers prompt steps without asking, as --yes and --defaults request.
	Prompts          step.PromptAnswers
	DryRun           bool
	Executor         process.Executor
	ElevatedExecutor process.Executor
	// ElevationAllowed is true only when the complete workflow provenance is trusted local code.
	ElevationAllowed bool
	// OnReturn observes a return after its condition, expressions, and output contract succeed.
	// A destinationless return supplies the empty value. Hosts should attach the callback only to
	// the root workflow whose destination they intend to honor; nested action runs do not inherit it.
	OnReturn func(workflow.ReturnDestination)
	// ReplaceProcess accepts the program that replaces the Wuko process once the run ends.
	// Hosts attach it only to a directly invoked root workflow that they will exit after;
	// validation then admits a replacing step as that workflow's final top-level step.
	ReplaceProcess func(process.Replacement) error
	// Progress receives structured workflow, step, attempt, retry, poll, and timing events.
	// Calls are serialized against each other and against Diagnostics, so the callback
	// needs no locking of its own. It must not block indefinitely: every later event
	// queues behind it, though branch output no longer does.
	Progress func(ProgressEvent)
	// Diagnostics receives opt-in loading, validation, and execution phase events.
	// It is serialized on the same terms as Progress.
	Diagnostics            diagnostic.Reporter
	inputs                 map[string]any
	operationPrefix        string
	runID                  correlation.RunID
	parentRunID            correlation.RunID
	parentStepRunID        correlation.StepRunID
	stepRunID              correlation.StepRunID
	depth                  int
	runtime                *runRuntime
	defers                 *deferStack
	renderer               *workflow.Renderer
	deferContextValidation bool
	insideExecutor         bool
	executorFileSystem     bool
	executorStepValidator  executor.StepValidator
	services               step.ServiceLauncher
	// cleanups scopes managed-resource cleanup to a background control iteration, whose
	// resources must be released when the iteration ends rather than accumulating for
	// the life of the run. Nil everywhere else, meaning the run-level scope.
	// attempt, maxAttempts, operationID and previousPass are the ambient properties of the
	// innermost enclosing attempt control. A pass is the unit being repeated, so every step in
	// one body shares them. Outside an attempt they are zero, and executeStep supplies the
	// standalone defaults.
	attempt      int
	maxAttempts  int
	operationID  string
	previousPass map[string]step.Result
	// passFailure captures the partial result of the step that ends an attempt pass.
	passFailure   *passFailure
	cleanups      *cleanupScope
	secretSession *secret.Session
	// scopedEnv captures the active transparent env block for deferred cleanup, which may
	// execute after the block itself has restored State.Env.
	scopedEnv map[string]string
	// onceClaims records the once claims already held on the current path, keyed by
	// scope and key. It must travel into action invocations too: the lock is held per
	// open file description, so a second claim for the same key in this process would
	// block against itself forever instead of reporting the recursion. The claims
	// themselves are carried, not just their names, because a nested once has to
	// publish what it is waiting for on every claim its ancestors hold.
	onceClaims map[string]*storepkg.Claim
	// workflowRoot marks a nested run_workflow occurrence as the owner of an independent
	// background-service and cleanup lifecycle even though reporting coordination is shared.
	workflowRoot        bool
	workflowInvoker     WorkflowInvoker
	workflowEnvironment *workflowInvocationEnvironment
	initialState        *State
	// openParentReferences relaxes variable reference checks for inline callbacks validated
	// before the enclosing workflow has run: earlier steps may still write variables.
	openParentReferences bool
	// callerName and callerPath identify the innermost enclosing workflow file. A composite
	// action executes as a synthetic definition with no path of its own, so without these a
	// run_workflow call made from inside an action could not name the workflow it came from.
	callerName string
	callerPath string
	// replaceStepID names the final top-level step that may use ReplaceProcess.
	replaceStepID string
	// replacementAudit collects replacing and cleanup-owning steps during validation.
	replacementAudit *replacementAudit
}

// State carries one workflow's mutable values and is deliberately unsynchronized.
// Parallel execution never shares it: runConcurrent, executeControl, and executeCancelOn
// hand every branch a deep clone (see cloneState and workflow.Clone) and publish or merge
// results only after the group has joined. Concurrent dependencies enrich a clone with
// completed ancestor deltas without sharing the mutable State itself. The validation rules that keep this true live
// in validateSteps in workflow/types.go.
type State struct {
	Inputs             map[string]any
	Vars               map[string]any
	Env                map[string]string
	EnvironmentLoaders []string
	Steps              map[string]any
	// Outputs contains values produced by a return control or declared output expressions.
	Outputs map[string]any
	// Dependencies contains immutable outputs from direct prerequisite workflows.
	Dependencies map[string]map[string]any
	// Providers contains registered execution-provider schemas and active read-only values.
	Providers provider.Set
	// Bindings contains lifecycle and iteration-local roots such as batch, error, finally, foreach, and matrix.
	Bindings map[string]any
	Stats    RunStats
	// Replaced reports that the run ended by handing the process to the program resolved by
	// its final step. The host must replace itself with that program; Outputs stays empty.
	Replaced bool
	// presetVars is the variable set the run started with. It is fixed once the run
	// begins and shared by every derived state, so it is never mutated.
	presetVars  map[string]any
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

func (e *Engine) Run(ctx context.Context, definition *workflow.Definition, options Options) (*State, error) {
	return e.run(ctx, definition, options, true)
}

// RunValidated executes a definition already accepted by the command-level
// preflight pipeline. It prevents validation hooks from running twice while
// keeping Run's public behavior unchanged for direct engine callers.
func (e *Engine) RunValidated(ctx context.Context, definition *workflow.Definition, options Options) (*State, error) {
	return e.run(ctx, definition, options, false)
}

func (e *Engine) run(ctx context.Context, definition *workflow.Definition, options Options, validate bool) (runState *State, runErr error) {
	options = prepareWorkflowEnvironment(options)
	options = recordCaller(options, definition)
	if options.workflowInvoker == nil {
		options.workflowInvoker = e.workflowInvoker
	}
	if options.ElevatedExecutor == nil {
		options.ElevatedExecutor = e.elevatedExecutor
	}
	ctx = definition.PluginContext(ctx)
	options.secretSession = definition.SecretSession()
	if session := definition.SecretSession(); session != nil {
		defer func() { runErr = session.RedactError(runErr) }()
	}
	options = scopeProcessReplacement(definition, options, options.runtime == nil)
	rootRun := options.runtime == nil || options.workflowRoot
	// workflowRoot is cleared below so nested action runs do not inherit it, but the lifecycle
	// events still have to say that this occurrence is a child workflow rather than an action.
	childWorkflow := options.workflowRoot
	options = prepareRunOptions(options)
	options.workflowRoot = false
	if rootRun {
		options.runtime.background = newBackgroundSupervisor(ctx)
		options.services = options.runtime.background
		defer options.runtime.background.cancel(errBackgroundStopped)
		ctx = options.runtime.background.context()
	} else if options.services == nil {
		options.services = options.runtime.background
	}
	options.runID = correlation.NewRunID()
	options.stepRunID = ""
	if options.renderer == nil {
		options.renderer, runErr = workflow.NewRendererWithHelpers(ctx, definition.Templates, definition.SecretSession(), definition.Helpers())
		if runErr != nil {
			return nil, runErr
		}
	}
	state, err := initialState(definition, options)
	if err != nil {
		trace(options, diagnostic.Event{Phase: diagnostic.PhaseValues, Status: diagnostic.StatusFailed, WorkflowName: definition.Name, Location: definition.Location, Error: err})
		return nil, err
	}
	state.Stats = runStatsIdentity(options)
	if validate {
		if err := e.Validate(ctx, definition, options); err != nil {
			return nil, err
		}
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
	stats := runStatsIdentity(options)
	stats.StartedAt = startedAt
	stats.Total = total
	stats.Steps = make([]StepStats, 0, total)
	report(options, ProgressEvent{
		Kind: WorkflowStarted, Status: StatusRunning, Time: startedAt,
		WorkflowName: definition.Name, Depth: options.depth, Total: total,
		ChildWorkflow: childWorkflow,
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
			ChildWorkflow: childWorkflow, Replaced: runErr == nil && rootRun && options.runtime.replaced.Load(),
		})
	}()

	mainErr = e.executeSequence(ctx, definition, definition.Steps, options, state, &stats, 1, total)
	state.returning = false
	if rootRun {
		options.runtime.background.seal()
		if mainErr != nil || state.didReturn {
			options.runtime.background.stop(errBackgroundStopped)
		}
		if count := options.runtime.background.count(); count > 0 {
			report(options, ProgressEvent{Kind: BackgroundJoining, Status: StatusRunning, Time: time.Now(), WorkflowName: definition.Name, Depth: options.depth, Started: count})
		}
		backgroundErr := options.runtime.background.wait()
		if options.runtime.background.endedScope() && cancellationOnly(mainErr) {
			mainErr = nil
		}
		mainErr = joinBackgroundError(mainErr, backgroundErr)
	}
	cleanupCtx := context.WithoutCancel(ctx)
	cleanupErrors := e.executeCleanupScope(cleanupCtx, definition, options.defers, definition.Finally, options, state, &stats, mainErr, stats.Steps, mainTotal+1, total)
	if rootRun {
		cleanupErrors = append(cleanupErrors, options.runtime.cleanups.run(cleanupCtx)...)
	}
	runErr = errors.Join(append([]error{mainErr}, cleanupErrors...)...)
	if runErr != nil {
		return nil, runErr
	}
	if rootRun && options.runtime.replaced.Load() {
		state.Replaced = true
		// Validation forbids outputs alongside a replacing step: the replacement owns the
		// process from here, so nothing is published.
		return state, nil
	}
	if err := e.finishWorkflowOutputs(definition, options, state); err != nil {
		return nil, err
	}
	return state, nil
}

// RunSteps executes a standalone sequence using the workflow's values and runtime context.
// It is used for lifecycle hooks, which must not execute the workflow's main finally scope.
func (e *Engine) RunSteps(ctx context.Context, definition *workflow.Definition, steps []workflow.Step, options Options) (*State, error) {
	if definition == nil {
		return nil, fmt.Errorf("workflow definition is required")
	}
	ctx = definition.PluginContext(ctx)
	if len(steps) == 0 {
		return nil, nil
	}
	scoped := *definition
	scoped.Steps = steps
	scoped.Finally = nil
	scoped.Install = nil
	scoped.Uninstall = nil
	scoped.Outputs = nil
	return e.Run(ctx, &scoped, options)
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
	options.stepRunID = ""
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
		if workflowStep.IsEnvironmentBlock() {
			if err := e.executeEnvironmentBlock(ctx, definition, workflowStep, options, state, stats, index, total); err != nil {
				return err
			}
			index += leafStepCount(workflowStep.Steps)
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
		if workflowStep.IsWorktreeBlock() {
			stepOptions := withStepRun(options)
			outcome := e.executeWorktreeBlock(ctx, definition, workflowStep, stepOptions, state, index, total)
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
			commitStarted := time.Now()
			traceStep(stepOptions, definition, workflowStep, diagnostic.PhaseCommit, diagnostic.StatusStarted, time.Time{}, "committing worktree result", nil)
			commitStepResult(state, workflowStep.ID, outcome.result)
			traceStep(stepOptions, definition, workflowStep, diagnostic.PhaseCommit, diagnostic.StatusSucceeded, commitStarted, "", nil,
				diagnostic.Attr("outputs", fmt.Sprint(len(outcome.result.Outputs))))
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
			run, err := e.evaluateConditionalBlock(definition, workflowStep, options, state)
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
		stepOptions := withStepRun(options)
		var outcome stepOutcome
		if workflowStep.IsTryCatch() {
			outcome = e.executeTryCatch(ctx, definition, workflowStep, stepOptions, state, index, total)
		} else if workflowStep.IsCancelOn() {
			outcome = e.executeCancelOn(ctx, definition, workflowStep, stepOptions, state, index, total)
		} else if control := e.backgroundControl(workflowStep); control != nil {
			outcome = e.executeBackgroundControl(ctx, definition, workflowStep, stepOptions, state, index, total, control)
		} else if workflowStep.IsOnce() {
			outcome = e.executeOnce(ctx, definition, workflowStep, stepOptions, state, index, total)
		} else if workflowStep.Batch != nil || workflowStep.Foreach != nil || workflowStep.Matrix != nil {
			outcome = e.executeControl(ctx, definition, workflowStep, stepOptions, state, index, total)
		} else if workflowStep.IsAttempt() {
			outcome = e.executeAttempt(ctx, definition, workflowStep, stepOptions, state, index, total)
		} else if workflowStep.Loop != nil {
			outcome = e.executeLoop(ctx, definition, workflowStep, stepOptions, state, index, total)
		} else {
			outcome = e.executeStep(ctx, definition, workflowStep, stepOptions, state, index, total)
		}
		if outcome.started {
			recordStep(stats, outcome.stats)
			if outcome.nested != nil {
				rollupNestedMetrics(stats, *outcome.nested)
				if workflowStep.IsAttempt() {
					// A timed-out attempt is present both in the control's AttemptStats and in
					// the child stats for the pass that hit the same deadline. Count the
					// timeout once while preserving any additional nested timeouts.
					stats.TimedOut -= min(stepTimeoutCount(outcome.stats), outcome.nested.TimedOut)
				}
			}
		}
		index++
		if outcome.err != nil {
			return outcome.err
		}
		if outcome.skipped && !outcome.commitSkipped {
			continue
		}
		commitStarted := time.Now()
		traceStep(stepOptions, definition, workflowStep, diagnostic.PhaseCommit, diagnostic.StatusStarted, time.Time{}, "committing step result", nil)
		commitStepResult(state, workflowStep.ID, outcome.result)
		traceStep(stepOptions, definition, workflowStep, diagnostic.PhaseCommit, diagnostic.StatusSucceeded, commitStarted, "", nil,
			diagnostic.Attr("outputs", fmt.Sprint(len(outcome.result.Outputs))), diagnostic.Attr("variables", fmt.Sprint(len(outcome.result.Variables))))
		if len(workflowStep.Defer) > 0 {
			options.defers.register(workflowStep.ID, stepOptions)
		}
	}
	return nil
}

func (e *Engine) evaluateConditionalBlock(definition *workflow.Definition, workflowStep workflow.Step, options Options, state *State) (bool, error) {
	started := time.Now()
	trace(options, diagnostic.Event{
		Phase: diagnostic.PhaseCondition, Status: diagnostic.StatusStarted, Time: started,
		WorkflowName: definition.Name, Location: workflowStep.Location,
		Message: string(workflowStep.If),
	})
	run, err := e.evaluateCondition(workflowStep.If, makeConditionEnvironment(definition, options.RunDir, options.Interactive, state))
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
	options.stepRunID = ""
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
		stepOptions := withStepRun(options)
		stepStats := StepStats{
			StepRunID: stepOptions.stepRunID, ID: workflowStep.ID, Type: skippedStepKind(workflowStep), Index: index,
			Location: workflowStep.Location, Status: StatusSkipped, StartedAt: started,
		}
		reportStepFinished(stepOptions, definition.Name, workflowStep.ID, stepStats.Type, index, total, stepStats)
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
	// commitSkipped distinguishes a durable replay from a condition-based skip.
	commitSkipped bool
	nested        *RunStats
}

func (e *Engine) executeStep(ctx context.Context, definition *workflow.Definition, workflowStep workflow.Step, options Options, state *State, index, total int) stepOutcome {
	if err := ctx.Err(); err != nil {
		return stepOutcome{err: err}
	}
	kind := executionKind(workflowStep)
	stepStartedAt := time.Now()
	outcome := stepOutcome{started: true}
	finishStep := func(status ExecutionStatus, stepErr error, attempts []AttemptStats, retryWait time.Duration) {
		outcome.stats = StepStats{
			StepRunID: options.stepRunID, ID: workflowStep.ID, Type: kind, Index: index, Status: status,
			Location: workflowStep.Location, StartedAt: stepStartedAt, Duration: time.Since(stepStartedAt),
			RetryWait: retryWait, Attempts: attempts, Error: stepErr,
		}
		reportStepFinished(options, definition.Name, workflowStep.ID, kind, index, total, outcome.stats)
	}
	conditionStarted := time.Now()
	traceStep(options, definition, workflowStep, diagnostic.PhaseCondition, diagnostic.StatusStarted, time.Time{}, string(workflowStep.If), nil)
	run, err := e.evaluateCondition(workflowStep.If, makeConditionEnvironment(definition, options.RunDir, options.Interactive, state))
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
		StepType: kind, Index: index, Total: total,
	})
	var execute stepExecutor
	// An action step's own request never invokes a workflow: the steps inside the action get
	// their own requests, and each is bound according to its own runner.
	workflowAware := false
	inlineWorkflowAware := false
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
	} else {
		renderStarted := time.Now()
		traceStep(options, definition, workflowStep, diagnostic.PhaseRender, diagnostic.StatusStarted, time.Time{}, "rendering step configuration", nil)
		raw, err := e.prepareStepConfig(definition, workflowStep, options, state, false)
		if err != nil {
			traceStep(options, definition, workflowStep, diagnostic.PhaseRender, diagnostic.StatusFailed, renderStarted, "", err)
			stepErr := fmt.Errorf("workflow %q step %q (%s): rendering configuration: %w", definition.Name, workflowStep.ID, workflowStep.Type, err)
			finishStep(StatusFailed, stepErr, nil, 0)
			outcome.err = stepErr
			return outcome
		}
		var configuration []diagnostic.Attribute
		if options.Diagnostics != nil {
			configText := diagnostic.RedactedJSON(raw)
			if session := definition.SecretSession(); session != nil {
				configText = session.Redact(configText)
			}
			configuration = []diagnostic.Attribute{diagnostic.Attr("config", configText)}
		}
		traceStep(options, definition, workflowStep, diagnostic.PhaseRender, diagnostic.StatusSucceeded, renderStarted, "", nil, configuration...)
		runnerStarted := time.Now()
		traceStep(options, definition, workflowStep, diagnostic.PhaseRunner, diagnostic.StatusStarted, time.Time{}, "building step runner", nil)
		runner, err := e.registry.BuildContext(ctx, workflowStep.Type, raw)
		if err != nil {
			traceStep(options, definition, workflowStep, diagnostic.PhaseRunner, diagnostic.StatusFailed, runnerStarted, "", err)
			stepErr := fmt.Errorf("workflow %q step %q (%s): %w", definition.Name, workflowStep.ID, workflowStep.Type, err)
			finishStep(StatusFailed, stepErr, nil, 0)
			outcome.err = stepErr
			return outcome
		}
		traceStep(options, definition, workflowStep, diagnostic.PhaseRunner, diagnostic.StatusSucceeded, runnerStarted, "", nil)
		_, workflowAware = runner.(step.WorkflowAware)
		_, inlineWorkflowAware = runner.(step.InlineWorkflowAware)
		outputSchema, schemaErr := e.registry.OutputSchemaForConfig(ctx, workflowStep.Type, raw)
		if schemaErr != nil {
			stepErr := fmt.Errorf("workflow %q step %q (%s): resolving output contract: %w", definition.Name, workflowStep.ID, workflowStep.Type, schemaErr)
			finishStep(StatusFailed, stepErr, nil, 0)
			outcome.err = stepErr
			return outcome
		}
		execute = managedExecutor(options, workflowStep.ID, runner, outputSchema)
	}
	operationID := options.operationID
	if operationID == "" {
		// Every step carries an operation ID whether or not an attempt encloses it: nested
		// action runs hash it into their own, so an empty one would break their determinism.
		operationID, err = executionOperationID(definition, workflowStep.ID, "", options, state)
		if err != nil {
			cleanup()
			stepErr := fmt.Errorf("workflow %q step %q (%s): %w", definition.Name, workflowStep.ID, kind, err)
			finishStep(StatusFailed, stepErr, nil, 0)
			outcome.err = stepErr
			return outcome
		}
	}
	attempt, maximum := max(options.attempt, 1), max(options.maxAttempts, 1)
	request := e.makeRequest(ctx, definition, workflowStep.ID, options, state, attempt, max(maximum, attempt), operationID, workflowAware, inlineWorkflowAware)
	if previous, ok := options.previousPass[workflowStep.ID]; ok {
		request.PreviousAttempt = &previous
	}
	runStartedAt := time.Now()
	result, runErr := execute(ctx, request)
	cleanup()
	// Only the outermost executor of a pass counts it. Inside an attempt body the control
	// already reports one AttemptStats per pass, so a leaf that reported its own would double
	// count every execution in the run summary.
	var stats []AttemptStats
	if options.maxAttempts == 0 {
		stats = []AttemptStats{{
			Number: attempt, Status: statusFromError(runErr), StartedAt: runStartedAt,
			Duration: time.Since(runStartedAt), Error: runErr,
		}}
	}
	finishStep(statusFromError(runErr), runErr, stats, 0)
	if runErr != nil {
		// The partial result travels two ways: on the outcome, and into the enclosing attempt's
		// recorder, because a failed step never commits and executeSequence returns only an error.
		options.passFailure.record(workflowStep.ID, result.Outputs)
		outcome.result = result
		outcome.err = fmt.Errorf("workflow %q step %q (%s): %w", definition.Name, workflowStep.ID, kind, runErr)
		return outcome
	}
	outcome.result = result
	return outcome
}

// cleanupScope returns the scope that owns managed resources created on this path.
func (options Options) cleanupScope() *cleanupScope {
	if options.cleanups != nil {
		return options.cleanups
	}
	return &options.runtime.cleanups
}

func managedExecutor(options Options, stepID string, runner step.Runner, outputs step.OutputSchema) stepExecutor {
	return func(ctx context.Context, request step.Request) (step.Result, error) {
		result, err := runContained(ctx, runner, request)
		if err != nil {
			return result, err
		}
		// The contract is checked only after cleanup registration so a
		// misbehaving step cannot leak the resources it already acquired.
		contractErr := step.ValidateOutputKeys(outputs, result.Outputs)
		if contractErr != nil {
			contractErr = fmt.Errorf("step %q violated its registered output contract: %w", stepID, contractErr)
		}
		cleaner, ok := runner.(step.Cleaner)
		if !ok {
			return result, contractErr
		}
		cleanupResult := step.Result{
			Outputs:   cloneMap(result.Outputs),
			Variables: cloneMap(result.Variables),
		}
		options.cleanupScope().register(func(cleanupCtx context.Context) error {
			if err := cleaner.Cleanup(cleanupCtx, cleanupResult); err != nil {
				return fmt.Errorf("cleaning managed resources for step %q: %w", stepID, err)
			}
			return nil
		})
		return result, contractErr
	}
}

// runContained runs a step and turns a panic into the step's error, so a faulty runner fails its
// own step and the run still reaches cleanup, finally blocks and the final report.
func runContained(ctx context.Context, runner step.Runner, request step.Request) (result step.Result, err error) {
	defer func() {
		if failure := controlpkg.RecoveredPanic(recover()); failure != nil {
			result, err = step.Result{}, failure
		}
	}()
	return runner.Run(ctx, request)
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

func bindingRoot(bindings map[string]any, name string) map[string]any {
	value, _ := bindings[name].(map[string]any)
	if value == nil {
		return map[string]any{}
	}
	return value
}

func executionKind(workflowStep workflow.Step) string {
	if workflowStep.IsTryCatch() {
		return "try"
	}
	if workflowStep.IsCancelOn() {
		return "cancel_on"
	}
	if kind := workflowStep.BackgroundControlKind(); kind != "" {
		return kind
	}
	if workflowStep.IsWorktreeBlock() {
		return "worktree"
	}
	if workflowStep.IsOnce() {
		return "once"
	}
	if workflowStep.IsAttempt() {
		return "attempt"
	}
	if workflowStep.Action != nil {
		return "uses"
	}
	return workflowStep.Type
}

func initialState(definition *workflow.Definition, options Options) (*State, error) {
	if options.initialState != nil {
		return cloneState(options.initialState), nil
	}
	vars, environment, err := workflow.PrepareValues(definition, workflow.LoadOptions{Vars: options.Vars, DeclaredVars: options.DeclaredVars, Env: options.Env, BaseEnv: options.BaseEnv, EnvironmentLoaders: options.EnvironmentLoaders, RunDir: options.RunDir, SecretSession: definition.SecretSession(), Interactive: options.Interactive, Providers: options.Providers})
	if err != nil {
		return nil, err
	}
	return &State{
		Inputs: cloneMap(options.inputs), Vars: vars, Env: environment, EnvironmentLoaders: slices.Clone(options.EnvironmentLoaders), Steps: make(map[string]any), Outputs: make(map[string]any),
		Dependencies: cloneDependencies(options.Dependencies), Providers: options.Providers.Clone(), presetVars: cloneMap(vars),
	}, nil
}

func templateData(definition *workflow.Definition, runDir string, interactive bool, state *State) map[string]any {
	data := workflow.TemplateDataWithProviders(definition, runDir, state.EnvironmentLoaders, interactive, state.Inputs, state.Vars, state.Env, state.Steps, state.Dependencies, state.Bindings, state.Providers)
	for name, function := range definition.Helpers().Functions(definition.HelperContext()) {
		data[name] = function
	}
	return data
}

// makeRequest builds one step request. workflowAware carries whether the receiving runner
// invokes child workflows: the binding embeds a full copy of Options, so attaching it to every
// request would allocate half a kilobyte per step for a capability almost no step uses.
func (e *Engine) makeRequest(ctx context.Context, definition *workflow.Definition, stepID string, options Options, state *State, attempt, maxAttempts int, operationID string, workflowAware, inlineWorkflowAware bool) step.Request {
	var resolveSecret func(string) (string, error)
	if session := definition.SecretSession(); session != nil {
		resolveSecret = session.Resolve
	}
	var workflows step.WorkflowRunner
	if workflowAware {
		workflows = boundWorkflowRunner{invoker: options.workflowInvoker, definition: definition, options: options, operationID: operationID}
	}
	var inlineWorkflows step.InlineWorkflowRunner
	if inlineWorkflowAware {
		inlineWorkflows = boundInlineWorkflowRunner{engine: e, definition: definition, options: options, state: cloneState(state), operationID: operationID}
	}
	return step.Request{
		StepID: stepID, WorkflowName: definition.Name, WorkflowSource: definition.Location.Source, WorkflowDir: definition.Dir,
		WorkflowDirBorrowed: definition.DirBorrowed, WorkflowTimezone: definition.Timezone,
		RunDir: options.RunDir, EnvironmentLoaders: slices.Clone(state.EnvironmentLoaders), LocalValueDir: options.LocalValueDir, GlobalValueDir: options.GlobalValueDir,
		Inputs: cloneMap(state.Inputs), Vars: cloneMap(state.Vars), PresetVars: cloneMap(state.presetVars), Env: maps.Clone(state.Env),
		Steps: cloneMap(state.Steps), Dependencies: cloneDependencies(state.Dependencies), Providers: state.Providers.Clone(), Bindings: cloneMap(state.Bindings), Stdin: options.Stdin, Stdout: options.Stdout,
		Stderr: options.Stderr, Interactive: options.Interactive, Prompts: options.Prompts,
		Attempt: attempt, MaxAttempts: maxAttempts, OperationID: operationID,
		TemplateRenderer: newBoundTemplateRenderer(options.renderer, func() map[string]any {
			return templateData(definition, options.RunDir, options.Interactive, state)
		}),
		Executor: options.Executor, ElevatedExecutor: options.ElevatedExecutor,
		ElevationAllowed: options.ElevationAllowed, InsideExecutor: options.insideExecutor,
		Services:  reportingServiceLauncher{launcher: options.services, options: options, workflowName: definition.Name},
		Workflows: workflows, InlineWorkflows: inlineWorkflows,
		Secret: resolveSecret, Helpers: definition.Helpers(), HelperContext: ctx,
		ReplaceProcess: processReplacementHook(stepID, options, operationID),
	}
}

// cloneMap and cloneAny defer to workflow so branch isolation has exactly one
// definition; see workflow.Clone for what "cloned" guarantees.
func cloneMap(source map[string]any) map[string]any {
	return workflow.CloneMap(source)
}

func cloneDependencies(source map[string]map[string]any) map[string]map[string]any {
	return workflow.CloneDependencies(source)
}

func cloneAny(value any) any {
	return workflow.Clone(value)
}
