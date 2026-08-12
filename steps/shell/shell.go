package shell

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
	Command          string               `yaml:"command,omitempty"`
	Script           string               `yaml:"script,omitempty"`
	Shell            string               `yaml:"shell,omitempty"`
	Args             []string             `yaml:"args,omitempty"`
	WorkingDirectory string               `yaml:"working_directory,omitempty"`
	Env              workflow.Environment `yaml:"env,omitempty"`
	Stdin            string               `yaml:"stdin,omitempty"`
}

type Runner struct{ config Config }

func Register(registry *step.Registry) error { return registry.Register("shell", New) }

func New(raw map[string]any) (step.Runner, error) {
	var config Config
	if err := step.DecodeConfig(raw, &config); err != nil {
		return nil, err
	}
	if (config.Command == "") == (config.Script == "") {
		return nil, fmt.Errorf("exactly one of command or script is required")
	}
	for key := range config.Env {
		if !workflow.ValidEnvironmentName(key) {
			return nil, fmt.Errorf("invalid environment name %q", key)
		}
	}
	return &Runner{config: config}, nil
}

func (r *Runner) Run(ctx context.Context, request step.Request) (step.Result, error) {
	command, args := r.command()
	dir := r.config.WorkingDirectory
	if dir == "" {
		dir = request.RunDir
	} else if !filepath.IsAbs(dir) {
		dir = filepath.Join(request.RunDir, dir)
	}
	environment := maps.Clone(request.Env)
	maps.Copy(environment, r.config.Env)
	result, err := process.Run(ctx, process.Options{
		Command: command, Args: args, Dir: dir, Env: environment,
		Stdin: process.StringInput(r.config.Stdin), Stdout: request.Stdout, Stderr: request.Stderr,
	})
	outputs := map[string]any{"stdout": result.Stdout, "stderr": result.Stderr, "exit_code": result.ExitCode}
	if err != nil {
		return step.Result{Outputs: outputs}, err
	}
	return step.Result{Outputs: outputs}, nil
}

func (r *Runner) command() (string, []string) {
	if r.config.Command != "" {
		return r.config.Command, r.config.Args
	}
	shell := r.config.Shell
	if shell == "" {
		shell = "/bin/sh"
	}
	args := []string{"-c", r.config.Script, "wuko"}
	args = append(args, r.config.Args...)
	return shell, args
}

func (r *Runner) Validate(_ context.Context, _ step.Request) error {
	if r.config.Script != "" && strings.TrimSpace(r.config.Script) == "" {
		return fmt.Errorf("script cannot be blank")
	}
	return nil
}
