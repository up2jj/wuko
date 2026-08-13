package agent

import (
	"context"
	"fmt"
	"maps"
	"path/filepath"
	"strings"

	"github.com/up2jj/wuko/process"
	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/workflow"
)

type Config struct {
	Command          string               `yaml:"command"`
	Args             []string             `yaml:"args,omitempty"`
	Prompt           string               `yaml:"prompt"`
	WorkingDirectory string               `yaml:"working_directory,omitempty"`
	Env              workflow.Environment `yaml:"env,omitempty"`
}

type Runner struct{ config Config }

func Register(registry *step.Registry) error { return registry.Register("agent", New) }

func New(raw map[string]any) (step.Runner, error) {
	var config Config
	if err := step.DecodeConfig(raw, &config); err != nil {
		return nil, err
	}
	if config.Command == "" {
		return nil, fmt.Errorf("command is required")
	}
	for key := range config.Env {
		if !workflow.ValidEnvironmentName(key) {
			return nil, fmt.Errorf("invalid environment name %q", key)
		}
	}
	return &Runner{config: config}, nil
}

func (r *Runner) Run(ctx context.Context, request step.Request) (step.Result, error) {
	dir := r.config.WorkingDirectory
	if dir == "" {
		dir = request.RunDir
	} else if !filepath.IsAbs(dir) {
		dir = filepath.Join(request.RunDir, dir)
	}
	environment := maps.Clone(request.Env)
	maps.Copy(environment, r.config.Env)
	environment = step.ApplyAttemptEnvironment(environment, request)
	input := r.config.Prompt
	if input != "" && !strings.HasSuffix(input, "\n") {
		input += "\n"
	}
	result, err := process.Run(ctx, process.Options{
		Command: r.config.Command, Args: r.config.Args, Dir: dir, Env: environment,
		Stdin: strings.NewReader(input), Stdout: request.Stdout, Stderr: request.Stderr,
	})
	outputs := map[string]any{"stdout": result.Stdout, "stderr": result.Stderr, "exit_code": result.ExitCode}
	if err != nil {
		return step.Result{Outputs: outputs}, err
	}
	return step.Result{Outputs: outputs}, nil
}
