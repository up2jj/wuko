// Package review implements an interactive plain-text or unified-diff review step.
package review

import (
	"context"
	"fmt"
	"strings"

	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/tui"
)

const (
	formatPlain = "plain"
	formatDiff  = "diff"
)

type Config struct {
	Variable string `yaml:"variable"`
	Message  string `yaml:"message"`
	Content  string `yaml:"content"`
	Format   string `yaml:"format,omitempty"`
	Default  *bool  `yaml:"default,omitempty"`
}

type Runner struct{ config Config }

func Register(registry *step.Registry) error { return registry.Register("tui_review", New) }

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
	if strings.TrimSpace(config.Content) == "" {
		return nil, fmt.Errorf("content is required")
	}
	if config.Format == "" {
		config.Format = formatPlain
	}
	if config.Format != formatPlain && config.Format != formatDiff {
		return nil, fmt.Errorf("format must be plain or diff")
	}
	return &Runner{config: config}, nil
}

func (r *Runner) Run(ctx context.Context, request step.Request) (step.Result, error) {
	if value, exists := request.Vars[r.config.Variable]; exists {
		approved, ok := value.(bool)
		if !ok {
			return step.Result{}, fmt.Errorf("pre-supplied variable %q must be a boolean", r.config.Variable)
		}
		return result(r.config.Variable, approved), nil
	}
	if !request.Interactive {
		return step.Result{}, fmt.Errorf("variable %q is required when stdin is non-interactive; supply it with --var", r.config.Variable)
	}
	approved, err := tui.Review(ctx, request.Stdin, request.Stdout, tui.ReviewConfig{
		Message: r.config.Message, Content: r.config.Content, Format: r.config.Format, Default: defaultValue(r.config.Default),
	})
	if err != nil {
		return step.Result{}, fmt.Errorf("reviewing: %w", err)
	}
	return result(r.config.Variable, approved), nil
}

func defaultValue(value *bool) bool { return value != nil && *value }

func result(variable string, value bool) step.Result {
	return step.Result{Outputs: map[string]any{"value": value}, Variables: map[string]any{variable: value}}
}
