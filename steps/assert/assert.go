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

type Runner struct {
	config  Config
	program *vm.Program
}

func Register(registry *step.Registry) error {
	return registry.RegisterDefinition("assert", step.Registration{Builder: New, Outputs: step.ClosedOutputs(), References: references})
}

func references(raw map[string]any) (step.References, error) {
	return step.References{Expressions: []step.ExpressionReference{step.ExpressionField(raw, "expr")}}, nil
}

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
	program, err := wukoexpr.Compile(config.Expr, expr.Env(step.ExpressionEnvironmentShape(nil)), expr.AllowUndefinedVariables(), expr.AsBool())
	if err != nil {
		return nil, fmt.Errorf("compiling expr: %w", err)
	}
	return &Runner{config: config, program: program}, nil
}

func (r *Runner) Run(ctx context.Context, request step.Request) (step.Result, error) {
	if err := ctx.Err(); err != nil {
		return step.Result{}, err
	}
	value, err := expr.Run(r.program, request.ExpressionEnvironment(nil))
	if err != nil {
		return step.Result{}, fmt.Errorf("evaluating expr: %w", err)
	}
	matched, ok := value.(bool)
	if !ok {
		return step.Result{}, fmt.Errorf("evaluating expr: expression returned %T, want bool", value)
	}
	if !matched {
		return step.Result{}, r.failure(request)
	}
	return step.Result{}, nil
}

// failure renders the message only now that the assertion has failed: the engine leaves it
// raw so a passing assertion never depends on it. A message that renders empty falls back
// to the expression, so the error still says what failed.
func (r *Runner) failure(request step.Request) error {
	message := r.config.Message
	if request.TemplateRenderer != nil {
		rendered, err := request.TemplateRenderer.Render(message)
		if err != nil {
			return fmt.Errorf("assertion failed: %s (rendering message: %w)", r.config.Expr, err)
		}
		message = rendered
	}
	if strings.TrimSpace(message) == "" {
		message = r.config.Expr
	}
	return fmt.Errorf("assertion failed: %s", message)
}
