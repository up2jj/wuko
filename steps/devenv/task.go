package devenv

import (
	"context"
	"fmt"
	"strings"

	"github.com/up2jj/wuko/executor"
	"github.com/up2jj/wuko/expression"
	"github.com/up2jj/wuko/process"
	"github.com/up2jj/wuko/step"
)

// defaultCaptureLimit bounds each captured stream when capture_limit is omitted,
// matching the shell step's TTY capture bound. The step keeps the raw stdout and a
// materialized value tree for the rest of the run, so unlimited capture would let one
// task graph's output sit in memory several times over.
const defaultCaptureLimit = 1 << 20

type TaskConfig struct {
	Name         string         `yaml:"name,omitempty"`
	Names        []string       `yaml:"names,omitempty"`
	Mode         string         `yaml:"mode,omitempty"`
	Inputs       map[string]any `yaml:"inputs,omitempty"`
	ShowOutput   bool           `yaml:"show_output,omitempty"`
	CaptureLimit string         `yaml:"capture_limit,omitempty"`
}

type TaskRunner struct {
	config       TaskConfig
	captureLimit int64
}

func (*TaskRunner) ExecutorAware() {}

func RegisterTask(registry *step.Registry) error {
	return registry.RegisterDefinition("devenv_task", step.Registration{
		Builder: NewTask,
		Outputs: step.ClosedObject(map[string]step.OutputSchema{
			"stdout": step.Scalar(), "stderr": step.Scalar(), "exit_code": step.Scalar(),
			"stdout_truncated": step.Scalar(), "stderr_truncated": step.Scalar(),
			"value": step.OpenObject(), "value_decoded": step.Scalar(),
		}),
	})
}

func NewTask(raw map[string]any) (step.Runner, error) {
	var config TaskConfig
	if err := step.DecodeConfig(raw, &config); err != nil {
		return nil, err
	}
	_, hasName := raw["name"]
	_, hasNames := raw["names"]
	if hasName == hasNames {
		return nil, fmt.Errorf("exactly one of name or names is required")
	}
	if hasName && strings.TrimSpace(config.Name) == "" {
		return nil, fmt.Errorf("task name must not be blank")
	}
	if strings.HasPrefix(config.Name, "-") {
		return nil, fmt.Errorf("task name must not start with a dash")
	}
	if hasNames && len(config.Names) == 0 {
		return nil, fmt.Errorf("task names must not be empty")
	}
	for i, name := range config.Names {
		if strings.TrimSpace(name) == "" {
			return nil, fmt.Errorf("task name %d must not be blank", i+1)
		}
		if strings.HasPrefix(name, "-") {
			return nil, fmt.Errorf("task name %d must not start with a dash", i+1)
		}
	}
	if config.Mode == "" {
		config.Mode = "before"
	}
	if config.Mode != "single" && config.Mode != "before" && config.Mode != "after" && config.Mode != "all" {
		return nil, fmt.Errorf("task mode %q is invalid", config.Mode)
	}
	captureLimit := int64(defaultCaptureLimit)
	if config.CaptureLimit != "" && !strings.Contains(config.CaptureLimit, "{{") {
		var err error
		captureLimit, err = process.ParseCaptureLimit(config.CaptureLimit)
		if err != nil {
			return nil, fmt.Errorf("capture_limit %w", err)
		}
	}
	return &TaskRunner{config: config, captureLimit: captureLimit}, nil
}

func (r *TaskRunner) Run(ctx context.Context, request step.Request) (step.Result, error) {
	taskRunner, ok := request.Executor.(executor.TaskRunner)
	if !ok {
		return step.Result{}, fmt.Errorf("devenv_task requires an executor with task support")
	}
	result, err := taskRunner.RunTask(ctx, executor.TaskRequest{
		Name: r.config.Name, Names: r.config.Names, Mode: r.config.Mode, Inputs: r.config.Inputs,
		ShowOutput: r.config.ShowOutput, CaptureLimit: r.captureLimit,
		Stdin: request.Stdin, Stdout: request.Stdout, Stderr: request.Stderr,
	})
	outputs := map[string]any{
		"stdout": result.Stdout, "stderr": result.Stderr, "exit_code": result.ExitCode,
		"stdout_truncated": result.StdoutTruncated, "stderr_truncated": result.StderrTruncated,
	}
	if err != nil {
		return step.Result{Outputs: outputs}, err
	}
	// A zero exit means the task graph committed its side effects, so undecodable
	// stdout never fails the step: retry and catch would re-run that graph over an
	// outcome that cannot change. Wrappers (devenv shell, secretspec run) and
	// show_output both put non-JSON on stdout, and a graph that matched no task
	// writes nothing at all, so value_decoded reports what happened instead.
	outputs["value_decoded"] = false
	if strings.TrimSpace(result.Stdout) == "" && !result.StdoutTruncated {
		outputs["value"], outputs["value_decoded"] = map[string]any{}, true
		return step.Result{Outputs: outputs}, nil
	}
	if result.StdoutTruncated {
		return step.Result{Outputs: outputs}, nil
	}
	value, err := expression.ParseJSON(result.Stdout)
	if err != nil {
		return step.Result{Outputs: outputs}, nil
	}
	taskOutputs, ok := value.(map[string]any)
	if !ok {
		return step.Result{Outputs: outputs}, nil
	}
	outputs["value"], outputs["value_decoded"] = taskOutputs, true
	return step.Result{Outputs: outputs}, nil
}
