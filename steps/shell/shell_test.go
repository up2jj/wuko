package shell

import (
	"context"
	"io"
	"os"
	"os/user"
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

func TestNewRejectsTTYWithConfiguredStdin(t *testing.T) {
	_, err := New(map[string]any{"command": "sh", "tty": true, "stdin": "input"})
	if err == nil || !strings.Contains(err.Error(), "tty and stdin cannot be combined") {
		t.Fatalf("New() error = %v", err)
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
