//go:build darwin || linux

package process

import (
	"context"
	"errors"
	"io"
	"testing"
)

func TestRunCapturesOutputAndExitStatus(t *testing.T) {
	result, err := Run(t.Context(), Options{
		Command: "sh", Args: []string{"-c", "printf out; printf err >&2; exit 7"},
		Env: map[string]string{}, Stdout: io.Discard, Stderr: io.Discard,
	})
	var exitErr *ExitError
	if !errors.As(err, &exitErr) || exitErr.Code != 7 {
		t.Fatalf("error = %v", err)
	}
	if result.Stdout != "out" || result.Stderr != "err" || result.ExitCode != 7 {
		t.Fatalf("result = %#v", result)
	}
}

func TestRunHonorsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := Run(ctx, Options{Command: "sh", Args: []string{"-c", "exit 0"}, Env: map[string]string{}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
}
