// Package password implements an interactive masked password step.
package password

import (
	"context"
	"fmt"
	"strings"

	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/tui"
)

type Config struct {
	Variable   string              `yaml:"variable"`
	Message    string              `yaml:"message"`
	Required   *bool               `yaml:"required,omitempty"`
	Validation step.TextValidation `yaml:"validation,omitempty"`
}

type Runner struct {
	config   Config
	validate step.TextValidator
}

func Register(registry *step.Registry) error { return registry.Register("tui_password", New) }

func New(raw map[string]any) (step.Runner, error) {
	var config Config
	if err := step.DecodeConfig(raw, &config); err != nil {
		return nil, err
	}
	if config.Variable == "" {
		return nil, fmt.Errorf("variable is required")
	}
	if strings.TrimSpace(config.Message) == "" {
		return nil, fmt.Errorf("message is required")
	}
	validate, err := step.CompileTextValidator(required(config.Required), config.Validation)
	if err != nil {
		return nil, err
	}
	return &Runner{config: config, validate: validate}, nil
}

func (r *Runner) Run(ctx context.Context, request step.Request) (step.Result, error) {
	if value, exists := request.Vars[r.config.Variable]; exists {
		text, ok := value.(string)
		if !ok {
			return step.Result{}, fmt.Errorf("pre-supplied variable %q must be a string", r.config.Variable)
		}
		if err := r.validate(text); err != nil {
			return step.Result{}, fmt.Errorf("pre-supplied variable %q: %w", r.config.Variable, err)
		}
		return result(r.config.Variable, text), nil
	}
	if !request.Interactive {
		return step.Result{}, fmt.Errorf("variable %q is required when stdin is non-interactive; supply it with --var", r.config.Variable)
	}
	value, err := tui.PasswordWithValidation(ctx, request.Stdin, request.Stdout, r.config.Message, required(r.config.Required), r.validate)
	if err != nil {
		return step.Result{}, fmt.Errorf("reading password: %w", err)
	}
	return result(r.config.Variable, value), nil
}

func required(value *bool) bool { return value == nil || *value }

func result(variable, value string) step.Result {
	return step.Result{Outputs: map[string]any{"value": value}, Variables: map[string]any{variable: value}}
}
