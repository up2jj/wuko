package devenv

import (
	"context"
	"testing"

	"github.com/up2jj/wuko/executor"
	"github.com/up2jj/wuko/process"
	"github.com/up2jj/wuko/step"
)

type taskRecorder struct{ request executor.TaskRequest }

func (r *taskRecorder) Run(context.Context, process.Options) (process.Result, error) {
	return process.Result{}, nil
}

func (r *taskRecorder) RunTask(_ context.Context, request executor.TaskRequest) (process.Result, error) {
	r.request = request
	return process.Result{Stdout: "done"}, nil
}

func TestTaskRunnerPassesNameModeAndInputs(t *testing.T) {
	runner, err := NewTask(map[string]any{
		"name":   "app:build",
		"mode":   "all",
		"inputs": map[string]any{"target": "production"},
	})
	if err != nil {
		t.Fatal(err)
	}
	recorder := &taskRecorder{}
	result, err := runner.Run(t.Context(), step.Request{Executor: recorder})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outputs["stdout"] != "done" {
		t.Fatalf("outputs = %#v", result.Outputs)
	}
	if recorder.request.Name != "app:build" || recorder.request.Mode != "all" || recorder.request.Inputs["target"] != "production" {
		t.Fatalf("request = %#v", recorder.request)
	}
}

func TestTaskRunnerRejectsUnsupportedExecutor(t *testing.T) {
	runner, err := NewTask(map[string]any{"name": "app:build"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.Run(t.Context(), step.Request{Executor: process.LocalExecutor{}})
	if err == nil {
		t.Fatal("expected unsupported executor error")
	}
}

func TestTaskRunnerIsExecutorAware(t *testing.T) {
	runner, err := NewTask(map[string]any{"name": "app:build"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := runner.(step.ExecutorAware); !ok {
		t.Fatal("task runner is not executor-aware")
	}
}
