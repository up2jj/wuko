// Package runworkflow implements blocking invocation of discovered local workflows.
package runworkflow

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"slices"
	"strings"

	"github.com/expr-lang/expr"
	"github.com/expr-lang/expr/vm"
	wukoexpr "github.com/up2jj/wuko/expression"
	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/workflow"
)

type Config struct {
	Workflow string         `yaml:"workflow"`
	Target   string         `yaml:"target,omitempty"`
	Vars     map[string]any `yaml:"vars,omitempty"`
}

type Runner struct {
	config Config
	vars   map[string]*value
}

type value struct {
	literal    any
	expression string
	program    *vm.Program
	object     map[string]*value
	items      []*value
	kind       valueKind
}

type valueKind uint8

const (
	literalValue valueKind = iota
	expressionValue
	objectValue
	arrayValue
)

func Register(registry *step.Registry) error {
	return registry.RegisterDefinition("run_workflow", step.Registration{Builder: New, Outputs: step.OpenObject()})
}

func New(raw map[string]any) (step.Runner, error) {
	var config Config
	if err := step.DecodeConfig(raw, &config); err != nil {
		return nil, err
	}
	config.Workflow = strings.TrimSpace(config.Workflow)
	config.Target = strings.TrimSpace(config.Target)
	if !workflow.ValidWorkflowSelector(config.Workflow) {
		return nil, fmt.Errorf("workflow must be a static discovered workflow name")
	}
	if templated(config.Workflow) || templated(config.Target) {
		return nil, fmt.Errorf("workflow and target must be static")
	}

	parsed := make(map[string]*value, len(config.Vars))
	for _, name := range slices.Sorted(maps.Keys(config.Vars)) {
		rawValue := config.Vars[name]
		if strings.TrimSpace(name) == "" {
			return nil, fmt.Errorf("vars contains an empty name")
		}
		item, err := parseValue("vars."+name, rawValue)
		if err != nil {
			return nil, err
		}
		parsed[name] = item
	}
	return &Runner{config: config, vars: parsed}, nil
}

func (runner *Runner) Validate(ctx context.Context, request step.Request) error {
	if request.Workflows == nil {
		return fmt.Errorf("workflow invocation is unavailable")
	}
	vars, deferred := runner.staticVars()
	return request.Workflows.ValidateWorkflow(ctx, step.WorkflowCall{
		Workflow:     runner.config.Workflow,
		Target:       runner.config.Target,
		Vars:         vars,
		DeferredVars: deferred,
	})
}

// staticVars splits the configured variables into the values already known before the run and
// the names whose values an expression will produce. Validating without them would check the
// child against its own defaults instead of what the call supplies, and would report a variable
// only the caller declares as unknown.
func (runner *Runner) staticVars() (map[string]any, []string) {
	values := make(map[string]any, len(runner.vars))
	var deferred []string
	for _, name := range slices.Sorted(maps.Keys(runner.vars)) {
		resolved, known := runner.vars[name].static()
		if !known {
			deferred = append(deferred, name)
			continue
		}
		values[name] = resolved
	}
	return values, deferred
}

// static returns the configured value when no part of it comes from an expression.
func (configured *value) static() (any, bool) {
	switch configured.kind {
	case expressionValue:
		return nil, false
	case objectValue:
		result := make(map[string]any, len(configured.object))
		for name, child := range configured.object {
			resolved, known := child.static()
			if !known {
				return nil, false
			}
			result[name] = resolved
		}
		return result, true
	case arrayValue:
		result := make([]any, len(configured.items))
		for index, child := range configured.items {
			resolved, known := child.static()
			if !known {
				return nil, false
			}
			result[index] = resolved
		}
		return result, true
	default:
		return configured.literal, true
	}
}

func (runner *Runner) Run(ctx context.Context, request step.Request) (step.Result, error) {
	if request.Workflows == nil {
		return step.Result{}, fmt.Errorf("workflow invocation is unavailable")
	}
	if err := ctx.Err(); err != nil {
		return step.Result{}, err
	}
	vars := make(map[string]any, len(runner.vars))
	for _, name := range slices.Sorted(maps.Keys(runner.vars)) {
		configured := runner.vars[name]
		resolved, err := configured.resolve(request)
		if err != nil {
			return step.Result{}, fmt.Errorf("resolving var %q: %w", name, err)
		}
		vars[name] = resolved
	}
	if err := validateJSON(vars); err != nil {
		return step.Result{}, fmt.Errorf("vars are not JSON-compatible: %w", err)
	}
	outputs, err := request.Workflows.RunWorkflow(ctx, step.WorkflowCall{
		Workflow: runner.config.Workflow,
		Target:   runner.config.Target,
		Vars:     vars,
	})
	if err != nil {
		return step.Result{}, invocationError(runner.config, err)
	}
	return step.Result{Outputs: outputs}, nil
}

