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

const ttyCaptureLimit = 1 << 20

type Config struct {
	Command          string               `yaml:"command,omitempty"`
	Script           string               `yaml:"script,omitempty"`
	Shell            string               `yaml:"shell,omitempty"`
	Args             []string             `yaml:"args,omitempty"`
	WorkingDirectory string               `yaml:"working_directory,omitempty"`
	Env              workflow.Environment `yaml:"env,omitempty"`
	User             string               `yaml:"user,omitempty"`
	Stdin            string               `yaml:"stdin,omitempty"`
	TTY              bool                 `yaml:"tty,omitempty"`
	Stdout           string               `yaml:"stdout,omitempty"`
	Stderr           string               `yaml:"stderr,omitempty"`
	CaptureLimit     string               `yaml:"capture_limit,omitempty"`
}

type Runner struct {
	config       Config
	stdoutPolicy process.OutputPolicy
	stderrPolicy process.OutputPolicy
	captureLimit int64
}

func (*Runner) ExecutorAware() {}

func Register(registry *step.Registry) error { return registry.Register("shell", New) }

func New(raw map[string]any) (step.Runner, error) {
	var config Config
	if err := step.DecodeConfig(raw, &config); err != nil {
		return nil, err
	}
	if (config.Command == "") == (config.Script == "") {
		return nil, fmt.Errorf("exactly one of command or script is required")
	}
	if config.Script != "" && strings.TrimSpace(config.Script) == "" {
		return nil, fmt.Errorf("script cannot be blank")
	}
	if config.TTY && config.Stdin != "" {
		return nil, fmt.Errorf("tty and stdin cannot be combined")
	}
	if config.TTY && (config.Stdout != "" || config.Stderr != "" || config.CaptureLimit != "") {
		return nil, fmt.Errorf("tty cannot be combined with stdout, stderr, or capture_limit")
	}
	for key := range config.Env {
		if !workflow.ValidEnvironmentName(key) {
			return nil, fmt.Errorf("invalid environment name %q", key)
		}
	}
	stdoutPolicy, err := configuredOutputPolicy("stdout", config.Stdout)
	if err != nil {
		return nil, err
	}
	stderrPolicy, err := configuredOutputPolicy("stderr", config.Stderr)
	if err != nil {
		return nil, err
	}
	captureLimit := int64(0)
	if config.CaptureLimit != "" && !templated(config.CaptureLimit) {
		captureLimit, err = process.ParseCaptureLimit(config.CaptureLimit)
		if err != nil {
			return nil, fmt.Errorf("capture_limit %w", err)
		}
	}
	return &Runner{config: config, stdoutPolicy: stdoutPolicy, stderrPolicy: stderrPolicy, captureLimit: captureLimit}, nil
}

func (r *Runner) Run(ctx context.Context, request step.Request) (step.Result, error) {
	if r.config.TTY && (!request.Interactive || request.Stdin == nil) {
		return step.Result{}, fmt.Errorf("tty requires an interactive terminal")
	}
	command, args := r.command()
	dir := r.config.WorkingDirectory
	if dir == "" {
		dir = request.RunDir
	} else if !filepath.IsAbs(dir) {
		dir = filepath.Join(request.RunDir, dir)
	}
	environment := maps.Clone(request.Env)
	maps.Copy(environment, r.config.Env)
	environment = step.ApplyAttemptEnvironment(environment, request)
	executor := request.Executor
	if executor == nil {
		executor = process.LocalExecutor{}
	}
	stdin := process.StringInput(r.config.Stdin)
	captureLimit := r.captureLimit
	if r.config.TTY {
		stdin = request.Stdin
		captureLimit = ttyCaptureLimit
	}
	result, err := executor.Run(ctx, process.Options{
		Command: command, Args: args, Dir: dir, Env: environment, User: r.config.User,
		Stdin: stdin, Stdout: request.Stdout, Stderr: request.Stderr, TTY: r.config.TTY, CaptureLimit: captureLimit,
		StdoutPolicy: r.stdoutPolicy, StderrPolicy: r.stderrPolicy,
	})
	outputs := map[string]any{
		"stdout": result.Stdout, "stderr": result.Stderr, "exit_code": result.ExitCode,
		"stdout_truncated": result.StdoutTruncated, "stderr_truncated": result.StderrTruncated,
	}
	if err != nil {
		return step.Result{Outputs: outputs}, err
	}
	return step.Result{Outputs: outputs}, nil
}

func configuredOutputPolicy(field, value string) (process.OutputPolicy, error) {
	if templated(value) {
		return process.OutputTee, nil
	}
	policy, err := process.ParseOutputPolicy(value)
	if err != nil {
		return process.OutputTee, fmt.Errorf("%s %w", field, err)
	}
	return policy, nil
}

func templated(value string) bool { return strings.Contains(value, "{{") }

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
