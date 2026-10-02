package cmd

import (
	"context"
	"fmt"
	"reflect"

	"github.com/up2jj/wuko/engine"
	"github.com/up2jj/wuko/validation"
	"github.com/up2jj/wuko/workflow"
)

func resolveDependencyPlan(ctx context.Context, root *workflow.Definition, loader *workflow.Loader, options workflow.LoadOptions, cwd, homeDir, configDir string) (*workflow.DependencyPlan, error) {
	return resolveDependencyPlanWith(ctx, root, loader, options, func() ([]workflow.Source, error) {
		return workflow.Discover(cwd, homeDir, configDir)
	})
}

// resolveDependencyPlanWith takes discovery as a callback so a caller that repeats the same
// resolution - a child-workflow invocation inside a loop, say - can serve it from a cache
// instead of re-reading and re-parsing every discoverable workflow file.
func resolveDependencyPlanWith(ctx context.Context, root *workflow.Definition, loader *workflow.Loader, options workflow.LoadOptions, discover func() ([]workflow.Source, error)) (*workflow.DependencyPlan, error) {
	if len(root.DependsOn) == 0 {
		return workflow.ResolveDependencyPlan(ctx, root, nil)
	}
	sources, err := discover()
	if err != nil {
		return nil, err
	}
	byName := make(map[string]workflow.Source, len(sources))
	for _, source := range sources {
		byName[source.Name] = source
	}
	loaded := make(map[string]*workflow.Definition)
	return workflow.ResolveDependencyPlan(ctx, root, func(ctx context.Context, name string) (*workflow.Definition, error) {
		if !workflow.ValidWorkflowName(name) {
			return nil, fmt.Errorf("invalid workflow name %q", name)
		}
		source, exists := byName[name]
		if !exists {
			return nil, fmt.Errorf("workflow %q not found", name)
		}
		if source.Target != "" {
			return nil, fmt.Errorf("workflow %q declares targets and cannot be used as a dependency", name)
		}
		if definition := loaded[source.Path]; definition != nil {
			return definition, nil
		}
		dependencyOptions := options
		dependencyOptions.Target = ""
		// Every workflow in the plan shares the root's provider session, so authentication
		// happens once, the resolve cache is shared, and one redaction set covers the run.
		dependencyOptions.SecretSession = root.SecretSession()
		definition, err := loader.Load(ctx, source.Path, dependencyOptions)
		if err != nil {
			return nil, err
		}
		loaded[source.Path] = definition
		return definition, nil
	})
}

func dependencyValues(node *workflow.DependencyNode, states map[*workflow.DependencyNode]*engine.State, placeholders bool) map[string]map[string]any {
	if placeholders {
		return node.PlaceholderDependencies()
	}
	values := make(map[string]map[string]any, len(node.Dependencies))
	for alias, dependency := range node.Dependencies {
		state := states[dependency]
		if state == nil {
			values[alias] = map[string]any{}
			continue
		}
		outputs := make(map[string]any, len(dependency.Definition.Outputs))
		for name := range dependency.Definition.Outputs {
			outputs[name] = workflow.Clone(state.Outputs[name])
		}
		values[alias] = outputs
	}
	return values
}

// preflightDependencyPlan is the single semantic-validation path used by every
// command context after loading and dependency preparation.
func preflightDependencyPlan(ctx context.Context, plan *workflow.DependencyPlan, engineFor func() *engine.Engine, optionsFor func(*workflow.Definition, map[string]map[string]any) engine.Options) error {
	if err := validatePluginDeclarations(plan); err != nil {
		return err
	}
	var issues validation.Collector
	for _, node := range plan.Order {
		if err := ctx.Err(); err != nil {
			return err
		}
		options := optionsFor(node.Definition, node.PlaceholderDependencies())
		if err := engineFor().Validate(ctx, node.Definition, options); err != nil {
			if !issues.AddError(fmt.Errorf("workflow %q: %w", node.Definition.Name, err)) {
				return fmt.Errorf("workflow %q: %w", node.Definition.Name, err)
			}
		}
	}
	return issues.Err()
}

func preflightDefinition(ctx context.Context, definition *workflow.Definition, engineFor *engine.Engine, options engine.Options) error {
	return engineFor.Validate(ctx, definition, options)
}

func executeDependencyPlan(ctx context.Context, plan *workflow.DependencyPlan, engineFor func() *engine.Engine, optionsFor func(*workflow.Definition, map[string]map[string]any) engine.Options) (*engine.State, error) {
	return executePlan(ctx, plan, engineFor, optionsFor, true)
}

// executeInvokedDependencyPlan runs a plan for a caller that already names the root workflow in
// its own error - a run_workflow step does - so the root failure is returned unlabelled instead
// of repeating the name a third time.
func executeInvokedDependencyPlan(ctx context.Context, plan *workflow.DependencyPlan, engineFor func() *engine.Engine, optionsFor func(*workflow.Definition, map[string]map[string]any) engine.Options) (*engine.State, error) {
	return executePlan(ctx, plan, engineFor, optionsFor, false)
}

func executePlan(ctx context.Context, plan *workflow.DependencyPlan, engineFor func() *engine.Engine, optionsFor func(*workflow.Definition, map[string]map[string]any) engine.Options, labelRoot bool) (*engine.State, error) {
	if err := validatePluginDeclarations(plan); err != nil {
		return nil, err
	}
	states := make(map[*workflow.DependencyNode]*engine.State, len(plan.Order))
	for _, node := range plan.Order {
		options := optionsFor(node.Definition, nil)
		options.Dependencies = dependencyValues(node, states, options.DryRun)
		state, err := engineFor().RunValidated(ctx, node.Definition, options)
		if err != nil {
			if node == plan.Root && !labelRoot {
				return nil, err
			}
			return nil, fmt.Errorf("workflow %q: %w", node.Definition.Name, err)
		}
		states[node] = state
	}
	return states[plan.Root], nil
}

func validatePluginDeclarations(plan *workflow.DependencyPlan) error {
	declarations := make(map[string]workflow.PluginSource)
	for _, node := range plan.Order {
		for namespace, source := range node.Definition.Plugins {
			canonical, err := source.CanonicalSource()
			if err != nil {
				return fmt.Errorf("plugin %q: %w", namespace, err)
			}
			source.Source = canonical
			if previous, exists := declarations[namespace]; exists && !reflect.DeepEqual(previous, source) {
				return fmt.Errorf("plugin %q has conflicting declarations in dependency plan", namespace)
			}
			declarations[namespace] = source
		}
	}
	return nil
}
