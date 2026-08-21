// Package assert implements boolean workflow assertions.
package assert

import (
	"context"
	"fmt"
	"strings"

	"github.com/expr-lang/expr"
	"github.com/expr-lang/expr/vm"
	"github.com/up2jj/wuko/step"
)

type Config struct {
	Expr    string `yaml:"expr"`
	Message string `yaml:"message"`
}

type expressionEnvironment struct {
	Inputs   map[string]any    `expr:"inputs"`
	Vars     map[string]any    `expr:"vars"`
	Env      map[string]string `expr:"env"`
	Steps    map[string]any    `expr:"steps"`
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
	program, err := expr.Compile(config.Expr, expr.Env(expressionEnvironment{}), expr.AsBool())
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
		Inputs:  request.Inputs,
		Vars:    request.Vars,
		Env:     request.Env,
		Steps:   request.Steps,
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
