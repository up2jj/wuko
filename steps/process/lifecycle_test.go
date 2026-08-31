package process

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	processpkg "github.com/up2jj/wuko/process"
	"github.com/up2jj/wuko/step"
)

func mustRunner(t *testing.T, raw map[string]any) *Runner {
	t.Helper()
	runner, err := New(raw)
	if err != nil {
		t.Fatal(err)
	}
	return runner.(*Runner)
}

// nonStoppingExecutor stands in for an executor whose cancellation cannot reach the process it
// started, as a Docker exec cannot.
type nonStoppingExecutor struct{}

func (nonStoppingExecutor) Run(ctx context.Context, options processpkg.Options) (processpkg.Result, error) {
	return processpkg.LocalExecutor{}.Run(ctx, options)
}

func (nonStoppingExecutor) CancelStopsProcess() bool { return false }

func TestProcessFailedStartupIsReportedByTheStepAndNotTheScope(t *testing.T) {
	runner := mustRunner(t, map[string]any{
		"script":      "while :; do sleep 1; done",
		"readiness":   map[string]any{"log": map[string]any{"pattern": "never matches", "timeout": "100ms"}},
		"exit_on_end": true, "exit_on_failure": true,
		"shutdown": map[string]any{"timeout": "100ms"},
	})
	services := newTestServices(t)
	_, err := runner.Run(t.Context(), step.Request{StepID: "api", Services: services, Env: map[string]string{}, Stdout: io.Discard, Stderr: io.Discard})
	if err == nil || !strings.Contains(err.Error(), "readiness log did not match") {
		t.Fatalf("step error = %v, want the readiness failure", err)
	}
	select {
	case <-services.done:
	case <-time.After(3 * time.Second):
		t.Fatal("service did not stop after its startup failed")
	}
	services.mu.Lock()
	serviceErr := services.err
	services.mu.Unlock()
	if !errors.Is(serviceErr, step.ErrServiceAborted) {
		t.Fatalf("service error = %v, want an aborted service", serviceErr)
	}
}

func TestProcessAbandonedAfterReadinessStopsTheService(t *testing.T) {
	runner := mustRunner(t, map[string]any{
		"command": "sh", "args": []any{"-c", "while :; do sleep 1; done"},
		"shutdown": map[string]any{"timeout": "100ms"},
	})
	ready := make(chan error, 1)
	committed := make(chan struct{})
	abandoned := make(chan struct{})
	stopped := make(chan error, 1)
	request := step.Request{StepID: "api", Env: map[string]string{}, Stdout: io.Discard, Stderr: io.Discard}
	go func() {
		stopped <- runner.runLifecycle(t.Context(), t.Context(), request, "api", ready, committed, abandoned)
	}()
	select {
	case err := <-ready:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("service never became ready")
	}
	close(abandoned)
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatalf("abandoned service error = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("abandoned service was never released")
	}
}

func TestProcessLivenessFailureRunsShutdownCommandBeforeEachRestart(t *testing.T) {
	directory := t.TempDir()
	stops := filepath.Join(directory, "stops")
	runner := mustRunner(t, map[string]any{
		"script":   "trap 'exit 0' TERM INT; while :; do sleep 1; done",
		"liveness": map[string]any{"exec": map[string]any{"command": "false", "period": "10ms", "timeout": "1s", "failure_threshold": 1}},
		"restart":  map[string]any{"policy": "on_failure", "backoff": "1ms", "max_restarts": 1},
		"shutdown": map[string]any{"timeout": "500ms", "command": map[string]any{"script": "printf x >> \"$1\"", "args": []any{stops}}},
	})
	services := newTestServices(t)
	if _, err := runner.Run(t.Context(), step.Request{StepID: "worker", Services: services, RunDir: directory, Env: map[string]string{}, Stdout: io.Discard, Stderr: io.Discard}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-services.done:
	case <-time.After(5 * time.Second):
		t.Fatal("restart budget was not exhausted")
	}
	data, err := os.ReadFile(stops)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(data); got != "xx" {
		t.Fatalf("shutdown command runs = %q, want one before the restart and one after it", got)
	}
}

func TestProcessRestartRequiresShutdownCommandWhenCancellationCannotStop(t *testing.T) {
	runner := mustRunner(t, map[string]any{
		"command": "sh", "args": []any{"-c", "while :; do sleep 1; done"},
		"restart": map[string]any{"policy": "always"},
	})
	services := newTestServices(t)
	close(services.done)
	_, err := runner.Run(t.Context(), step.Request{StepID: "worker", Services: services, Executor: nonStoppingExecutor{},
		Env: map[string]string{}, Stdout: io.Discard, Stderr: io.Discard})
	if err == nil || !strings.Contains(err.Error(), "requires shutdown.command") {
		t.Fatalf("error = %v, want a rejected restart policy", err)
	}
}
