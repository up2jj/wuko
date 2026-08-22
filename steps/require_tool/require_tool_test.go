package requiretool

import (
	"context"
	"errors"
	"io"
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/up2jj/wuko/process"
	"github.com/up2jj/wuko/step"
)

type recordingExecutor struct {
	options process.Options
	result  process.Result
	err     error
}

func (executor *recordingExecutor) Run(_ context.Context, options process.Options) (process.Result, error) {
	executor.options = options
	return executor.result, executor.err
}

func TestNewValidatesConfiguration(t *testing.T) {
	tests := []struct {
		name string
		raw  map[string]any
	}{
		{"missing tool", map[string]any{}},
		{"blank tool", map[string]any{"tool": "  "}},
		{"invalid constraint", map[string]any{"tool": "go", "constraint": ">> 2"}},
		{"unknown field", map[string]any{"tool": "go", "unknown": true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := New(tt.raw); err == nil || strings.TrimSpace(err.Error()) == "" {
				t.Fatalf("New(%#v) error = %v", tt.raw, err)
			}
		})
	}
}

func TestDefaultAndExplicitEmptyVersionArguments(t *testing.T) {
	for _, tt := range []struct {
		name string
		raw  map[string]any
		want []string
	}{
		{"default", map[string]any{"tool": "go"}, []string{"--version"}},
		{"explicit empty", map[string]any{"tool": "go", "version_args": []any{}}, []string{}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			executor := &recordingExecutor{}
			runner, err := New(tt.raw)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := runner.Run(t.Context(), step.Request{Executor: executor}); err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(executor.options.Args, tt.want) {
				t.Fatalf("args = %#v, want %#v", executor.options.Args, tt.want)
			}
		})
	}
}

func TestRunUsesExecutorEnvironmentAndReturnsName(t *testing.T) {
	executor := &recordingExecutor{}
	runner, err := New(map[string]any{"tool": "go", "version_args": []any{"version"}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(t.Context(), step.Request{
		Executor: executor, RunDir: "/workspace", Env: map[string]string{"CHANNEL": "stable"},
		Attempt: 2, MaxAttempts: 3, OperationID: "probe-go",
	})
	if err != nil {
		t.Fatal(err)
	}
	if executor.options.Command != "go" || !slices.Equal(executor.options.Args, []string{"version"}) || executor.options.Dir != "/workspace" {
		t.Fatalf("options = %#v", executor.options)
	}
	if executor.options.Env["CHANNEL"] != "stable" || executor.options.Env[step.AttemptEnv] != "2" || executor.options.Env[step.OperationIDEnv] != "probe-go" {
		t.Fatalf("environment = %#v", executor.options.Env)
	}
	if executor.options.Stdout != nil || executor.options.Stderr != nil || executor.options.CaptureLimit != captureLimit {
		t.Fatalf("capture options = %#v", executor.options)
	}
	if result.Outputs["path"] != "go" {
		t.Fatalf("outputs = %#v", result.Outputs)
	}
}

func TestRunExtractsAndConstrainsVersions(t *testing.T) {
	tests := []struct {
		name       string
		stdout     string
		stderr     string
		constraint string
		want       string
	}{
		{"stdout", "go version go1.26.1 darwin/arm64\n", "", ">= 1.25.0", "1.26.1"},
		{"stderr v prefix", "", "tool v2.3.4-rc.1+build.7\n", ">= 2.3.4-rc.1, < 3.0.0", "2.3.4-rc.1+build.7"},
		{"stdout precedes stderr", "tool 1.5.0\n", "tool 2.0.0\n", "^1.0", "1.5.0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			executor := &recordingExecutor{result: process.Result{Stdout: tt.stdout, Stderr: tt.stderr}}
			runner, err := New(map[string]any{"tool": "tool", "constraint": tt.constraint})
			if err != nil {
				t.Fatal(err)
			}
			result, err := runner.Run(t.Context(), step.Request{Executor: executor})
			if err != nil {
				t.Fatal(err)
			}
			if result.Outputs["version"] != tt.want {
				t.Fatalf("outputs = %#v", result.Outputs)
			}
		})
	}
}

func TestRunRejectsMissingOrUnsupportedVersion(t *testing.T) {
	for _, tt := range []struct {
		name   string
		output string
		want   string
	}{
		{"missing", "development build", "does not contain a semantic version"},
		{"mismatch", "tool 1.9.0", "does not satisfy constraint"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			executor := &recordingExecutor{result: process.Result{Stdout: tt.output}}
			runner, err := New(map[string]any{"tool": "tool", "constraint": ">= 2.0.0"})
			if err != nil {
				t.Fatal(err)
			}
			result, err := runner.Run(t.Context(), step.Request{Executor: executor})
			if err == nil || !strings.Contains(err.Error(), tt.want) || len(result.Outputs) != 0 {
				t.Fatalf("result = %#v, error = %v", result, err)
			}
		})
	}
}

func TestRunReportsUnavailableToolWithoutOutputs(t *testing.T) {
	executor := &recordingExecutor{
		result: process.Result{Stderr: "not found\n", ExitCode: 127},
		err:    &process.ExitError{Command: "missing", Code: 127},
	}
	runner, err := New(map[string]any{"tool": "missing"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(t.Context(), step.Request{Executor: executor})
	if err == nil || !strings.Contains(err.Error(), `required tool "missing" is unavailable: not found`) || len(result.Outputs) != 0 {
		t.Fatalf("result = %#v, error = %v", result, err)
	}
}

func TestRunPreservesCancellation(t *testing.T) {
	executor := &recordingExecutor{err: context.Canceled}
	runner, err := New(map[string]any{"tool": "tool"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(t.Context(), step.Request{Executor: executor})
	if !errors.Is(err, context.Canceled) || len(result.Outputs) != 0 {
		t.Fatalf("result = %#v, error = %v", result, err)
	}
}

func TestTemplatedConfigurationIsAcceptedBeforeRendering(t *testing.T) {
	if _, err := New(map[string]any{
		"tool": "{{ .vars.tool }}", "version_args": []any{"{{ .vars.arg }}"}, "constraint": "{{ .vars.constraint }}",
	}); err != nil {
		t.Fatal(err)
	}
}

func TestLocalRunReturnsResolvedPathWithoutProbeOutput(t *testing.T) {
	runner, err := New(map[string]any{"tool": "sh", "version_args": []any{"-c", "printf 1.2.3"}, "constraint": "= 1.2.3"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(t.Context(), step.Request{Stdout: io.Discard, Stderr: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	want, err := exec.LookPath("sh")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(result.Outputs["path"].(string), want) || result.Outputs["version"] != "1.2.3" {
		t.Fatalf("outputs = %#v, want path ending in %q", result.Outputs, want)
	}
}
