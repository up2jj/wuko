package engine

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"sync/atomic"
	"time"

	"github.com/up2jj/wuko/diagnostic"
	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/workflow"
)

// BackgroundControl is the engine extension point for ready background controls.
// Implementations own configuration and scheduling; the engine owns state isolation,
// registration, lifecycle reporting, and body execution.
type BackgroundControl interface {
	Kind() string
	Matches(workflow.Step) bool
	Body(workflow.Step) []workflow.Step
	BindingRoot() string
	Configuration(workflow.Step) any
	Validate(context.Context, workflow.Step) error
	Launch(context.Context, BackgroundControlRequest) (BackgroundControlProgram, error)
}

type BackgroundControlRequest struct {
	Step   workflow.Step
	RunDir string
	// Env is the workflow environment visible where the control was declared. Controls that
	// run commands start from it instead of the bare host environment.
	Env    map[string]string
	Render func(any) (any, error)
}

type BackgroundControlProgram struct {
	Result step.Result
	Run    func(context.Context, BackgroundControlRuntime) (BackgroundControlSummary, error)
	Close  func() error
}

type BackgroundControlRuntime struct {
	RunIteration func(context.Context, map[string]any) error
	Report       func(BackgroundControlEvent)
}

type BackgroundControlSummary struct {
	Iterations int
}

type BackgroundControlEventKind uint8

const (
	BackgroundIterationStarted BackgroundControlEventKind = iota
	BackgroundIterationFinished
	BackgroundTriggerHandled
)

type BackgroundControlEvent struct {
	Kind      BackgroundControlEventKind
	Iteration int
	Action    string
	StartedAt time.Time
	Duration  time.Duration
	Error     error
}

func WithBackgroundControl(control BackgroundControl) Option {
	return func(engine *Engine) {
		if control != nil {
			engine.backgroundControls = append(engine.backgroundControls, control)
		}
	}
}

func (e *Engine) backgroundControl(workflowStep workflow.Step) BackgroundControl {
	for _, control := range e.backgroundControls {
		if control.Matches(workflowStep) {
			return control
		}
	}
	return nil
}

func (e *Engine) validateBackgroundControl(ctx context.Context, definition *workflow.Definition, workflowStep workflow.Step, options Options, state *State, control BackgroundControl) error {
	if err := validateTemplates(options.renderer, control.Configuration(workflowStep), false); err != nil {
		return fmt.Errorf("%s configuration: %w", control.Kind(), err)
	}
	if err := control.Validate(ctx, workflowStep); err != nil {
		return err
	}
	childOptions := options
	childOptions.depth++
	childOptions.Interactive = false
	childOptions.Stdin = nil
	return e.validateSteps(ctx, definition, control.Body(workflowStep), childOptions, cloneState(state))
}

func (e *Engine) executeBackgroundControl(ctx context.Context, definition *workflow.Definition, workflowStep workflow.Step, options Options, state *State, index, total int, control BackgroundControl) stepOutcome {
	started := time.Now()
	kind := control.Kind()
	outcome := stepOutcome{started: true}
	finish := func(status ExecutionStatus, err error) {
		outcome.stats = StepStats{StepRunID: options.stepRunID, ID: workflowStep.ID, Type: kind, Index: index, Status: status, StartedAt: started, Duration: time.Since(started), Error: err}
		reportStepFinished(options, definition.Name, workflowStep.ID, kind, index, total, outcome.stats)
	}
	fail := func(cause error) stepOutcome {
		stepErr := fmt.Errorf("workflow %q step %q (%s): %w", definition.Name, workflowStep.ID, kind, cause)
		finish(statusFromError(stepErr), stepErr)
		outcome.err = stepErr
		return outcome
	}
	report(options, ProgressEvent{Kind: StepStarted, Status: StatusRunning, Time: started, WorkflowName: definition.Name, Depth: options.depth, StepID: workflowStep.ID, StepType: kind, Index: index, Total: total})

	baseline := cloneState(state)
	program, err := control.Launch(ctx, BackgroundControlRequest{
		Step: workflowStep, RunDir: options.RunDir, Env: maps.Clone(state.Env),
		Render: func(value any) (any, error) {
			return renderValue(options.renderer, value, templateData(definition, options.RunDir, state), false)
		},
	})
	if err != nil {
		return fail(err)
	}
	if options.runtime == nil || options.runtime.background == nil {
		if program.Close != nil {
			_ = program.Close()
		}
		return fail(fmt.Errorf("background supervisor is unavailable"))
	}

	jobOptions := options
	jobOptions.depth++
	jobOptions.stepRunID = ""
	jobOptions.Interactive = false
	jobOptions.Stdin = nil
	if err := options.runtime.background.start(workflowStep.ID, kind, func(jobCtx context.Context) error {
		return e.runBackgroundProgram(jobCtx, definition, workflowStep, jobOptions, baseline, control, program)
	}); err != nil {
		if program.Close != nil {
			_ = program.Close()
		}
		return fail(err)
	}

	outcome.result = program.Result
	finish(StatusSucceeded, nil)
	traceStep(options, definition, workflowStep, diagnostic.PhaseControl, diagnostic.StatusSucceeded, started, kind+" registered", nil)
	return outcome
}

