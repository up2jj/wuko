package cmd

import (
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/up2jj/wuko/engine"
	"github.com/up2jj/wuko/workflow"
)

type commandWorkflowInvoker struct {
	deps dependencies
}

// preparedInvocation is one resolved child-workflow call: the dependency plan to execute, the
// context carrying its cycle-detection frame, the per-node engine options, and the cache key
// identifying the call shape for validation reuse.
type preparedInvocation struct {
	plan       *workflow.DependencyPlan
	ctx        context.Context
	optionsFor func(*workflow.Definition, map[string]map[string]any) engine.Options
	key        string
}

func (invoker commandWorkflowInvoker) ValidateWorkflow(ctx context.Context, invocation engine.WorkflowInvocation) error {
	prepared, err := invoker.prepare(ctx, invocation, false)
	if err != nil {
		return err
	}
	return invoker.preflight(prepared)
}

func (invoker commandWorkflowInvoker) RunWorkflow(ctx context.Context, invocation engine.WorkflowInvocation) (map[string]any, error) {
	prepared, err := invoker.prepare(ctx, invocation, true)
	if err != nil {
		return nil, err
	}
	if err := invoker.preflight(prepared); err != nil {
		return nil, err
	}
	state, err := executeInvokedDependencyPlan(prepared.ctx, prepared.plan, invoker.engineFor(), prepared.optionsFor)
	if err != nil {
		return nil, err
	}
	return workflow.CloneMap(state.Outputs), nil
}

// preflight validates the plan unless an identical call shape was already validated in this
// command execution. The caller's own validation phase runs ValidateWorkflow for every
// run_workflow step, so without the memo each invocation would re-validate the whole subtree.
func (invoker commandWorkflowInvoker) preflight(prepared preparedInvocation) error {
	cache := invoker.deps.workflowCache
	if cache.alreadyValidated(prepared.key) {
		return nil
	}
	if err := preflightDependencyPlan(prepared.ctx, prepared.plan, invoker.engineFor(), prepared.optionsFor); err != nil {
		return err
	}
	cache.recordValidated(prepared.key)
	return nil
}

func (invoker commandWorkflowInvoker) engineFor() func() *engine.Engine {
	return func() *engine.Engine { return workflowEngine(invoker.deps) }
}

func (invoker commandWorkflowInvoker) prepare(ctx context.Context, invocation engine.WorkflowInvocation, ensureSecretAuth bool) (preparedInvocation, error) {
	if !invocation.TrustedLocal() {
		return preparedInvocation{}, fmt.Errorf("run_workflow is only available to trusted local workflows")
	}
	homeDir, err := callDirectory(invoker.deps.homeDir, os.UserHomeDir)
	if err != nil {
		return preparedInvocation{}, fmt.Errorf("resolving home directory: %w", err)
	}
	configDir, err := callDirectory(invoker.deps.configDir, os.UserConfigDir)
	if err != nil {
		return preparedInvocation{}, fmt.Errorf("resolving config directory: %w", err)
	}
	cache := invoker.deps.workflowCache
	runDir := invocation.RunDir()
	source, err := cache.find(runDir, homeDir, configDir, invocation.Workflow())
	if err != nil {
		return preparedInvocation{}, err
	}
	nested, err := enterWorkflowInvocation(ctx, invocation, source)
	if err != nil {
		return preparedInvocation{}, err
	}
	loader := invoker.deps.loader
	if loader == nil {
		loader = defaultWorkflowLoader(invoker.deps.plugins)
	}
	loadOptions := invocation.LoadOptions(ensureSecretAuth)
	loadOptions.Target = invocation.Target()
	definition, err := loader.Load(nested, source.Path, loadOptions)
	if err != nil {
		return preparedInvocation{}, err
	}
	if err := requireDirectlyInvokable(definition); err != nil {
		return preparedInvocation{}, err
	}
	plan, err := resolveDependencyPlanWith(nested, definition, loader, loadOptions, func() ([]workflow.Source, error) {
		return cache.discover(runDir, homeDir, configDir)
	})
	if err != nil {
		return preparedInvocation{}, err
	}
	nested, err = mergeInvocationPlugins(nested, plan)
	if err != nil {
		return preparedInvocation{}, err
	}
	globalValueDir := filepath.Join(configDir, "wuko", "values")
	optionsFor := func(definition *workflow.Definition, dependencies map[string]map[string]any) engine.Options {
		return invocation.EngineOptions(dependencies, filepath.Join(definition.Dir, ".wuko", "values"), globalValueDir)
	}
	return preparedInvocation{plan: plan, ctx: nested, optionsFor: optionsFor, key: validationKey(source, invocation)}, nil
}

func callDirectory(configured func() (string, error), fallback func() (string, error)) (string, error) {
	if configured == nil {
		configured = fallback
	}
	return configured()
}

type workflowInvocationContextKey struct{}

type workflowInvocationState struct {
	stack   []workflowInvocationFrame
	plugins map[string]workflow.PluginSource
}

type workflowInvocationFrame struct {
	name string
	path string
}

func enterWorkflowInvocation(ctx context.Context, invocation engine.WorkflowInvocation, source workflow.Source) (context.Context, error) {
	state, _ := ctx.Value(workflowInvocationContextKey{}).(workflowInvocationState)
	stack := append([]workflowInvocationFrame(nil), state.stack...)
	if len(stack) == 0 && invocation.CallerSource() != "" {
		stack = append(stack, workflowInvocationFrame{name: invocation.CallerName(), path: filepath.Clean(invocation.CallerSource())})
	}
	targetPath := filepath.Clean(source.Path)
	for index, frame := range stack {
		if frame.path != targetPath {
			continue
		}
		names := make([]string, 0, len(stack)-index+1)
		for _, active := range stack[index:] {
			names = append(names, active.name)
		}
		names = append(names, invocation.Workflow())
		return nil, fmt.Errorf("workflow invocation cycle: %s", strings.Join(names, " -> "))
	}
	stack = append(stack, workflowInvocationFrame{name: invocation.Workflow(), path: targetPath})
	return context.WithValue(ctx, workflowInvocationContextKey{}, workflowInvocationState{stack: stack, plugins: maps.Clone(state.plugins)}), nil
}

func mergeInvocationPlugins(ctx context.Context, plan *workflow.DependencyPlan) (context.Context, error) {
	state, _ := ctx.Value(workflowInvocationContextKey{}).(workflowInvocationState)
	declarations := maps.Clone(state.plugins)
	if declarations == nil {
		declarations = make(map[string]workflow.PluginSource)
		for namespace, source := range workflow.PluginsFromContext(ctx) {
			canonical, err := source.CanonicalSource()
			if err != nil {
				return nil, fmt.Errorf("plugin %q: %w", namespace, err)
			}
			source.Source = canonical
			declarations[namespace] = source
		}
	}
	for _, node := range plan.Order {
		for namespace, source := range node.Definition.Plugins {
			canonical, err := source.CanonicalSource()
			if err != nil {
				return nil, fmt.Errorf("plugin %q: %w", namespace, err)
			}
			source.Source = canonical
			if previous, exists := declarations[namespace]; exists && !reflect.DeepEqual(previous, source) {
				return nil, fmt.Errorf("plugin %q has conflicting declarations in workflow invocation", namespace)
			}
			declarations[namespace] = source
		}
	}
	state.plugins = declarations
	return context.WithValue(ctx, workflowInvocationContextKey{}, state), nil
}

var _ engine.WorkflowInvoker = commandWorkflowInvoker{}
