package workflow

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"text/template/parse"

	"github.com/expr-lang/expr/ast"
	"github.com/expr-lang/expr/parser"
)

type dependencyOutputReference struct {
	alias  string
	output string
}

// DependencyResolver loads the effective discovered workflow with name.
type DependencyResolver func(context.Context, string) (*Definition, error)

// DependencyNode is one deduplicated workflow in a dependency plan.
type DependencyNode struct {
	Definition   *Definition
	Dependencies map[string]*DependencyNode
}

// PlaceholderDependencies returns typed zero values for a node's direct dependencies.
func (node *DependencyNode) PlaceholderDependencies() map[string]map[string]any {
	values := make(map[string]map[string]any, len(node.Dependencies))
	for alias, dependency := range node.Dependencies {
		outputs := make(map[string]any, len(dependency.Definition.Outputs))
		for name, declaration := range dependency.Definition.Outputs {
			outputs[name] = OutputPlaceholder(declaration.Type)
		}
		values[alias] = outputs
	}
	return values
}

// DependencyPlan contains workflows in deterministic prerequisite-first execution order.
type DependencyPlan struct {
	Root  *DependencyNode
	Order []*DependencyNode
}

// ResolveDependencyPlan resolves, validates, cycle-checks, and deduplicates a workflow graph.
func ResolveDependencyPlan(ctx context.Context, root *Definition, resolve DependencyResolver) (*DependencyPlan, error) {
	if root == nil {
		return nil, fmt.Errorf("root workflow is required")
	}
	if resolve == nil && len(root.DependsOn) > 0 {
		return nil, fmt.Errorf("dependency resolver is required")
	}

	plan := &DependencyPlan{}
	nodes := make(map[string]*DependencyNode)
	visiting := make(map[string]int)
	var chain []string

	var visit func(*Definition) (*DependencyNode, error)
	visit = func(definition *Definition) (*DependencyNode, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if definition == nil {
			return nil, fmt.Errorf("dependency resolver returned a nil workflow")
		}
		identity := dependencyIdentity(definition)
		if index, exists := visiting[identity]; exists {
			cycle := append(slices.Clone(chain[index:]), definition.Name)
			return nil, fmt.Errorf("workflow dependency cycle: %s", strings.Join(cycle, " -> "))
		}
		if node, exists := nodes[identity]; exists {
			return node, nil
		}

		node := &DependencyNode{Definition: definition, Dependencies: make(map[string]*DependencyNode)}
		visiting[identity] = len(chain)
		chain = append(chain, definition.Name)
		for _, alias := range slices.Sorted(maps.Keys(definition.DependsOn)) {
			name := definition.DependsOn[alias]
			dependency, err := resolve(ctx, name)
			if err != nil {
				return nil, fmt.Errorf("workflow %q dependency %q (%s): %w", definition.Name, alias, name, err)
			}
			child, err := visit(dependency)
			if err != nil {
				return nil, err
			}
			node.Dependencies[alias] = child
		}
		if err := validateDependencyReferences(node); err != nil {
			return nil, fmt.Errorf("workflow %q: %w", definition.Name, err)
		}
		chain = chain[:len(chain)-1]
		delete(visiting, identity)
		nodes[identity] = node
		plan.Order = append(plan.Order, node)
		return node, nil
	}

	var err error
	plan.Root, err = visit(root)
	if err != nil {
		return nil, err
	}
	return plan, nil
}

func validateDependencyReferences(node *DependencyNode) error {
	for _, reference := range definitionDependencyReferences(node.Definition) {
		dependency, exists := node.Dependencies[reference.alias]
		if !exists {
			return fmt.Errorf("dependency output references unknown alias %q", reference.alias)
		}
		if _, exists := dependency.Definition.Outputs[reference.output]; !exists {
			return fmt.Errorf("dependency %q does not declare output %q", reference.alias, reference.output)
		}
	}
	return nil
}

