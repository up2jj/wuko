// Package confirm implements an interactive boolean confirmation step.
package confirm

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
	Default  *bool  `yaml:"default,omitempty"`
}

type Runner struct{ config Config }

func Register(registry *step.Registry) error { return registry.Register("confirm", New) }

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
		confirmed, ok := value.(bool)
		if !ok {
			return step.Result{}, fmt.Errorf("pre-supplied variable %q must be a boolean", r.config.Variable)
		}
		return result(r.config.Variable, confirmed), nil
	}
	if !request.Interactive {
		return step.Result{}, fmt.Errorf("variable %q is required when stdin is non-interactive; supply it with --var", r.config.Variable)
	}
	confirmed, err := tui.Confirm(ctx, request.Stdin, request.Stdout, r.config.Message, defaultValue(r.config.Default))
	if err != nil {
		return step.Result{}, fmt.Errorf("confirming: %w", err)
	}
	return result(r.config.Variable, confirmed), nil
}

func defaultValue(value *bool) bool { return value != nil && *value }

func result(variable string, value bool) step.Result {
	return step.Result{Outputs: map[string]any{"value": value}, Variables: map[string]any{variable: value}}
}
