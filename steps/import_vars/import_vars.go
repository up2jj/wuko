// Package importvars implements workflow variable imports from configuration files.
package importvars

import (
	"context"
	"fmt"

	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/variables"
)

type Config struct {
	Files []string `yaml:"files"`
}

type Runner struct {
	config Config
}

func Register(registry *step.Registry) error { return registry.Register("import_vars", New) }

func New(raw map[string]any) (step.Runner, error) {
	var config Config
	if err := step.DecodeConfig(raw, &config); err != nil {
		return nil, err
	}
	if len(config.Files) == 0 {
		return nil, fmt.Errorf("files must contain at least one path")
	}
	for index, path := range config.Files {
		if err := variables.ValidatePath(path); err != nil {
			return nil, fmt.Errorf("files[%d]: %w", index, err)
		}
	}
	return &Runner{config: config}, nil
}

func (r *Runner) Run(ctx context.Context, request step.Request) (step.Result, error) {
	values, err := variables.LoadFiles(ctx, request.WorkflowDir, r.config.Files)
	if err != nil {
		return step.Result{}, err
	}
	return step.Result{
		Outputs: map[string]any{
			"variables": values,
			"count":     len(values),
		},
		Variables: values,
	}, nil
}
