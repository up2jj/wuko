// Package assert implements boolean workflow assertions.
package assert

import (
	"context"
	"fmt"
	"reflect"
	"strings"

	"github.com/expr-lang/expr"
	"github.com/expr-lang/expr/vm"
	wukoexpr "github.com/up2jj/wuko/expression"
	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/useroutput"
)

type Config struct {
	Expr     string `yaml:"expr"`
	Message  any    `yaml:"message"`
	Plain    bool   `yaml:"plain,omitempty"`
	ExitCode *int   `yaml:"exit_code,omitempty"`
}

type Runner struct {
	config         Config
	program        *vm.Program
	messageExpr    string
	messageProgram *vm.Program
	literalMessage bool
}

func Register(registry *step.Registry) error {
	return registry.RegisterDefinition("assert", step.Registration{Builder: New, Config: Config{}, Outputs: step.ClosedOutputs(), References: references})
}

func references(raw map[string]any) (step.References, error) {
	references := step.References{Expressions: []step.ExpressionReference{step.ExpressionField(raw, "expr")}}
	if message, ok := raw["message"].(map[string]any); ok && len(message) == 1 {
		reference := step.ExpressionField(message, "expr")
		reference.Label = "message expr"
		references.Expressions = append(references.Expressions, reference)
	}
	return references, nil
}

func New(raw map[string]any) (step.Runner, error) {
	var config Config
	if err := step.DecodeConfig(raw, &config); err != nil {
		return nil, err
	}
	if strings.TrimSpace(config.Expr) == "" {
		return nil, fmt.Errorf("expr is required")
	}
	if value, exists := raw["exit_code"]; exists && value != nil && step.ConfigKnown(raw, "exit_code") {
		if err := step.CheckBindingType(Config{}, []string{"exit_code"}, value); err != nil {
			return nil, err
		}
	}
	if step.ConfigKnown(raw, "plain", "exit_code") && config.ExitCode != nil && (!config.Plain || *config.ExitCode < 1 || *config.ExitCode > 255) {
		return nil, fmt.Errorf("exit_code requires plain: true and must be between 1 and 255")
	}
	var messageExpr string
	var messageProgram *vm.Program
	var literalMessage bool
	switch message := config.Message.(type) {
	case string:
		if strings.TrimSpace(message) == "" {
			return nil, fmt.Errorf("message is required")
		}
	case map[string]any:
		if literal, ok := message["literal"].(string); ok && len(message) == 1 {
			config.Message = literal
			literalMessage = true
			break
		}
		var ok bool
		messageExpr, ok = message["expr"].(string)
		if !ok || len(message) != 1 || strings.TrimSpace(messageExpr) == "" {
			return nil, fmt.Errorf("message must be a string or a non-empty expr binding")
		}
		if !strings.Contains(messageExpr, "{{") {
			var err error
			messageProgram, err = wukoexpr.Compile(messageExpr, expr.Env(step.ExpressionEnvironmentShape(nil)), expr.AllowUndefinedVariables())
			if err != nil {
				return nil, fmt.Errorf("compiling message expr: %w", err)
			}
			if resultType := messageProgram.Node().Type(); resultType != nil && resultType.Kind() != reflect.Interface && resultType.Kind() != reflect.String {
				return nil, fmt.Errorf("message expr must return a string")
			}
		}
	default:
		// Preserve the existing YAML decoder's scalar-to-string behavior for plain
		// configuration. Expression results take the strict string path in failure.
		var plain struct {
			Expr     string `yaml:"expr"`
			Message  string `yaml:"message"`
			Plain    bool   `yaml:"plain,omitempty"`
			ExitCode *int   `yaml:"exit_code,omitempty"`
		}
		if err := step.DecodeConfig(raw, &plain); err != nil {
			return nil, err
		}
		if strings.TrimSpace(plain.Message) == "" {
			return nil, fmt.Errorf("message is required")
		}
		config.Message = plain.Message
	}
	program, err := wukoexpr.Compile(config.Expr, expr.Env(step.ExpressionEnvironmentShape(nil)), expr.AllowUndefinedVariables(), expr.AsBool())
	if err != nil {
		return nil, fmt.Errorf("compiling expr: %w", err)
	}
	return &Runner{config: config, program: program, messageExpr: messageExpr, messageProgram: messageProgram, literalMessage: literalMessage}, nil
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
		failure := r.failure(request)
		if plain, ok := failure.(*useroutput.Failure); ok {
			plain.Reported = request.PrintMessage(useroutput.Message{Text: plain.Message, Level: useroutput.Error, Plain: true})
		}
		return step.Result{}, failure
	}
	return step.Result{}, nil
}

// failure renders the message only now that the assertion has failed: the engine leaves it
// raw so a passing assertion never depends on it. A message that renders empty falls back
// to the expression, so the error still says what failed.
func (r *Runner) failure(request step.Request) error {
	message, _ := r.config.Message.(string)
	if r.messageExpr != "" {
		program := r.messageProgram
		if program == nil {
			source := r.messageExpr
			if request.TemplateRenderer != nil {
				var err error
				source, err = request.TemplateRenderer.Render(source)
				if err != nil {
					return fmt.Errorf("assertion failed: %s (rendering message expr: %w)", r.config.Expr, err)
				}
			}
			var err error
			program, err = wukoexpr.Compile(source, expr.Env(step.ExpressionEnvironmentShape(nil)), expr.AllowUndefinedVariables())
			if err != nil {
				return fmt.Errorf("assertion failed: %s (compiling message expr: %w)", r.config.Expr, err)
			}
		}
		value, err := expr.Run(program, request.ExpressionEnvironment(nil))
		if err != nil {
			return fmt.Errorf("assertion failed: %s (evaluating message expr: %w)", r.config.Expr, err)
		}
		var ok bool
		message, ok = value.(string)
		if !ok {
			return fmt.Errorf("assertion failed: %s (message expr returned %T, want string)", r.config.Expr, value)
		}
	} else if request.TemplateRenderer != nil && !r.literalMessage {
		rendered, err := request.TemplateRenderer.Render(message)
		if err != nil {
			return fmt.Errorf("assertion failed: %s (rendering message: %w)", r.config.Expr, err)
		}
		message = rendered
	}
	if strings.TrimSpace(message) == "" {
		message = r.config.Expr
	}
	if r.config.Plain {
		code := 1
		if r.config.ExitCode != nil {
			code = *r.config.ExitCode
		}
		return &useroutput.Failure{Message: message, Code: code}
	}
	return fmt.Errorf("assertion failed: %s", message)
}