func definitionDependencyReferences(definition *Definition) []dependencyOutputReference {
	var references []dependencyOutputReference
	for _, name := range slices.Sorted(maps.Keys(definition.Outputs)) {
		references = append(references, expressionDependencyReferences(definition.Outputs[name].Value)...)
	}
	references = append(references, stepDependencyReferences(definition.Steps)...)
	return append(references, stepDependencyReferences(definition.Finally)...)
}

func stepDependencyReferences(steps []Step) []dependencyOutputReference {
	var references []dependencyOutputReference
	for _, workflowStep := range steps {
		references = append(references, expressionDependencyReferences(string(workflowStep.If))...)
		references = append(references, templateDependencyReferences(workflowStep.WorkingDirectory)...)
		if workflowStep.Worktree != nil {
			references = append(references, templateDependencyReferences(workflowStep.Worktree.Revision)...)
			references = append(references, templateDependencyReferences(workflowStep.Worktree.Path)...)
			if workflowStep.Worktree.Publish != nil {
				references = append(references, templateDependencyReferences(workflowStep.Worktree.Publish.Branch)...)
			}
		}
		references = append(references, templateDependencyReferences(workflowStep.Uses.URL)...)
		references = append(references, templateDependencyReferences(workflowStep.Uses.GitHub)...)
		references = append(references, templateDependencyReferences(workflowStep.Uses.Plugin)...)
		references = append(references, templateDependencyReferences(workflowStep.Uses.Token)...)
		references = append(references, templateDependencyReferences(workflowStep.Uses.Command)...)
		if workflowStep.Once != nil {
			references = append(references, templateDependencyReferences(workflowStep.Once.Key)...)
		}
		if workflowStep.Attempt != nil {
			for _, option := range []AttemptDuration{
				workflowStep.Attempt.Duration,
				workflowStep.Attempt.Timeout,
				workflowStep.Attempt.InitialDelay,
				workflowStep.Attempt.MaxDelay,
				workflowStep.Attempt.Interval,
				workflowStep.Attempt.MaxElapsedTime,
			} {
				references = append(references, expressionDependencyReferences(option.Expression)...)
			}
			references = append(references, expressionDependencyReferences(workflowStep.Attempt.MaxAttempts.Expression)...)
			references = append(references, expressionDependencyReferences(workflowStep.Attempt.BackoffMultiplier.Expression)...)
			references = append(references, expressionDependencyReferences(workflowStep.Attempt.Jitter.Expression)...)
			references = append(references, expressionDependencyReferences(string(workflowStep.Attempt.Until))...)
			references = append(references, expressionDependencyReferences(string(workflowStep.Attempt.When))...)
			references = append(references, templateDependencyReferences(workflowStep.Attempt.OperationID)...)
		}
		for _, argument := range workflowStep.Uses.Args {
			references = append(references, templateDependencyReferences(argument)...)
		}
		configuration := workflowStep.With
		if workflowStep.Type == "tui_form" {
			configuration = formTemplateConfiguration(configuration)
		}
		references = append(references, templateValueDependencyReferences(configuration)...)
		if workflowStep.Return != nil {
			for _, output := range workflowStep.Return.Outputs {
				references = append(references, expressionDependencyReferences(output)...)
			}
		}
		if workflowStep.Batch != nil {
			references = append(references, expressionDependencyReferences(workflowStep.Batch.Items)...)
			references = append(references, expressionDependencyReferences(workflowStep.Batch.Size.Expression)...)
			references = append(references, expressionDependencyReferences(workflowStep.Batch.Collect)...)
		}
		if workflowStep.Foreach != nil {
			references = append(references, expressionDependencyReferences(workflowStep.Foreach.Items)...)
			references = append(references, expressionDependencyReferences(workflowStep.Foreach.Collect)...)
		}
		if workflowStep.Matrix != nil {
			for _, axis := range workflowStep.Matrix.Axes {
				references = append(references, expressionDependencyReferences(axis.Expression)...)
			}
			references = append(references, expressionDependencyReferences(workflowStep.Matrix.Collect)...)
		}
		switch workflowStep.Type {
		case "assert", "set":
			if expression, ok := workflowStep.With["expr"].(string); ok {
				references = append(references, expressionDependencyReferences(expression)...)
			}
			if variables, ok := workflowStep.With["variables"].(map[string]any); ok {
				references = append(references, typedBindingDependencyReferences(variables)...)
			}
		case "tui_form":
			fields, _ := workflowStep.With["fields"].([]any)
			for _, item := range fields {
				field, _ := item.(map[string]any)
				if from, _ := field["from"].(string); from != "" {
					parts := strings.Split(from, ".")
					if len(parts) >= 3 && parts[0] == "dependencies" {
						references = append(references, dependencyOutputReference{alias: parts[1], output: parts[2]})
					}
				}
				if validation, ok := field["validation"].(map[string]any); ok {
					source, _ := validation["expr"].(string)
					references = append(references, expressionDependencyReferences(source)...)
				}
				for _, key := range []string{"value", "default"} {
					if binding, ok := field[key].(map[string]any); ok {
						if source, ok := binding["expr"].(string); ok {
							references = append(references, expressionDependencyReferences(source)...)
						}
					}
				}
			}
		case "tui_choice":
			for _, name := range []string{"label_expr", "value_expr", "description_expr", "disabled_expr", "reason_expr", "default_expr"} {
				if expression, ok := workflowStep.With[name].(string); ok {
					references = append(references, expressionDependencyReferences(expression)...)
				}
			}
		case "shell", "process":
			if argv, ok := workflowStep.With["argv"].(map[string]any); ok {
				if expression, ok := argv["expr"].(string); ok {
					references = append(references, expressionDependencyReferences(expression)...)
				}
			}
		case "lua":
			if args, ok := workflowStep.With["args"].(map[string]any); ok {
				references = append(references, typedBindingDependencyReferences(args)...)
			}
		case "template":
			if data, ok := workflowStep.With["data"].(map[string]any); ok {
				references = append(references, typedBindingDependencyReferences(data)...)
			}
		}
		if workflowStep.Action != nil {
			references = append(references, typedBindingDependencyReferences(workflowStep.With)...)
		}
		for _, child := range workflowStep.ChildSequences() {
			references = append(references, stepDependencyReferences(child.Steps)...)
		}
	}
	return references
}

