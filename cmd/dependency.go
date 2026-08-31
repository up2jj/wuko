package cmd

import (
	"context"
	"fmt"

	"github.com/up2jj/wuko/engine"
	"github.com/up2jj/wuko/workflow"
)

func resolveDependencyPlan(ctx context.Context, root *workflow.Definition, loader *workflow.Loader, options workflow.LoadOptions, cwd, homeDir, configDir string) (*workflow.DependencyPlan, error) {
	if len(root.DependsOn) == 0 {
		return workflow.ResolveDependencyPlan(ctx, root, nil)
	}
	sources, err := workflow.Discover(cwd, homeDir, configDir)
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

func validateDependencyPlan(ctx context.Context, plan *workflow.DependencyPlan, engineFor func() *engine.Engine, optionsFor func(*workflow.Definition, map[string]map[string]any) engine.Options) error {
	for _, node := range plan.Order {
		options := optionsFor(node.Definition, node.PlaceholderDependencies())
		if err := engineFor().Validate(ctx, node.Definition, options); err != nil {
			return fmt.Errorf("workflow %q: %w", node.Definition.Name, err)
		}
	}
	return nil
}

func executeDependencyPlan(ctx context.Context, plan *workflow.DependencyPlan, engineFor func() *engine.Engine, optionsFor func(*workflow.Definition, map[string]map[string]any) engine.Options) (*engine.State, error) {
	states := make(map[*workflow.DependencyNode]*engine.State, len(plan.Order))
	for _, node := range plan.Order {
		options := optionsFor(node.Definition, nil)
		options.Dependencies = dependencyValues(node, states, options.DryRun)
		state, err := engineFor().Run(ctx, node.Definition, options)
		if err != nil {
			return nil, fmt.Errorf("workflow %q: %w", node.Definition.Name, err)
		}
		states[node] = state
	}
	return states[plan.Root], nil
}
