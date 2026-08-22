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

type expressionEnvironment struct {
	Inputs   map[string]any    `expr:"inputs"`
	Vars     map[string]any    `expr:"vars"`
	Env      map[string]string `expr:"env"`
	Steps    map[string]any    `expr:"steps"`
	Batch    map[string]any    `expr:"batch"`
	Foreach  map[string]any    `expr:"foreach"`
	Matrix   map[string]any    `expr:"matrix"`
	Finally  map[string]any    `expr:"finally"`
	Workflow workflowValue     `expr:"workflow"`
	Run      runValue          `expr:"run"`
}

type workflowValue struct {
	Name string `expr:"name"`
	Dir  string `expr:"dir"`
}

type runValue struct {
	Dir string `expr:"dir"`
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
	program, err := wukoexpr.Compile(config.Expr, expr.Env(expressionEnvironment{}))
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
		value, err = expr.Run(r.program, expressionEnvironment{
			Inputs:  request.Inputs,
			Vars:    request.Vars,
			Env:     request.Env,
			Steps:   request.Steps,
			Batch:   bindingRoot(request.Bindings, "batch"),
			Foreach: bindingRoot(request.Bindings, "foreach"),
			Matrix:  bindingRoot(request.Bindings, "matrix"),
			Finally: bindingRoot(request.Bindings, "finally"),
			Workflow: workflowValue{
				Name: request.WorkflowName,
				Dir:  request.WorkflowDir,
			},
			Run: runValue{Dir: request.RunDir},
		})
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

func bindingRoot(bindings map[string]any, name string) map[string]any {
	value, _ := bindings[name].(map[string]any)
	if value == nil {
		return map[string]any{}
	}
	return value
}

func validateJSON(value any) error {
	if number, ok := value.(float64); ok && (math.IsNaN(number) || math.IsInf(number, 0)) {
		return fmt.Errorf("non-finite number")
	}
	_, err := json.Marshal(value)
	return err
}
