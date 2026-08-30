// Package assert implements boolean workflow assertions.
package assert

import (
	"context"
	"fmt"
	"strings"

	"github.com/expr-lang/expr"
	"github.com/expr-lang/expr/vm"
	wukoexpr "github.com/up2jj/wuko/expression"
	"github.com/up2jj/wuko/step"
)

type Config struct {
	Expr    string `yaml:"expr"`
	Message string `yaml:"message"`
}

type expressionEnvironment struct {
	Inputs       map[string]any            `expr:"inputs"`
	Vars         map[string]any            `expr:"vars"`
	Env          map[string]string         `expr:"env"`
	Steps        map[string]any            `expr:"steps"`
	Dependencies map[string]map[string]any `expr:"dependencies"`
	Batch        map[string]any            `expr:"batch"`
	Foreach      map[string]any            `expr:"foreach"`
	Matrix       map[string]any            `expr:"matrix"`
	Observe      map[string]any            `expr:"observe"`
	Finally      map[string]any            `expr:"finally"`
	Error        map[string]any            `expr:"error"`
	Workflow     step.WorkflowValue        `expr:"workflow"`
	Run          runValue                  `expr:"run"`
}

type runValue struct {
	Dir string `expr:"dir"`
}

type Runner struct {
	config  Config
	program *vm.Program
}

func Register(registry *step.Registry) error { return registry.Register("assert", New) }

func New(raw map[string]any) (step.Runner, error) {
	var config Config
	if err := step.DecodeConfig(raw, &config); err != nil {
		return nil, err
	}
	if strings.TrimSpace(config.Expr) == "" {
		return nil, fmt.Errorf("expr is required")
	}
	if strings.TrimSpace(config.Message) == "" {
		return nil, fmt.Errorf("message is required")
	}
	program, err := wukoexpr.Compile(config.Expr, expr.Env(expressionEnvironment{}), expr.AsBool())
	if err != nil {
		return nil, fmt.Errorf("compiling expr: %w", err)
	}
	return &Runner{config: config, program: program}, nil
}

func (r *Runner) Run(ctx context.Context, request step.Request) (step.Result, error) {
	if err := ctx.Err(); err != nil {
		return step.Result{}, err
	}
	value, err := expr.Run(r.program, expressionEnvironment{
		Inputs:       request.Inputs,
		Vars:         request.Vars,
		Env:          request.Env,
		Steps:        request.Steps,
		Dependencies: request.Dependencies,
		Batch:        bindingRoot(request.Bindings, "batch"),
		Foreach:      bindingRoot(request.Bindings, "foreach"),
		Matrix:       bindingRoot(request.Bindings, "matrix"),
		Observe:      bindingRoot(request.Bindings, "observe"),
		Finally:      bindingRoot(request.Bindings, "finally"),
		Error:        bindingRoot(request.Bindings, "error"),
		Workflow:     request.WorkflowValue(),
		Run:          runValue{Dir: request.RunDir},
	})
	if err != nil {
		return step.Result{}, fmt.Errorf("evaluating expr: %w", err)
	}
	matched, ok := value.(bool)
	if !ok {
		return step.Result{}, fmt.Errorf("evaluating expr: expression returned %T, want bool", value)
	}
	if !matched {
		return step.Result{}, fmt.Errorf("assertion failed: %s", r.config.Message)
	}
	return step.Result{}, nil
}

func bindingRoot(bindings map[string]any, name string) map[string]any {
	value, _ := bindings[name].(map[string]any)
	if value == nil {
		return map[string]any{}
	}
	return value
}
