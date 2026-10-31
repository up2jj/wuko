package engine

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/up2jj/wuko/diagnostic"
	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/validation"
	"github.com/up2jj/wuko/workflow"
)

func (e *Engine) Validate(ctx context.Context, definition *workflow.Definition, options Options) (validateErr error) {
	options = prepareWorkflowEnvironment(options)
	options = recordCaller(options, definition)
	options = scopeProcessReplacement(definition, options, true)
	// Only the selected workflow owns an audit; nested action validation adds to it.
	var audit *replacementAudit
	if options.ReplaceProcess != nil {
		audit = &replacementAudit{}
		options.replacementAudit = audit
	}
	if options.workflowInvoker == nil {
		options.workflowInvoker = e.workflowInvoker
	}
	if options.ElevatedExecutor == nil {
		options.ElevatedExecutor = e.elevatedExecutor
	}
	ctx = definition.PluginContext(ctx)
	options.secretSession = definition.SecretSession()
	// Command preflight calls Validate outside Run, so it redacts its own
	// result; Run's redaction of the same error is idempotent.
	if session := definition.SecretSession(); session != nil {
		defer func() { validateErr = session.RedactError(validateErr) }()
	}
	started := time.Now()
	trace(options, diagnostic.Event{Phase: diagnostic.PhaseValidation, Status: diagnostic.StatusStarted, Time: started, WorkflowName: definition.Name, Location: definition.Location, Message: "validating workflow"})
	if err := definition.ValidateStructure(); err != nil {
		err = definition.ValidationError(err, validation.CodeInvalidWorkflow, "", definition.Location, "")
		trace(options, diagnostic.Event{Phase: diagnostic.PhaseValidation, Status: diagnostic.StatusFailed, WorkflowName: definition.Name, Location: definition.Location, Duration: time.Since(started), Error: err})
		return err
	}
	if err := validateWorkflowOutputExpressions(definition); err != nil {
		err = definition.ValidationError(err, validation.CodeInvalidExpression, "outputs", definition.Location, "")
		trace(options, diagnostic.Event{Phase: diagnostic.PhaseValidation, Status: diagnostic.StatusFailed, WorkflowName: definition.Name, Location: definition.Location, Duration: time.Since(started), Error: err})
		return err
	}
	if options.renderer == nil {
		var err error
		options.renderer, err = workflow.NewRendererWithHelpers(ctx, definition.Templates, definition.SecretSession(), definition.Helpers())
		if err != nil {
			return definition.ValidationError(err, validation.CodeInvalidTemplate, "templates", definition.Location, "")
		}
	}
	state, err := initialState(definition, options)
	if err != nil {
		err = definition.ValidationError(err, validation.CodeInvalidValue, "", definition.Location, "")
		trace(options, diagnostic.Event{Phase: diagnostic.PhaseValues, Status: diagnostic.StatusFailed, WorkflowName: definition.Name, Location: definition.Location, Error: err})
		trace(options, diagnostic.Event{Phase: diagnostic.PhaseValidation, Status: diagnostic.StatusFailed, WorkflowName: definition.Name, Location: definition.Location, Duration: time.Since(started)})
		return err
	}
	providerBindings := make(map[string]struct{}, len(e.backgroundControls))
	for _, control := range e.backgroundControls {
		providerBindings[control.BindingRoot()] = struct{}{}
	}
	if err := state.Providers.ValidateNames(providerBindings); err != nil {
		err = definition.ValidationError(err, validation.CodeInvalidWorkflow, "", definition.Location, "")
		trace(options, diagnostic.Event{Phase: diagnostic.PhaseValidation, Status: diagnostic.StatusFailed, WorkflowName: definition.Name, Location: definition.Location, Duration: time.Since(started), Error: err})
		return err
	}
	if err := e.validateDataReferences(ctx, definition, options, state); err != nil {
		err = definition.ValidationError(err, validation.CodeUnavailableReference, "", definition.Location, "")
		trace(options, diagnostic.Event{Phase: diagnostic.PhaseValidation, Status: diagnostic.StatusFailed, WorkflowName: definition.Name, Location: definition.Location, Duration: time.Since(started), Error: err})
		return err
	}
	var issues validation.Collector
	if err := e.validateIndependentSteps(ctx, definition, definition.Steps, options, state); err != nil {
		if !issues.AddError(err) {
			return err
		}
	}
	if len(definition.Finally) > 0 {
		state.Bindings = map[string]any{"finally": map[string]any{
			"status": string(StatusSucceeded), "errors": []any{},
		}}
		if err := e.validateIndependentSteps(ctx, definition, definition.Finally, options, state); err != nil {
			if !issues.AddError(err) {
				return err
			}
		}
	}
	if err := e.validateLifecycleSteps(ctx, definition, "install", definition.Install, options); err != nil {
		if !issues.AddError(err) {
			return err
		}
	}
	if err := e.validateLifecycleSteps(ctx, definition, "uninstall", definition.Uninstall, options); err != nil {
		if !issues.AddError(err) {
			return err
		}
	}
	err = issues.Err()
	if err == nil {
		err = validateProcessReplacement(definition, options, audit)
	}
	status := diagnostic.StatusSucceeded
	if err != nil {
		status = diagnostic.StatusFailed
	}
	trace(options, diagnostic.Event{Phase: diagnostic.PhaseValidation, Status: status, WorkflowName: definition.Name, Location: definition.Location, Duration: time.Since(started)})
	return err
}