// formTemplateConfiguration excludes literal Lua source from template dependency scanning.
func formTemplateConfiguration(raw map[string]any) map[string]any {
	fields, ok := raw["fields"].([]any)
	if !ok {
		return raw
	}
	result := maps.Clone(raw)
	items := make([]any, len(fields))
	for i, item := range fields {
		field, ok := item.(map[string]any)
		if !ok {
			items[i] = item
			continue
		}
		clone := maps.Clone(field)
		if validation, ok := field["validation"].(map[string]any); ok {
			rules := maps.Clone(validation)
			delete(rules, "lua")
			clone["validation"] = rules
		}
		items[i] = clone
	}
	result["fields"] = items
	return result
}

func typedBindingDependencyReferences(bindings map[string]any) []dependencyOutputReference {
	var references []dependencyOutputReference
	for _, value := range bindings {
		mapping, ok := value.(map[string]any)
		if !ok || len(mapping) != 1 {
			continue
		}
		if expression, ok := mapping["expr"].(string); ok {
			references = append(references, expressionDependencyReferences(expression)...)
		}
	}
	return references
}

func expressionDependencyReferences(expression string) []dependencyOutputReference {
	if strings.TrimSpace(expression) == "" {
		return nil
	}
	tree, err := parser.Parse(expression)
	if err != nil {
		return nil
	}
	var references []dependencyOutputReference
	ast.Walk(&tree.Node, expressionDependencyVisitor(func(node ast.Node) {
		path, ok := expressionMemberPath(node)
		if ok && len(path) >= 3 && path[0] == "dependencies" {
			references = append(references, dependencyOutputReference{alias: path[1], output: path[2]})
		}
	}))
	return references
}

