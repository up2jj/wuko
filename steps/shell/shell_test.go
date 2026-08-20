package shell

import (
	"io"
	"os"
	"os/user"
	"strconv"
	"strings"
	"testing"

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