func (e *Engine) validateIndependentSteps(ctx context.Context, definition *workflow.Definition, steps []workflow.Step, options Options, state *State) error {
	var issues validation.Collector
	for _, declaration := range steps {
		if err := e.validateSteps(ctx, definition, []workflow.Step{declaration}, options, state); err != nil {
			if !issues.AddError(err) {
				return err
			}
		}
	}
	return issues.Err()
}

func (e *Engine) validateLifecycleSteps(ctx context.Context, definition *workflow.Definition, name string, steps []workflow.Step, options Options) error {
	if len(steps) == 0 {
		return nil
	}
	state, err := initialState(definition, options)
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	if err := e.validateIndependentSteps(ctx, definition, steps, options, state); err != nil {
		return validation.Prefix(err, name+": ")
	}
	return nil
}

func (e *Engine) validateSteps(ctx context.Context, definition *workflow.Definition, steps []workflow.Step, options Options, state *State) error {
	err := e.validateStepsRaw(ctx, definition, steps, options, state)
	if err == nil {
		return nil
	}
	var stepErr *stepValidationError
	if errors.As(err, &stepErr) {
		return definition.ValidationError(err, validation.CodeInvalidStep, stepErr.step.ValidationPath(), stepErr.step.Location, stepErr.step.ID)
	}
	return definition.ValidationError(err, validation.CodeInvalidStep, "", definition.Location, "")
}

