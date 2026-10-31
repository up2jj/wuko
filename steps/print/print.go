// Package print implements explicit user-facing workflow messages.
package print

import (
	"context"
	"fmt"
	"strings"

	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/useroutput"
)

type Config struct {
	Message string           `yaml:"message"`
	Level   useroutput.Level `yaml:"level,omitempty"`
}

type Runner struct{ config Config }

func Register(registry *step.Registry) error {
	return registry.RegisterDefinition("print", step.Registration{Builder: New, Config: Config{}, Outputs: step.ClosedOutputs()})
}

func New(raw map[string]any) (step.Runner, error) {
	config := Config{Level: useroutput.Info}
	if err := step.DecodeConfig(raw, &config); err != nil {
		return nil, err
	}
	if value, exists := raw["message"]; exists && step.ConfigKnown(raw, "message") {
		if err := step.CheckBindingType(Config{}, []string{"message"}, value); err != nil {
			return nil, err
		}
	}
	if strings.TrimSpace(config.Message) == "" {
		return nil, fmt.Errorf("message is required")
	}
	if step.ConfigKnown(raw, "level") && !config.Level.Valid() {
		return nil, fmt.Errorf("level must be info, success, warn, or error")
	}
	return &Runner{config: config}, nil
}

func (runner *Runner) Run(ctx context.Context, request step.Request) (step.Result, error) {
	if err := ctx.Err(); err != nil {
		return step.Result{}, err
	}
	request.PrintMessage(useroutput.Message{Text: runner.config.Message, Level: runner.config.Level})
	return step.Result{}, nil
}
