package engine

import (
	"context"
	"fmt"
	"maps"
	"slices"

	"github.com/up2jj/wuko/diagnostic"
	"github.com/up2jj/wuko/provider"
	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/workflow"
)

type workflowInvocationEnvironment struct {
	env                map[string]string
	baseEnv            map[string]string
	environmentLoaders []string
	runDir             string
	providers          provider.Set
}

func prepareWorkflowEnvironment(options Options) Options {
	if options.workflowEnvironment != nil {
		return options
	}
	options.workflowEnvironment = &workflowInvocationEnvironment{
		env:                maps.Clone(options.Env),
		baseEnv:            cloneOptionalMap(options.BaseEnv),
		environmentLoaders: slices.Clone(options.EnvironmentLoaders),
		runDir:             options.RunDir,
		providers:          options.Providers.Clone(),
	}
	return options
}

// WorkflowInvoker resolves and executes blocking child-workflow calls for an Engine host.
// The command layer implements it because discovery and dependency planning are host concerns.
type WorkflowInvoker interface {
	ValidateWorkflow(context.Context, WorkflowInvocation) error
	RunWorkflow(context.Context, WorkflowInvocation) (map[string]any, error)
}

// WorkflowInvocation carries one call plus the immutable invocation settings inherited from its
// parent. Accessors deliberately expose only the values needed by a host implementation.
type WorkflowInvocation struct {
	call        step.WorkflowCall
	definition  *workflow.Definition
	options     Options
	operationID string
}

func (invocation WorkflowInvocation) Workflow() string { return invocation.call.Workflow }
func (invocation WorkflowInvocation) Target() string   { return invocation.call.Target }
func (invocation WorkflowInvocation) Vars() map[string]any {
	return workflow.CloneMap(invocation.call.Vars)
}

// DeclaredVars names the variables the caller supplies whose values this call does not yet
// know. It is empty for a run, where every value has been resolved.
func (invocation WorkflowInvocation) DeclaredVars() []string {
	return slices.Clone(invocation.call.DeferredVars)
}
func (invocation WorkflowInvocation) RunDir() string {
	if invocation.options.workflowEnvironment != nil {
		return invocation.options.workflowEnvironment.runDir
	}
	return invocation.options.RunDir
}

// recordCaller remembers the workflow file a run belongs to. A composite action runs as a
// synthetic definition without a path, and inherits the enclosing workflow's identity so a
// run_workflow call made inside an action still reports where the call came from.
func recordCaller(options Options, definition *workflow.Definition) Options {
	if definition == nil || definition.Path == "" {
		return options
	}
	options.callerName = definition.Name
	options.callerPath = definition.Path
	return options
}

// CallerName and CallerSource identify the active workflow for cycle diagnostics. They name the
// enclosing workflow file, not the composite action a call may sit inside.
func (invocation WorkflowInvocation) CallerName() string {
	if invocation.definition != nil && invocation.definition.Path != "" {
		return invocation.definition.Name
	}
	return invocation.options.callerName
}

func (invocation WorkflowInvocation) CallerSource() string {
	if invocation.definition != nil && invocation.definition.Path != "" {
		return invocation.definition.Path
	}
	return invocation.options.callerPath
}

// TrustedLocal is false for remote/stdin workflows and remote or plugin actions.
func (invocation WorkflowInvocation) TrustedLocal() bool {
	return invocation.definition != nil && invocation.options.ElevationAllowed && !invocation.definition.DirBorrowed
}

// LoadOptions returns the child preparation settings inherited from the original invocation.
// Vars contains only the explicit run_workflow values.
func (invocation WorkflowInvocation) LoadOptions(ensureSecretAuth bool) workflow.LoadOptions {
	environment := invocation.options.workflowEnvironment
	if environment == nil {
		prepared := prepareWorkflowEnvironment(invocation.options)
		environment = prepared.workflowEnvironment
	}
	return workflow.LoadOptions{
		Vars:               invocation.Vars(),
		DeclaredVars:       invocation.DeclaredVars(),
		Env:                maps.Clone(environment.env),
		BaseEnv:            cloneOptionalMap(environment.baseEnv),
		EnvironmentLoaders: slices.Clone(environment.environmentLoaders),
		RunDir:             environment.runDir,
		Diagnostics:        invocation.loadDiagnostics(),
		SecretSession:      invocation.definition.SecretSession(),
		EnsureSecretAuth:   ensureSecretAuth,
		Stdin:              invocation.options.Stdin,
		Stdout:             invocation.options.Stdout,
		Stderr:             invocation.options.Stderr,
		Interactive:        invocation.options.Interactive,
		Providers:          environment.providers.Clone(),
	}
}

