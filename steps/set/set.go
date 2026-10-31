// Package set implements typed workflow variable assignment.
package set

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
)

type Config struct {
	Variable  string         `yaml:"variable"`
	Variables map[string]any `yaml:"variables,omitempty"`
	Value     any            `yaml:"value,omitempty"`
	Expr      string         `yaml:"expr,omitempty"`
}

type assignment struct {
	name    string
	value   any
	program *vm.Program
}

type Runner struct {
	config      Config
	hasValue    bool
	program     *vm.Program
	assignments []assignment
}

func Register(registry *step.Registry) error {
	return registry.RegisterDefinition("set", step.Registration{
		Builder: New, Config: Config{}, Outputs: step.ClosedObject(map[string]step.OutputSchema{"value": step.OpenObject()}), References: references,
	})
}

func references(raw map[string]any) (step.References, error) {
	references := step.References{Expressions: []step.ExpressionReference{step.ExpressionField(raw, "expr")}}
	variables, _ := raw["variables"].(map[string]any)
	for _, name := range slices.Sorted(maps.Keys(variables)) {
		mapping, ok := variables[name].(map[string]any)
		if !ok || len(mapping) != 1 {
			continue
		}
		source, ok := mapping["expr"].(string)
		if ok {
			references.Expressions = append(references.Expressions, step.ExpressionReference{
				Label: "variables." + name + ".expr", Source: source,
			})
		}
	}
	return references, nil
}

func New(raw map[string]any) (step.Runner, error) {
	var config Config
	if err := step.DecodeConfig(raw, &config); err != nil {
		return nil, err
	}
	_, hasVariable := raw["variable"]
	_, hasVariables := raw["variables"]
	if hasVariable == hasVariables {
		return nil, fmt.Errorf("exactly one of variable or variables is required")
	}
	if hasVariables {
		return newBatchRunner(config)
	}
	if config.Variable == "" {
		return nil, fmt.Errorf("variable is required")
	}
	_, hasValue := raw["value"]
	_, hasExpr := raw["expr"]
	if hasValue == hasExpr {
		return nil, fmt.Errorf("exactly one of value or expr is required")
	}
	if hasValue {
		if step.ConfigKnown(raw, "value") {
			if err := validateJSON(config.Value); err != nil {
				return nil, fmt.Errorf("value is not JSON-compatible: %w", err)
			}
		}
		return &Runner{config: config, hasValue: true}, nil
	}
	if config.Expr == "" {
		return nil, fmt.Errorf("expr must not be empty")
	}
	program, err := wukoexpr.Compile(config.Expr, expr.Env(step.ExpressionEnvironmentShape(nil)), expr.AllowUndefinedVariables())
	if err != nil {
		return nil, fmt.Errorf("compiling expr: %w", err)
	}
	return &Runner{config: config, program: program}, nil
}

func newBatchRunner(config Config) (step.Runner, error) {
	if len(config.Variables) == 0 {
		return nil, fmt.Errorf("variables must not be empty")
	}
	assignments := make([]assignment, 0, len(config.Variables))
	for _, name := range slices.Sorted(maps.Keys(config.Variables)) {
		if strings.TrimSpace(name) == "" {
			return nil, fmt.Errorf("variable names must not be empty")
		}
		configured, err := parseAssignment(name, config.Variables[name])
		if err != nil {
			return nil, err
		}
		assignments = append(assignments, configured)
	}
	return &Runner{config: config, assignments: assignments}, nil
}

func parseAssignment(name string, raw any) (assignment, error) {
	if mapping, ok := raw.(map[string]any); ok && len(mapping) == 1 {
		if source, exists := mapping["expr"]; exists {
			text, ok := source.(string)
			if !ok || strings.TrimSpace(text) == "" {
				return assignment{}, fmt.Errorf("variables.%s.expr must be a non-empty string", name)
			}
			program, err := wukoexpr.Compile(text, expr.Env(step.ExpressionEnvironmentShape(nil)), expr.AllowUndefinedVariables())
			if err != nil {
				return assignment{}, fmt.Errorf("compiling variables.%s.expr: %w", name, err)
			}
			return assignment{name: name, program: program}, nil
		}
		if literal, exists := mapping["literal"]; exists {
			if err := validateJSON(literal); err != nil {
				return assignment{}, fmt.Errorf("variables.%s.literal is not JSON-compatible: %w", name, err)
			}
			return assignment{name: name, value: literal}, nil
		}
		if value, exists := mapping["value"]; exists {
			if err := validateJSON(value); err != nil {
				return assignment{}, fmt.Errorf("variables.%s.value is not JSON-compatible: %w", name, err)
			}
			return assignment{name: name, value: value}, nil
		}
	}
	if err := validateJSON(raw); err != nil {
		return assignment{}, fmt.Errorf("variables.%s is not JSON-compatible: %w", name, err)
	}
	return assignment{name: name, value: raw}, nil
}

func (r *Runner) Run(ctx context.Context, request step.Request) (step.Result, error) {
	if err := ctx.Err(); err != nil {
		return step.Result{}, err
	}
	if len(r.assignments) > 0 {
		values := make(map[string]any, len(r.assignments))
		environment := request.ExpressionEnvironment(nil)
		for _, assignment := range r.assignments {
			value := assignment.value
			if assignment.program != nil {
				var err error
				value, err = expr.Run(assignment.program, environment)
				if err != nil {
					return step.Result{}, fmt.Errorf("evaluating variables.%s.expr: %w", assignment.name, err)
				}
			}
			if err := validateJSON(value); err != nil {
				return step.Result{}, fmt.Errorf("variables.%s result is not JSON-compatible: %w", assignment.name, err)
			}
			values[assignment.name] = value
		}
		return step.Result{Outputs: map[string]any{"value": values}, Variables: values}, nil
	}

	value := r.config.Value
	if !r.hasValue {
		var err error
		value, err = expr.Run(r.program, request.ExpressionEnvironment(nil))
		if err != nil {
			return step.Result{}, fmt.Errorf("evaluating expr: %w", err)
		}
	}
	if err := validateJSON(value); err != nil {
		return step.Result{}, fmt.Errorf("result is not JSON-compatible: %w", err)
	}
	return step.Result{
		Outputs:   map[string]any{"value": value},
		Variables: map[string]any{r.config.Variable: value},
	}, nil
}

func validateJSON(value any) error {
	if number, ok := value.(float64); ok && (math.IsNaN(number) || math.IsInf(number, 0)) {
		return fmt.Errorf("non-finite number")
	}
	_, err := json.Marshal(value)
	return err
}
