package devenv

import (
	"context"
	"errors"
	"math"
	"slices"
	"strings"
	"testing"

	"github.com/up2jj/wuko/executor"
	"github.com/up2jj/wuko/process"
	"github.com/up2jj/wuko/step"
)

type taskRecorder struct {
	request executor.TaskRequest
	result  process.Result
	err     error
}

func (r *taskRecorder) Run(context.Context, process.Options) (process.Result, error) {
	return process.Result{}, nil
}

func (r *taskRecorder) RunTask(_ context.Context, request executor.TaskRequest) (process.Result, error) {
	r.request = request
	return r.result, r.err
}

func TestTaskRunnerPassesConfigurationAndReturnsTypedOutputs(t *testing.T) {
	runner, err := NewTask(map[string]any{
		"names":         []any{"app:prepare", "app:build"},
		"mode":          "all",
		"inputs":        map[string]any{"target": "production"},
		"show_output":   true,
		"capture_limit": "1MiB",
	})
	if err != nil {
		t.Fatal(err)
	}
	recorder := &taskRecorder{result: process.Result{
		Stdout: `{"app:prepare":{"ready":true,"count":3,"ratio":1.5,"large":18446744073709551615,"items":["one",null],"devenv":{"env":{"TOKEN":"data-only"}}},"app:build":{"artifact":"dist/app"}}` + "\n",
		Stderr: "diagnostic\n",
	}}
	result, err := runner.Run(t.Context(), step.Request{Executor: recorder})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(recorder.request.Names, []string{"app:prepare", "app:build"}) || recorder.request.Name != "" {
		t.Fatalf("task roots = name %q, names %v", recorder.request.Name, recorder.request.Names)
	}
	if recorder.request.Mode != "all" || recorder.request.Inputs["target"] != "production" || !recorder.request.ShowOutput || recorder.request.CaptureLimit != 1<<20 {
		t.Fatalf("request = %#v", recorder.request)
	}
	if result.Outputs["stdout"] != recorder.result.Stdout || result.Outputs["stderr"] != "diagnostic\n" || result.Outputs["exit_code"] != 0 {
		t.Fatalf("process outputs = %#v", result.Outputs)
	}
	if result.Outputs["stdout_truncated"] != false || result.Outputs["stderr_truncated"] != false {
		t.Fatalf("truncation outputs = %#v", result.Outputs)
	}
	if result.Outputs["value_decoded"] != true {
		t.Fatalf("decoding outputs = %#v", result.Outputs)
	}
	if result.Variables != nil {
		t.Fatalf("task outputs unexpectedly became workflow variables: %#v", result.Variables)
	}
	value := result.Outputs["value"].(map[string]any)
	prepare := value["app:prepare"].(map[string]any)
	if prepare["ready"] != true || prepare["count"] != int64(3) || prepare["ratio"] != 1.5 || prepare["large"] != uint64(math.MaxUint64) {
		t.Fatalf("typed prepare output = %#v", prepare)
	}
	items := prepare["items"].([]any)
	if len(items) != 2 || items[0] != "one" || items[1] != nil {
		t.Fatalf("typed items = %#v", items)
	}
	if prepare["devenv"].(map[string]any)["env"].(map[string]any)["TOKEN"] != "data-only" {
		t.Fatalf("typed devenv exports = %#v", prepare["devenv"])
	}
}

