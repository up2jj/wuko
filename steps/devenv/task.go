package devenv

import (
	"context"
	"fmt"

	"github.com/up2jj/wuko/executor"
	"github.com/up2jj/wuko/step"
)

type TaskConfig struct {
	Name   string         `yaml:"name"`
	Mode   string         `yaml:"mode,omitempty"`
	Inputs map[string]any `yaml:"inputs,omitempty"`
}

type TaskRunner struct{ config TaskConfig }

func (*TaskRunner) ExecutorAware() {}

func RegisterTask(registry *step.Registry) error {
	return registry.Register("devenv_task", NewTask)
}

func NewTask(raw map[string]any) (step.Runner, error) {
	var config TaskConfig
	if err := step.DecodeConfig(raw, &config); err != nil {
		return nil, err
	}
	if config.Name == "" {
		return nil, fmt.Errorf("task name is required")
	}
	if config.Mode == "" {
		config.Mode = "before"
	}
	if config.Mode != "single" && config.Mode != "before" && config.Mode != "after" && config.Mode != "all" {
		return nil, fmt.Errorf("task mode %q is invalid", config.Mode)
	}
	return &TaskRunner{config: config}, nil
}

func (r *TaskRunner) Run(ctx context.Context, request step.Request) (step.Result, error) {
	taskRunner, ok := request.Executor.(executor.TaskRunner)
	if !ok {
		return step.Result{}, fmt.Errorf("devenv_task requires an executor with task support")
	}
	result, err := taskRunner.RunTask(ctx, executor.TaskRequest{
		Name: r.config.Name, Mode: r.config.Mode, Inputs: r.config.Inputs,
		Stdin: request.Stdin, Stdout: request.Stdout, Stderr: request.Stderr,
	})
	outputs := map[string]any{"stdout": result.Stdout, "stderr": result.Stderr, "exit_code": result.ExitCode}
	if err != nil {
		return step.Result{Outputs: outputs}, err
	}
	return step.Result{Outputs: outputs}, nil
}
