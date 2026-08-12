package prompt

import (
	"context"
	"fmt"
	"strings"

	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/tui"
)

type Config struct {
	Variable string `yaml:"variable"`
	Message  string `yaml:"message"`
	Default  string `yaml:"default,omitempty"`
	Required *bool  `yaml:"required,omitempty"`
}

type Runner struct{ config Config }

func Register(registry *step.Registry) error { return registry.Register("prompt", New) }

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
	return &Runner{config: config}, nil
}

func (r *Runner) Run(ctx context.Context, request step.Request) (step.Result, error) {
	if value, exists := request.Vars[r.config.Variable]; exists {
		text, ok := value.(string)
		if !ok {
			return step.Result{}, fmt.Errorf("pre-supplied variable %q must be a string", r.config.Variable)
		}
		if r.required() && strings.TrimSpace(text) == "" {
			return step.Result{}, fmt.Errorf("pre-supplied variable %q cannot be empty", r.config.Variable)
		}
		return result(r.config.Variable, text), nil
	}
	if !request.Interactive {
		return step.Result{}, fmt.Errorf("variable %q is required when stdin is non-interactive; supply it with --var", r.config.Variable)
	}
	value, err := tui.Text(ctx, request.Stdin, request.Stdout, r.config.Message, r.config.Default, r.required())
	if err != nil {
		return step.Result{}, fmt.Errorf("prompting: %w", err)
	}
	return result(r.config.Variable, value), nil
}

func (r *Runner) required() bool { return r.config.Required == nil || *r.config.Required }

func result(variable, value string) step.Result {
	return step.Result{Outputs: map[string]any{"value": value}, Variables: map[string]any{variable: value}}
}