func TestNewTaskValidatesRootsModeAndCaptureLimit(t *testing.T) {
	tests := []struct {
		name string
		raw  map[string]any
		want string
	}{
		{name: "missing roots", raw: map[string]any{}, want: "exactly one of name or names"},
		{name: "both root forms", raw: map[string]any{"name": "app:build", "names": []any{"app:test"}}, want: "exactly one of name or names"},
		{name: "empty names", raw: map[string]any{"names": []any{}}, want: "task names must not be empty"},
		{name: "empty name alongside names", raw: map[string]any{"name": "", "names": []any{"app:test"}}, want: "exactly one of name or names"},
		{name: "blank name", raw: map[string]any{"name": "  "}, want: "must not be blank"},
		{name: "blank list item", raw: map[string]any{"names": []any{"app:build", " "}}, want: "task name 2 must not be blank"},
		{name: "flag-like name", raw: map[string]any{"name": "--input-json"}, want: "task name must not start with a dash"},
		{name: "flag-like list item", raw: map[string]any{"names": []any{"app:build", "--mode"}}, want: "task name 2 must not start with a dash"},
		{name: "invalid mode", raw: map[string]any{"name": "app:build", "mode": "nearby"}, want: "task mode"},
		{name: "invalid capture limit", raw: map[string]any{"name": "app:build", "capture_limit": "1MB"}, want: "capture_limit must be"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewTask(test.raw)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("NewTask() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestTaskRunnerDefaultsModeAndCaptureLimit(t *testing.T) {
	runner, err := NewTask(map[string]any{"name": "app:build"})
	if err != nil {
		t.Fatal(err)
	}
	recorder := &taskRecorder{result: process.Result{Stdout: "{}\n"}}
	if _, err := runner.Run(t.Context(), step.Request{Executor: recorder}); err != nil {
		t.Fatal(err)
	}
	if recorder.request.Name != "app:build" || recorder.request.Mode != "before" || recorder.request.CaptureLimit != 1<<20 {
		t.Fatalf("request = %#v", recorder.request)
	}
}

func TestTaskRunnerDecodesUnusableTypedOutputWithoutFailing(t *testing.T) {
	tests := []struct {
		name   string
		result process.Result
	}{
		{name: "truncated stdout", result: process.Result{Stdout: `{"app:build":`, StdoutTruncated: true}},
		{name: "invalid JSON", result: process.Result{Stdout: "not-json"}},
		{name: "non-object JSON", result: process.Result{Stdout: "[]"}},
		{name: "wrapper noise around JSON", result: process.Result{Stdout: "entering shell\n{\"app:build\":{}}\n"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner, err := NewTask(map[string]any{"name": "app:build"})
			if err != nil {
				t.Fatal(err)
			}
			result, err := runner.Run(t.Context(), step.Request{Executor: &taskRecorder{result: test.result}})
			if err != nil {
				t.Fatalf("Run() error = %v, want a successful step", err)
			}
			if _, ok := result.Outputs["value"]; ok {
				t.Fatalf("undecodable task output unexpectedly returned value: %#v", result.Outputs)
			}
			if result.Outputs["value_decoded"] != false || result.Outputs["stdout"] != test.result.Stdout {
				t.Fatalf("outputs = %#v", result.Outputs)
			}
		})
	}
}

func TestTaskRunnerTreatsEmptyStdoutAsNoTaskOutputs(t *testing.T) {
	runner, err := NewTask(map[string]any{"name": "app:build"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(t.Context(), step.Request{Executor: &taskRecorder{result: process.Result{Stdout: "\n"}}})
	if err != nil {
		t.Fatal(err)
	}
	value, ok := result.Outputs["value"].(map[string]any)
	if !ok || len(value) != 0 || result.Outputs["value_decoded"] != true {
		t.Fatalf("outputs = %#v", result.Outputs)
	}
}

func TestTaskRunnerPreservesProcessOutputsOnFailure(t *testing.T) {
	runner, err := NewTask(map[string]any{"name": "app:build"})
	if err != nil {
		t.Fatal(err)
	}
	recorder := &taskRecorder{
		result: process.Result{Stdout: "partial", Stderr: "failed", ExitCode: 7, StdoutTruncated: true, StderrTruncated: true},
		err:    errors.New("task failed"),
	}
	result, err := runner.Run(t.Context(), step.Request{Executor: recorder})
	if err == nil || !strings.Contains(err.Error(), "task failed") {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Outputs["stdout"] != "partial" || result.Outputs["stderr"] != "failed" || result.Outputs["exit_code"] != 7 || result.Outputs["stdout_truncated"] != true || result.Outputs["stderr_truncated"] != true {
		t.Fatalf("outputs = %#v", result.Outputs)
	}
	if _, ok := result.Outputs["value"]; ok {
		t.Fatalf("failed task unexpectedly returned value: %#v", result.Outputs)
	}
	if _, ok := result.Outputs["value_decoded"]; ok {
		t.Fatalf("failed task unexpectedly reported decoding: %#v", result.Outputs)
	}
}

func TestTaskRunnerAllowsTruncatedStderr(t *testing.T) {
	runner, err := NewTask(map[string]any{"name": "app:build", "capture_limit": "4B"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(t.Context(), step.Request{Executor: &taskRecorder{result: process.Result{Stdout: "{}", Stderr: "warn", StderrTruncated: true}}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outputs["stderr_truncated"] != true {
		t.Fatalf("outputs = %#v", result.Outputs)
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
