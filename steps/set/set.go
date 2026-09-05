// Package set implements typed workflow variable assignment.
package set

import (
	"context"
	"encoding/json"
	"fmt"
	"math"

	"github.com/expr-lang/expr"
	"github.com/expr-lang/expr/vm"
	wukoexpr "github.com/up2jj/wuko/expression"
	"github.com/up2jj/wuko/step"
)

type Config struct {
	Variable string `yaml:"variable"`
	Value    any    `yaml:"value,omitempty"`
	Expr     string `yaml:"expr,omitempty"`
}

type Runner struct {
	config   Config
	hasValue bool
	program  *vm.Program
}

func Register(registry *step.Registry) error { return registry.Register("set", New) }

func New(raw map[string]any) (step.Runner, error) {
	var config Config
	if err := step.DecodeConfig(raw, &config); err != nil {
		return nil, err
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
		if err := validateJSON(config.Value); err != nil {
			return nil, fmt.Errorf("value is not JSON-compatible: %w", err)
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

func (r *Runner) Run(ctx context.Context, request step.Request) (step.Result, error) {
	if err := ctx.Err(); err != nil {
		return step.Result{}, err
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