func (invocation WorkflowInvocation) loadDiagnostics() diagnostic.Reporter {
	reporter := invocation.options.Diagnostics
	if reporter == nil {
		return nil
	}
	return func(event diagnostic.Event) {
		if event.InvocationID == "" {
			event.InvocationID = invocation.options.InvocationID
		}
		if event.ParentRunID == "" {
			event.ParentRunID = invocation.options.runID
		}
		if event.ParentStepRunID == "" {
			event.ParentStepRunID = invocation.options.stepRunID
		}
		if event.Depth == 0 {
			event.Depth = invocation.options.depth + 1
		}
		if runtime := invocation.options.runtime; runtime != nil && runtime.coordination != nil {
			runtime.coordination.reportMu.Lock()
			defer runtime.coordination.reportMu.Unlock()
		}
		reporter(event)
	}
}

// EngineOptions creates an isolated child workflow occurrence. Reporting locks and correlation
// are shared with the caller, while background services and managed cleanups remain child-owned.
func (invocation WorkflowInvocation) EngineOptions(dependencies map[string]map[string]any, localValueDir, globalValueDir string) Options {
	parent := invocation.options
	environment := parent.workflowEnvironment
	if environment == nil {
		prepared := prepareWorkflowEnvironment(parent)
		environment = prepared.workflowEnvironment
	}
	child := Options{
		InvocationID:        parent.InvocationID,
		Vars:                invocation.Vars(),
		DeclaredVars:        invocation.DeclaredVars(),
		Env:                 maps.Clone(environment.env),
		BaseEnv:             cloneOptionalMap(environment.baseEnv),
		EnvironmentLoaders:  slices.Clone(environment.environmentLoaders),
		RunDir:              environment.runDir,
		Dependencies:        cloneDependencies(dependencies),
		Providers:           environment.providers.Clone(),
		LocalValueDir:       localValueDir,
		GlobalValueDir:      globalValueDir,
		Stdin:               parent.Stdin,
		Stdout:              parent.Stdout,
		Stderr:              parent.Stderr,
		Interactive:         parent.Interactive,
		ElevatedExecutor:    parent.ElevatedExecutor,
		ElevationAllowed:    parent.ElevationAllowed,
		Progress:            parent.Progress,
		Diagnostics:         parent.Diagnostics,
		operationPrefix:     invocation.operationID,
		parentRunID:         parent.runID,
		parentStepRunID:     parent.stepRunID,
		depth:               parent.depth + 1,
		onceClaims:          parent.onceClaims,
		workflowInvoker:     parent.workflowInvoker,
		workflowEnvironment: environment,
	}
	if parent.runtime != nil {
		child.runtime = &runRuntime{coordination: parent.runtime.coordination}
		child.workflowRoot = true
	}
	return child
}

func cloneOptionalMap(source map[string]string) map[string]string {
	if source == nil {
		return nil
	}
	return maps.Clone(source)
}

type boundWorkflowRunner struct {
	invoker     WorkflowInvoker
	definition  *workflow.Definition
	options     Options
	operationID string
}

func (runner boundWorkflowRunner) invocation(call step.WorkflowCall) WorkflowInvocation {
	call.Vars = workflow.CloneMap(call.Vars)
	return WorkflowInvocation{call: call, definition: runner.definition, options: runner.options, operationID: runner.operationID}
}

func (runner boundWorkflowRunner) ValidateWorkflow(ctx context.Context, call step.WorkflowCall) error {
	if runner.invoker == nil {
		return fmt.Errorf("workflow invocation is unavailable")
	}
	return runner.invoker.ValidateWorkflow(ctx, runner.invocation(call))
}

func (runner boundWorkflowRunner) RunWorkflow(ctx context.Context, call step.WorkflowCall) (map[string]any, error) {
	if runner.invoker == nil {
		return nil, fmt.Errorf("workflow invocation is unavailable")
	}
	outputs, err := runner.invoker.RunWorkflow(ctx, runner.invocation(call))
	return workflow.CloneMap(outputs), err
}