func (e *Engine) validateStepsRaw(ctx context.Context, definition *workflow.Definition, steps []workflow.Step, options Options, state *State) (resultErr error) {
	var current workflow.Step
	defer func() {
		if resultErr == nil {
			return
		}
		var located *stepValidationError
		if !errors.As(resultErr, &located) {
			resultErr = &stepValidationError{step: current, err: resultErr}
		}
	}()
	for _, workflowStep := range steps {
		current = workflowStep
		if workflowStep.IsTryCatch() {
			if err := e.validateTryCatch(ctx, definition, workflowStep, options, state); err != nil {
				return fmt.Errorf("step %q: %w", workflowStep.ID, err)
			}
			continue
		}
		if workflowStep.IsCancelOn() {
			if err := e.validateCancelOn(ctx, definition, workflowStep, options, state); err != nil {
				return fmt.Errorf("step %q: %w", workflowStep.ID, err)
			}
			continue
		}
		if workflowStep.IsBackgroundControl() {
			control := e.backgroundControl(workflowStep)
			if control == nil {
				return fmt.Errorf("step %q: background control %q is not registered", workflowStep.ID, workflowStep.BackgroundControlKind())
			}
			if err := e.validateBackgroundControl(ctx, definition, workflowStep, options, state, control); err != nil {
				return fmt.Errorf("step %q: %w", workflowStep.ID, err)
			}
			continue
		}
		if workflowStep.IsExecutorBlock() {
			if err := e.validateExecutorBlock(ctx, definition, workflowStep, options, state); err != nil {
				return err
			}
			continue
		}
		if workflowStep.IsEnvironmentBlock() {
			if err := e.validateEnvironmentBlock(ctx, definition, workflowStep, options, state); err != nil {
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
		if workflowStep.IsWorktreeBlock() {
			if err := e.validateWorktreeBlock(ctx, definition, workflowStep, options, state); err != nil {
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
			if workflowStep.If != "" {
				if _, err := e.compileCondition(workflowStep.If); err != nil {
					return fail(fmt.Errorf("conditional block if: %w", err))
				}
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
		if workflowStep.IsOnce() {
			if err := e.validateOnce(ctx, definition, workflowStep, options, state); err != nil {
				return fmt.Errorf("step %q: %w", workflowStep.ID, err)
			}
			continue
		}
		if workflowStep.IsAttempt() {
			if err := e.validateAttempt(ctx, definition, workflowStep, options, state); err != nil {
				return fmt.Errorf("step %q: %w", workflowStep.ID, err)
			}
			continue
		}
		if workflowStep.Batch != nil || workflowStep.Foreach != nil || workflowStep.Matrix != nil {
			if err := e.validateControl(ctx, definition, workflowStep, options, state); err != nil {
				return fmt.Errorf("step %q: %w", workflowStep.ID, err)
			}
			continue
		}
		if workflowStep.Loop != nil {
			if err := e.validateLoop(ctx, definition, workflowStep, options, state); err != nil {
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
			if _, err := e.compileCondition(workflowStep.If); err != nil {
				traceStep(options, definition, workflowStep, diagnostic.PhaseValidation, diagnostic.StatusFailed, started, "compiling condition", err)
				return fmt.Errorf("step %q if: %w", workflowStep.ID, err)
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
		validationConfig, err := e.prepareStepConfig(definition, workflowStep, options, state, true)
		if err != nil {
			traceStep(options, definition, workflowStep, diagnostic.PhaseValidation, diagnostic.StatusFailed, started, "validating templates", err)
			return fmt.Errorf("step %q template: %w", workflowStep.ID, err)
		}
		runner, err := e.registry.BuildContext(ctx, workflowStep.Type, validationConfig)
		if err != nil {
			traceStep(options, definition, workflowStep, diagnostic.PhaseValidation, diagnostic.StatusFailed, started, "building runner", err)
			return fmt.Errorf("step %q: %w", workflowStep.ID, err)
		}
		options.replacementAudit.record(workflowStep, runner)
		if options.insideExecutor {
			if validator := options.executorStepValidator; validator != nil {
				if err := validator.ValidateStep(workflowStep.Type, validationConfig); err != nil {
					traceStep(options, definition, workflowStep, diagnostic.PhaseValidation, diagnostic.StatusFailed, started, "validating executor step restrictions", err)
					return fmt.Errorf("step %q: %w", workflowStep.ID, err)
				}
			}
			if _, ok := runner.(step.ExecutorAware); !ok {
				err := fmt.Errorf("step type %q is not supported inside executor blocks", workflowStep.Type)
				traceStep(options, definition, workflowStep, diagnostic.PhaseValidation, diagnostic.StatusFailed, started, "validating executor support", err)
				return fmt.Errorf("step %q: %w", workflowStep.ID, err)
			}
			if _, required := runner.(step.ExecutorFileSystem); required && !options.executorFileSystem {
				err := fmt.Errorf("step type %q requires an executor that exposes a filesystem", workflowStep.Type)
				traceStep(options, definition, workflowStep, diagnostic.PhaseValidation, diagnostic.StatusFailed, started, "validating executor filesystem support", err)
				return fmt.Errorf("step %q: %w", workflowStep.ID, err)
			}
		}
		validator, ok := runner.(step.Validator)
		if !ok || options.deferContextValidation {
			traceStep(options, definition, workflowStep, diagnostic.PhaseValidation, diagnostic.StatusSucceeded, started, "", nil)
			continue
		}
		_, workflowAware := runner.(step.WorkflowAware)
		_, inlineWorkflowAware := runner.(step.InlineWorkflowAware)
		request := e.makeRequest(ctx, definition, workflowStep.ID, options, state, 1, 1, "validation", workflowAware, inlineWorkflowAware)
		if err := validator.Validate(ctx, request); err != nil {
			traceStep(options, definition, workflowStep, diagnostic.PhaseValidation, diagnostic.StatusFailed, started, "validating runner", err)
			return fmt.Errorf("step %q: %w", workflowStep.ID, err)
		}
		traceStep(options, definition, workflowStep, diagnostic.PhaseValidation, diagnostic.StatusSucceeded, started, "", nil)
	}
	return nil
}
