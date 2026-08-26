package shell

import (
	"context"
	"errors"
	"io"
	"math"
	"os"
	"os/user"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/up2jj/wuko/process"
	"github.com/up2jj/wuko/step"
)

func TestInlineShellArgumentsAndEnvironment(t *testing.T) {
	runner, err := New(map[string]any{
		"script": `printf '%s:%s' "$1" "$VALUE"`,
		"args":   []any{"argument"},
		"env":    map[string]any{"VALUE": "step"},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(t.Context(), step.Request{
		RunDir: t.TempDir(), Env: map[string]string{"VALUE": "base"}, Stdout: io.Discard, Stderr: io.Discard,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := result.Outputs["stdout"]; got != "argument:step" {
		t.Fatalf("stdout = %q", got)
	}
	if result.Outputs["stdout_truncated"] != false || result.Outputs["stderr_truncated"] != false {
		t.Fatalf("truncation outputs = %#v", result.Outputs)
	}
}

func TestNewRejectsBlankScript(t *testing.T) {
	if _, err := New(map[string]any{"script": " \t\n"}); err == nil || !strings.Contains(err.Error(), "script cannot be blank") {
		t.Fatalf("New() error = %v, want blank script error", err)
	}
}

func TestNewAcceptsTemplatedScript(t *testing.T) {
	if _, err := New(map[string]any{"script": "{{ .vars.script }}"}); err != nil {
		t.Fatalf("New() error = %v", err)
	}
}

func TestNewValidatesArgvConfiguration(t *testing.T) {
	tests := []struct {
		name string
		raw  map[string]any
		want string
	}{
		{name: "static list", raw: map[string]any{"argv": []any{"echo"}}, want: "argv must be an object"},
		{name: "null", raw: map[string]any{"argv": nil}, want: "argv must be an object"},
		{name: "blank expression", raw: map[string]any{"argv": map[string]any{"expr": "  "}}, want: "argv expr must be a non-empty string"},
		{name: "non-string expression", raw: map[string]any{"argv": map[string]any{"expr": 42}}, want: "argv expr must be a string"},
		{name: "unknown expression field", raw: map[string]any{"argv": map[string]any{"expr": "steps.resolve.argv", "fallback": []any{"false"}}}, want: "exactly the expr field"},
		{name: "invalid expression", raw: map[string]any{"argv": map[string]any{"expr": "steps."}}, want: "compiling argv expr"},
		{name: "command", raw: map[string]any{"argv": map[string]any{"expr": "steps.resolve.argv"}, "command": "echo"}, want: "cannot be combined with command"},
		{name: "script", raw: map[string]any{"argv": map[string]any{"expr": "steps.resolve.argv"}, "script": "echo"}, want: "cannot be combined with script"},
		{name: "shell", raw: map[string]any{"argv": map[string]any{"expr": "steps.resolve.argv"}, "shell": "/bin/bash"}, want: "cannot be combined with shell"},
		{name: "args", raw: map[string]any{"argv": map[string]any{"expr": "steps.resolve.argv"}, "args": []any{}}, want: "cannot be combined with args"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := New(test.raw)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("New() error = %v, want %q", err, test.want)
			}
		})
	}

	for _, raw := range []map[string]any{
		{"command": "echo", "args": []any{"hello"}},
		{"script": "echo hello"},
		{"argv": map[string]any{"expr": "steps.resolve.argv"}},
	} {
		if _, err := New(raw); err != nil {
			t.Fatalf("New(%#v) error = %v", raw, err)
		}
	}
}

func TestShellEvaluatesTypedArgvWithoutParsing(t *testing.T) {
	executor := &recordingExecutor{}
	runner, err := New(map[string]any{"argv": map[string]any{"expr": "steps.resolve.argv"}})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/app/bin/recruitee", "with spaces", `a"quote`, "*.go", "$HOME", "", "x; rm -rf ignored"}
	values := make([]any, len(want))
	for i, value := range want {
		values[i] = value
	}
	_, err = runner.Run(t.Context(), step.Request{
		RunDir: t.TempDir(), Env: map[string]string{}, Steps: map[string]any{"resolve": map[string]any{"argv": values}},
		Stdout: io.Discard, Stderr: io.Discard, Executor: executor,
	})
	if err != nil {
		t.Fatal(err)
	}
	got := append([]string{executor.options.Command}, executor.options.Args...)
	if !slices.Equal(got, want) {
		t.Fatalf("argv = %#v, want %#v", got, want)
	}
}

func TestShellArgvExpressionUsesRuntimeRoots(t *testing.T) {
	executor := &recordingExecutor{}
	runner, err := New(map[string]any{"argv": map[string]any{
		"expr": "list(inputs.command, vars.arg, env.MODE, dependencies.build.artifact, batch.name, foreach.item, matrix.os, finally.status, workflow.name, workflow.dir, run.dir)",
	}})
	if err != nil {
		t.Fatal(err)
	}
	request := step.Request{
		WorkflowName: "release", WorkflowDir: "/workflow", RunDir: "/run",
		Inputs: map[string]any{"command": "tool"}, Vars: map[string]any{"arg": "value"}, Env: map[string]string{"MODE": "test"},
		Dependencies: map[string]map[string]any{"build": {"artifact": "app"}},
		Bindings: map[string]any{
			"batch": map[string]any{"name": "batch"}, "foreach": map[string]any{"item": "item"},
			"matrix": map[string]any{"os": "linux"}, "finally": map[string]any{"status": "succeeded"},
		},
		Stdout: io.Discard, Stderr: io.Discard, Executor: executor,
	}
	if _, err := runner.Run(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	want := []string{"tool", "value", "test", "app", "batch", "item", "linux", "succeeded", "release", "/workflow", "/run"}
	got := append([]string{executor.options.Command}, executor.options.Args...)
	if !slices.Equal(got, want) {
		t.Fatalf("argv = %#v, want %#v", got, want)
	}
}

func TestArgvStringsConvertsScalars(t *testing.T) {
	got, err := argvStrings([]any{"tool", true, int8(-2), uint16(3), float32(1.25), float64(2.5)})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"tool", "true", "-2", "3", "1.25", "2.5"}
	if !slices.Equal(got, want) {
		t.Fatalf("argvStrings() = %#v, want %#v", got, want)
	}
}

func TestShellRejectsInvalidArgvResults(t *testing.T) {
	tests := []struct {
		name  string
		value any
		want  string
	}{
		{name: "not list", value: "echo", want: "want a list"},
		{name: "empty", value: []any{}, want: "empty list"},
		{name: "empty executable", value: []any{""}, want: "empty executable"},
		{name: "null", value: []any{"tool", nil}, want: "null is not"},
		{name: "object", value: []any{"tool", map[string]any{"value": "arg"}}, want: "map is not"},
		{name: "nested list", value: []any{"tool", []any{"arg"}}, want: "slice is not"},
		{name: "non-finite", value: []any{"tool", math.Inf(1)}, want: "non-finite"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner, err := New(map[string]any{"argv": map[string]any{"expr": "steps.resolve.argv"}})
			if err != nil {
				t.Fatal(err)
			}
			_, err = runner.Run(t.Context(), step.Request{
				RunDir: t.TempDir(), Env: map[string]string{}, Steps: map[string]any{"resolve": map[string]any{"argv": test.value}},
				Stdout: io.Discard, Stderr: io.Discard, Executor: &recordingExecutor{},
			})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Run() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestNewRejectsTTYWithConfiguredStdin(t *testing.T) {
	_, err := New(map[string]any{"command": "sh", "tty": true, "stdin": "input"})
	if err == nil || !strings.Contains(err.Error(), "tty and stdin cannot be combined") {
		t.Fatalf("New() error = %v", err)
	}
}

func TestNewValidatesInteractionConfiguration(t *testing.T) {
	tests := []struct {
		name string
		raw  map[string]any
		want string
	}{
		{name: "requires tty", raw: map[string]any{"command": "sh", "interactions": []any{map[string]any{"send": "x"}}}, want: "interactions require tty"},
		{name: "empty", raw: map[string]any{"command": "sh", "tty": true, "interactions": []any{}}, want: "at least one"},
		{name: "interact alone", raw: map[string]any{"command": "sh", "tty": true, "interact": true}, want: "interact requires interactions"},
		{name: "missing send", raw: map[string]any{"command": "sh", "tty": true, "interactions": []any{map[string]any{"expect": "ready"}}}, want: "send is required"},
		{name: "non-string send", raw: map[string]any{"command": "sh", "tty": true, "interactions": []any{map[string]any{"send": 42}}}, want: "send must be a string"},
		{name: "unknown field", raw: map[string]any{"command": "sh", "tty": true, "interactions": []any{map[string]any{"send": "x", "delay": "1s"}}}, want: "field delay not found"},
		{name: "empty expect", raw: map[string]any{"command": "sh", "tty": true, "interactions": []any{map[string]any{"expect": "", "send": "x"}}}, want: "expect must not be empty"},
		{name: "invalid expect", raw: map[string]any{"command": "sh", "tty": true, "interactions": []any{map[string]any{"expect": "[", "send": "x"}}}, want: "compiling expect"},
		{name: "timeout without expect", raw: map[string]any{"command": "sh", "tty": true, "interactions": []any{map[string]any{"send": "x", "timeout": "1s"}}}, want: "timeout requires expect"},
		{name: "invalid timeout", raw: map[string]any{"command": "sh", "tty": true, "interactions": []any{map[string]any{"expect": "ready", "send": "x", "timeout": "soon"}}}, want: "invalid timeout"},
		{name: "zero timeout", raw: map[string]any{"command": "sh", "tty": true, "interactions": []any{map[string]any{"expect": "ready", "send": "x", "timeout": "0s"}}}, want: "timeout must be positive"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := New(test.raw)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("New() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestNewAcceptsImmediatePromptAndTemplatedInteractions(t *testing.T) {
	for _, interactions := range []any{
		[]any{map[string]any{"send": "first", "newline": true}, map[string]any{"send": "second"}},
		[]any{map[string]any{"expect": "ready>", "send": "go", "timeout": "1s", "sensitive": true}},
		[]any{map[string]any{"expect": "{{ .vars.prompt }}", "send": "{{ .steps.answer.value }}", "timeout": "{{ .vars.timeout }}"}},
	} {
		if _, err := New(map[string]any{"command": "sh", "tty": true, "interactions": interactions}); err != nil {
			t.Fatalf("New() error = %v", err)
		}
	}
}

func TestShellHeadlessInteractionsDelegateWithoutTerminalInput(t *testing.T) {
	executor := &recordingExecutor{}
	runner, err := New(map[string]any{
		"command": "sh", "tty": true,
		"interactions": []any{map[string]any{"send": "first", "newline": true}, map[string]any{"expect": "ready", "send": "second"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(t.Context(), step.Request{RunDir: t.TempDir(), Env: map[string]string{}, Executor: executor}); err != nil {
		t.Fatal(err)
	}
	if !executor.options.TTY || executor.options.Interactions == nil || executor.options.Interactions.Len() != 2 || executor.options.Interact || executor.options.Stdin != nil {
		t.Fatalf("process options = %#v", executor.options)
	}
}

func TestShellInteractionHandoffRequiresInteractiveTerminal(t *testing.T) {
	runner, err := New(map[string]any{
		"command": "sh", "tty": true, "interactions": []any{map[string]any{"send": "first"}}, "interact": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(t.Context(), step.Request{RunDir: t.TempDir(), Env: map[string]string{}}); err == nil || !strings.Contains(err.Error(), "interactive terminal") {
		t.Fatalf("Run() error = %v", err)
	}

	executor := &recordingExecutor{}
	terminalInput := strings.NewReader("terminal")
	if _, err := runner.Run(t.Context(), step.Request{
		RunDir: t.TempDir(), Env: map[string]string{}, Interactive: true, Stdin: terminalInput, Executor: executor,
	}); err != nil {
		t.Fatal(err)
	}
	if !executor.options.Interact || executor.options.Stdin != terminalInput {
		t.Fatalf("process options = %#v", executor.options)
	}
}

func TestNewValidatesOutputConfiguration(t *testing.T) {
	tests := []struct {
		name string
		raw  map[string]any
		want string
	}{
		{name: "stdout", raw: map[string]any{"command": "true", "stdout": "quiet"}, want: "stdout must be"},
		{name: "stderr", raw: map[string]any{"command": "true", "stderr": "quiet"}, want: "stderr must be"},
		{name: "capture limit", raw: map[string]any{"command": "true", "capture_limit": "0B"}, want: "capture_limit must be a positive"},
		{name: "tty stdout", raw: map[string]any{"command": "true", "tty": true, "stdout": "inherit"}, want: "tty cannot be combined"},
		{name: "tty stderr", raw: map[string]any{"command": "true", "tty": true, "stderr": "discard"}, want: "tty cannot be combined"},
		{name: "tty limit", raw: map[string]any{"command": "true", "tty": true, "capture_limit": "1MiB"}, want: "tty cannot be combined"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := New(test.raw)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("New() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestNewValidatesAllowedExitCodes(t *testing.T) {
	tests := []struct {
		name string
		raw  map[string]any
		want string
	}{
		{name: "valid", raw: map[string]any{"command": "true", "allowed_exit_codes": []any{0, 1}}},
		{name: "empty", raw: map[string]any{"command": "true", "allowed_exit_codes": []any{}}, want: "at least one"},
		{name: "null", raw: map[string]any{"command": "true", "allowed_exit_codes": nil}, want: "at least one"},
		{name: "non-integer", raw: map[string]any{"command": "true", "allowed_exit_codes": []any{"1"}}, want: "cannot unmarshal"},
		{name: "negative", raw: map[string]any{"command": "true", "allowed_exit_codes": []any{-1}}, want: "0 through 255"},
		{name: "too large", raw: map[string]any{"command": "true", "allowed_exit_codes": []any{256}}, want: "0 through 255"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := New(test.raw)
			if test.want == "" && err != nil {
				t.Fatalf("New() error = %v", err)
			}
			if test.want != "" && (err == nil || !strings.Contains(err.Error(), test.want)) {
				t.Fatalf("New() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestShellAppliesAllowedExitCodes(t *testing.T) {
	tests := []struct {
		name       string
		codes      []any
		result     process.Result
		runErr     error
		wantErr    bool
		wantExit   bool
		wantOutput bool
	}{
		{
			name: "allowed non-zero", codes: []any{0, 7},
			result: process.Result{Stdout: "out", Stderr: "err", ExitCode: 7, StdoutTruncated: true, StderrTruncated: true},
			runErr: &process.ExitError{Command: "probe", Code: 7}, wantOutput: true,
		},
		{
			name: "allowed sole joined exit", codes: []any{0, 7}, result: process.Result{ExitCode: 7},
			runErr: errors.Join(&process.ExitError{Command: "probe", Code: 7}),
		},
		{
			name: "default rejects non-zero", result: process.Result{ExitCode: 7},
			runErr: &process.ExitError{Command: "probe", Code: 7}, wantErr: true, wantExit: true,
		},
		{name: "explicit list rejects zero", codes: []any{1}, result: process.Result{ExitCode: 0}, wantErr: true, wantExit: true},
		{
			name: "operational error is preserved", codes: []any{0, 7}, result: process.Result{ExitCode: 7},
			runErr: errors.New("executor failed"), wantErr: true,
		},
		{
			name: "joined error is preserved", codes: []any{0, 7}, result: process.Result{ExitCode: 7},
			runErr: errors.Join(&process.ExitError{Command: "probe", Code: 7}, errors.New("stream failed")), wantErr: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			raw := map[string]any{"command": "probe"}
			if test.codes != nil {
				raw["allowed_exit_codes"] = test.codes
			}
			runner, err := New(raw)
			if err != nil {
				t.Fatal(err)
			}
			result, err := runner.Run(t.Context(), step.Request{
				RunDir: t.TempDir(), Env: map[string]string{}, Stdout: io.Discard, Stderr: io.Discard,
				Executor: &recordingExecutor{result: test.result, err: test.runErr},
			})
			if (err != nil) != test.wantErr {
				t.Fatalf("Run() error = %v, wantErr %t", err, test.wantErr)
			}
			_, isExitError := err.(*process.ExitError)
			if isExitError != test.wantExit {
				t.Fatalf("Run() error = %v, want process exit error %t", err, test.wantExit)
			}
			if test.wantOutput && (result.Outputs["exit_code"] != 7 || result.Outputs["stdout"] != "out" || result.Outputs["stderr"] != "err" || result.Outputs["stdout_truncated"] != true || result.Outputs["stderr_truncated"] != true) {
				t.Fatalf("outputs = %#v", result.Outputs)
			}
		})
	}
}

func TestNewAcceptsTemplatedOutputConfiguration(t *testing.T) {
	_, err := New(map[string]any{
		"command": "true", "stdout": "{{ .vars.stdout }}", "stderr": "{{ .vars.stderr }}", "capture_limit": "{{ .vars.limit }}",
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
}

func TestShellPassesOutputConfigurationToExecutor(t *testing.T) {
	executor := &recordingExecutor{result: process.Result{Stdout: "1234", StdoutTruncated: true}}
	runner, err := New(map[string]any{
		"command": "generate", "stdout": "capture", "capture_limit": "4B",
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(t.Context(), step.Request{
		RunDir: t.TempDir(), Env: map[string]string{}, Stdout: io.Discard, Stderr: io.Discard, Executor: executor,
	})
	if err != nil {
		t.Fatal(err)
	}
	if executor.options.StdoutPolicy != process.OutputCapture || executor.options.StderrPolicy != process.OutputTee || executor.options.CaptureLimit != 4 {
		t.Fatalf("process options = %#v", executor.options)
	}
	if result.Outputs["stdout"] != "1234" || result.Outputs["stdout_truncated"] != true || result.Outputs["stderr_truncated"] != false {
		t.Fatalf("outputs = %#v", result.Outputs)
	}
}

func TestShellTTYRequiresInteractiveRequest(t *testing.T) {
	runner, err := New(map[string]any{"command": "sh", "tty": true})
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.Run(t.Context(), step.Request{RunDir: t.TempDir(), Env: map[string]string{}})
	if err == nil || !strings.Contains(err.Error(), "tty requires an interactive terminal") {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestShellTTYSuppliesTerminalAndBoundedCapture(t *testing.T) {
	executor := &recordingExecutor{result: process.Result{Stdout: "transcript", StdoutTruncated: true}}
	terminalInput := strings.NewReader("terminal")
	runner, err := New(map[string]any{"command": "sh", "tty": true})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(t.Context(), step.Request{
		RunDir: t.TempDir(), Env: map[string]string{}, Stdin: terminalInput,
		Stdout: io.Discard, Stderr: io.Discard, Interactive: true, Executor: executor,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !executor.options.TTY || executor.options.Stdin != terminalInput || executor.options.CaptureLimit != ttyCaptureLimit {
		t.Fatalf("process options = %#v", executor.options)
	}
	if result.Outputs["stdout"] != "transcript" || result.Outputs["stderr"] != "" || result.Outputs["stdout_truncated"] != true || result.Outputs["stderr_truncated"] != false {
		t.Fatalf("outputs = %#v", result.Outputs)
	}
}

func TestShellExportsAttemptMetadataAfterStepEnvironment(t *testing.T) {
	runner, err := New(map[string]any{
		"script": `printf '%s:%s:%s' "$WUKO_STEP_ATTEMPT" "$WUKO_STEP_MAX_ATTEMPTS" "$WUKO_STEP_OPERATION_ID"`,
		"env":    map[string]any{"WUKO_STEP_ATTEMPT": "spoofed"},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(t.Context(), step.Request{
		Attempt: 2, MaxAttempts: 4, OperationID: "release-42",
		RunDir: t.TempDir(), Env: map[string]string{}, Stdout: io.Discard, Stderr: io.Discard,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := result.Outputs["stdout"]; got != "2:4:release-42" {
		t.Fatalf("stdout = %q", got)
	}
}

func TestShellRunsAsConfiguredUser(t *testing.T) {
	current, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	runner, err := New(map[string]any{
		"script": "id -u",
		"user":   current.Username,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(t.Context(), step.Request{
		RunDir: t.TempDir(), Env: map[string]string{}, Stdout: io.Discard, Stderr: io.Discard,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(result.Outputs["stdout"].(string)); got != strconv.Itoa(os.Geteuid()) {
		t.Fatalf("effective user ID = %q, want %d", got, os.Geteuid())
	}
}

type recordingExecutor struct {
	options process.Options
	result  process.Result
	err     error
}

func (executor *recordingExecutor) Run(_ context.Context, options process.Options) (process.Result, error) {
	executor.options = options
	return executor.result, executor.err
}