type expressionDependencyVisitor func(ast.Node)

func (visit expressionDependencyVisitor) Visit(node *ast.Node) { visit(*node) }

func expressionMemberPath(node ast.Node) ([]string, bool) {
	switch typed := node.(type) {
	case *ast.IdentifierNode:
		return []string{typed.Value}, true
	case *ast.MemberNode:
		path, ok := expressionMemberPath(typed.Node)
		property, propertyOK := typed.Property.(*ast.StringNode)
		if !ok || !propertyOK {
			return nil, false
		}
		return append(path, property.Value), true
	case *ast.ChainNode:
		return expressionMemberPath(typed.Node)
	default:
		return nil, false
	}
}

func templateValueDependencyReferences(value any) []dependencyOutputReference {
	switch typed := value.(type) {
	case string:
		return templateDependencyReferences(typed)
	case map[string]any:
		var references []dependencyOutputReference
		for _, item := range typed {
			references = append(references, templateValueDependencyReferences(item)...)
		}
		return references
	case []any:
		var references []dependencyOutputReference
		for _, item := range typed {
			references = append(references, templateValueDependencyReferences(item)...)
		}
		return references
	default:
		return nil
	}
}

func templateDependencyReferences(value string) []dependencyOutputReference {
	if !strings.Contains(value, "{{") {
		return nil
	}
	// Only the field references matter here, so function names are not resolved: a template
	// piping a dependency output through any helper must still be found.
	tree := parse.New("dependency-reference")
	tree.Mode = parse.SkipFuncCheck
	trees := make(map[string]*parse.Tree)
	if _, err := tree.Parse(value, "", "", trees); err != nil {
		return nil
	}
	var references []dependencyOutputReference
	for _, name := range slices.Sorted(maps.Keys(trees)) {
		collectTemplateDependencyReferences(trees[name].Root, &references)
	}
	return references
}

func collectTemplateDependencyReferences(node parse.Node, references *[]dependencyOutputReference) {
	if node == nil {
		return
	}
	switch typed := node.(type) {
	case *parse.ListNode:
		if typed == nil {
			return
		}
		for _, child := range typed.Nodes {
			collectTemplateDependencyReferences(child, references)
		}
	case *parse.ActionNode:
		collectTemplateDependencyReferences(typed.Pipe, references)
	case *parse.PipeNode:
		if typed == nil {
			return
		}
		for _, command := range typed.Cmds {
			collectTemplateDependencyReferences(command, references)
		}
	case *parse.CommandNode:
		for _, argument := range typed.Args {
			collectTemplateDependencyReferences(argument, references)
		}
	case *parse.FieldNode:
		if len(typed.Ident) >= 3 && typed.Ident[0] == "dependencies" {
			*references = append(*references, dependencyOutputReference{alias: typed.Ident[1], output: typed.Ident[2]})
		}
	case *parse.ChainNode:
		collectTemplateDependencyReferences(typed.Node, references)
	case *parse.IfNode:
		collectTemplateDependencyReferences(typed.Pipe, references)
		collectTemplateDependencyReferences(typed.List, references)
		collectTemplateDependencyReferences(typed.ElseList, references)
	case *parse.RangeNode:
		collectTemplateDependencyReferences(typed.Pipe, references)
		collectTemplateDependencyReferences(typed.List, references)
		collectTemplateDependencyReferences(typed.ElseList, references)
	case *parse.WithNode:
		collectTemplateDependencyReferences(typed.Pipe, references)
		collectTemplateDependencyReferences(typed.List, references)
		collectTemplateDependencyReferences(typed.ElseList, references)
	case *parse.TemplateNode:
		collectTemplateDependencyReferences(typed.Pipe, references)
	}
}

func dependencyIdentity(definition *Definition) string {
	if definition.Path != "" {
		return definition.Path
	}
	return "name\x00" + definition.Name
}