func invocationError(config Config, err error) error {
	if config.Target == "" {
		return fmt.Errorf("running workflow %q: %w", config.Workflow, err)
	}
	return fmt.Errorf("running workflow %q target %q: %w", config.Workflow, config.Target, err)
}

func parseValue(label string, raw any) (*value, error) {
	switch raw := raw.(type) {
	case map[string]any:
		if len(raw) == 1 {
			if source, ok := raw["expr"]; ok {
				text, ok := source.(string)
				if !ok || strings.TrimSpace(text) == "" {
					return nil, fmt.Errorf("%s.expr must be a non-empty expression string", label)
				}
				configured := &value{kind: expressionValue, expression: text}
				if !templated(text) {
					program, err := compileExpression(text)
					if err != nil {
						return nil, fmt.Errorf("compiling %s.expr: %w", label, err)
					}
					configured.program = program
				}
				return configured, nil
			}
			if literal, ok := raw["literal"]; ok {
				if err := validateJSON(literal); err != nil {
					return nil, fmt.Errorf("%s.literal is not JSON-compatible: %w", label, err)
				}
				return &value{kind: literalValue, literal: literal}, nil
			}
		}
		object := make(map[string]*value, len(raw))
		for _, name := range slices.Sorted(maps.Keys(raw)) {
			child := raw[name]
			parsed, err := parseValue(label+"."+name, child)
			if err != nil {
				return nil, err
			}
			object[name] = parsed
		}
		return &value{kind: objectValue, object: object}, nil
	case []any:
		items := make([]*value, len(raw))
		for index, child := range raw {
			parsed, err := parseValue(fmt.Sprintf("%s[%d]", label, index), child)
			if err != nil {
				return nil, err
			}
			items[index] = parsed
		}
		return &value{kind: arrayValue, items: items}, nil
	default:
		if err := validateJSON(raw); err != nil {
			return nil, fmt.Errorf("%s is not JSON-compatible: %w", label, err)
		}
		return &value{kind: literalValue, literal: raw}, nil
	}
}

func (configured *value) resolve(request step.Request) (any, error) {
	switch configured.kind {
	case expressionValue:
		if configured.program == nil {
			return nil, fmt.Errorf("expression contains an unresolved template")
		}
		result, err := expr.Run(configured.program, request.ExpressionEnvironment(nil))
		if err != nil {
			return nil, fmt.Errorf("evaluating expr: %w", err)
		}
		return result, nil
	case objectValue:
		result := make(map[string]any, len(configured.object))
		for _, name := range slices.Sorted(maps.Keys(configured.object)) {
			child := configured.object[name]
			resolved, err := child.resolve(request)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", name, err)
			}
			result[name] = resolved
		}
		return result, nil
	case arrayValue:
		result := make([]any, len(configured.items))
		for index, child := range configured.items {
			resolved, err := child.resolve(request)
			if err != nil {
				return nil, fmt.Errorf("[%d]: %w", index, err)
			}
			result[index] = resolved
		}
		return result, nil
	default:
		return configured.literal, nil
	}
}

func compileExpression(source string) (*vm.Program, error) {
	return wukoexpr.Compile(source, expr.Env(step.ExpressionEnvironmentShape(nil)), expr.AllowUndefinedVariables())
}

func templated(value string) bool {
	return strings.Contains(value, "{{") || strings.Contains(value, "}}")
}

func validateJSON(value any) error {
	if number, ok := value.(float64); ok && (math.IsNaN(number) || math.IsInf(number, 0)) {
		return fmt.Errorf("non-finite number")
	}
	_, err := json.Marshal(value)
	return err
}

// WorkflowAware asks the engine to bind the invoking host to this runner's requests.
func (runner *Runner) WorkflowAware() {}

var (
	_ step.Validator     = (*Runner)(nil)
	_ step.WorkflowAware = (*Runner)(nil)
)