func (e *Engine) runBackgroundProgram(ctx context.Context, definition *workflow.Definition, workflowStep workflow.Step, options Options, baseline *State, control BackgroundControl, program BackgroundControlProgram) (runErr error) {
	started := time.Now()
	kind := control.Kind()
	report(options, ProgressEvent{Kind: BackgroundStarted, Status: StatusRunning, Time: started, WorkflowName: definition.Name, Depth: options.depth - 1, StepID: workflowStep.ID, StepType: kind})
	report(options, ProgressEvent{Kind: ControlStarted, Status: StatusRunning, Time: started, WorkflowName: definition.Name, Depth: options.depth - 1, StepID: workflowStep.ID, ControlKind: kind, MaxConcurrency: 1})
	summary := BackgroundControlSummary{}
	var succeeded atomic.Int64
	defer func() {
		if program.Close != nil {
			if closeErr := program.Close(); closeErr != nil {
				runErr = errors.Join(runErr, fmt.Errorf("closing %s: %w", kind, closeErr))
			}
		}
		finished := time.Now()
		status := statusFromError(runErr)
		report(options, ProgressEvent{Kind: ControlFinished, Status: status, Time: finished, WorkflowName: definition.Name, Depth: options.depth - 1, StepID: workflowStep.ID, ControlKind: kind, Iterations: summary.Iterations, Started: summary.Iterations, Succeeded: int(succeeded.Load()), Duration: finished.Sub(started), Error: runErr})
		report(options, ProgressEvent{Kind: BackgroundFinished, Status: status, Time: finished, WorkflowName: definition.Name, Depth: options.depth - 1, StepID: workflowStep.ID, StepType: kind, Duration: finished.Sub(started), Error: runErr})
		if nonCancellationError(runErr) != nil {
			traceStep(options, definition, workflowStep, diagnostic.PhaseControl, diagnostic.StatusFailed, started, kind+" stopped", runErr)
		}
	}()

	runtime := BackgroundControlRuntime{
		RunIteration: func(iterationCtx context.Context, binding map[string]any) error {
			bodyState := cloneState(baseline)
			if bodyState.Bindings == nil {
				bodyState.Bindings = make(map[string]any)
			}
			bodyState.Bindings[control.BindingRoot()] = cloneMap(binding)
			body := control.Body(workflowStep)
			bodyTotal := leafStepCount(body)
			stats := RunStats{StartedAt: time.Now(), Total: bodyTotal, Steps: make([]StepStats, 0, bodyTotal)}
			// Managed resources belong to the iteration that created them: a rerunning
			// observer would otherwise hold every container and temporary directory it
			// ever started until the workflow exits.
			iterationOptions := options
			iterationOptions.cleanups = &cleanupScope{}
			err := e.executeSequence(iterationCtx, definition, body, iterationOptions, bodyState, &stats, 1, bodyTotal)
			cleanupErrors := iterationOptions.cleanups.run(context.WithoutCancel(iterationCtx))
			return errors.Join(append([]error{err}, cleanupErrors...)...)
		},
		Report: func(event BackgroundControlEvent) {
			if event.Kind == BackgroundIterationFinished && event.Error == nil {
				succeeded.Add(1)
			}
			reportBackgroundControlEvent(options, definition, workflowStep, kind, event)
		},
	}
	summary, runErr = program.Run(ctx, runtime)
	return runErr
}

func reportBackgroundControlEvent(options Options, definition *workflow.Definition, workflowStep workflow.Step, kind string, event BackgroundControlEvent) {
	switch event.Kind {
	case BackgroundIterationStarted:
		report(options, ProgressEvent{Kind: IterationStarted, Status: StatusRunning, Time: event.StartedAt, WorkflowName: definition.Name, Depth: options.depth, StepID: workflowStep.ID, ControlKind: kind, Iteration: event.Iteration - 1})
	case BackgroundIterationFinished:
		status := statusFromError(event.Error)
		if cancellationOnly(event.Error) {
			status = StatusCanceled
		}
		report(options, ProgressEvent{Kind: IterationFinished, Status: status, Time: event.StartedAt.Add(event.Duration), WorkflowName: definition.Name, Depth: options.depth, StepID: workflowStep.ID, ControlKind: kind, Iteration: event.Iteration - 1, Duration: event.Duration, Error: nonCancellationError(event.Error)})
	case BackgroundTriggerHandled:
		report(options, ProgressEvent{Kind: BackgroundTriggered, Status: StatusRunning, Time: time.Now(), WorkflowName: definition.Name, Depth: options.depth - 1, StepID: workflowStep.ID, StepType: kind, ControlKind: kind, Action: event.Action})
	}
}

func nonCancellationError(err error) error {
	if cancellationOnly(err) {
		return nil
	}
	return err
}
