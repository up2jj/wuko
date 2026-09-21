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

func TestNewValidatesAllowedExitCodes(t *testing.T) {
	tests := []struct {
		name string
		raw  map[string]any
		want string
	}{
		{name: "valid", raw: map[string]any{"command": "true", "allowed_exit_codes": []any{0, 1}}},
		{name: "any", raw: map[string]any{"command": "true", "allowed_exit_codes": "any"}},
		{name: "empty", raw: map[string]any{"command": "true", "allowed_exit_codes": []any{}}, want: "non-empty list"},
		{name: "null", raw: map[string]any{"command": "true", "allowed_exit_codes": nil}, want: "non-empty list"},
		{name: "unsupported string", raw: map[string]any{"command": "true", "allowed_exit_codes": "ANY"}, want: "non-empty list"},
		{name: "scalar number", raw: map[string]any{"command": "true", "allowed_exit_codes": 0}, want: "non-empty list"},
		{name: "boolean", raw: map[string]any{"command": "true", "allowed_exit_codes": false}, want: "non-empty list"},
		{name: "mapping", raw: map[string]any{"command": "true", "allowed_exit_codes": map[string]any{"any": true}}, want: "non-empty list"},
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

func TestProcessAppliesAllowedExitCodes(t *testing.T) {
	tests := []struct {
		name    string
		allowed any
		exit    processExit
		wantErr bool
	}{
		{name: "default accepts zero", exit: processExit{result: processpkg.Result{ExitCode: 0}}},
		{
			name: "default rejects non-zero", exit: processExit{result: processpkg.Result{ExitCode: 7},
				err: &processpkg.ExitError{Command: "worker", Code: 7}}, wantErr: true,
		},
		{
			name: "list accepts a configured code", allowed: []any{0, 7},
			exit: processExit{result: processpkg.Result{ExitCode: 7}, err: &processpkg.ExitError{Command: "worker", Code: 7}},
		},
		{
			name: "list rejects an unconfigured code", allowed: []any{0, 7},
			exit:    processExit{result: processpkg.Result{ExitCode: 8}, err: &processpkg.ExitError{Command: "worker", Code: 8}},
			wantErr: true,
		},
		{name: "any accepts zero", allowed: "any", exit: processExit{result: processpkg.Result{ExitCode: 0}}},
		{
			name: "any accepts a non-zero status", allowed: "any",
			exit: processExit{result: processpkg.Result{ExitCode: 7}, err: &processpkg.ExitError{Command: "worker", Code: 7}},
		},
		{
			name: "any accepts the highest status", allowed: "any",
			exit: processExit{result: processpkg.Result{ExitCode: 255}, err: &processpkg.ExitError{Command: "worker", Code: 255}},
		},
		{
			name: "any rejects signal termination", allowed: "any",
			exit:    processExit{result: processpkg.Result{ExitCode: -1}, err: &processpkg.ExitError{Command: "worker", Code: -1}},
			wantErr: true,
		},
		{
			name: "any preserves an operational error", allowed: "any",
			exit:    processExit{result: processpkg.Result{ExitCode: 7}, err: errors.New("executor failed")},
			wantErr: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			raw := map[string]any{"command": "worker"}
			if test.allowed != nil {
				raw["allowed_exit_codes"] = test.allowed
			}
			if err := mustRunner(t, raw).exitError(test.exit); (err != nil) != test.wantErr {
				t.Fatalf("exitError() = %v, wantErr %t", err, test.wantErr)
			}
		})
	}
}

func TestProcessAnyExitCodeSuppressesOnFailureRestart(t *testing.T) {
	runner := mustRunner(t, map[string]any{
		"script": "exit 7", "allowed_exit_codes": "any",
		"restart": map[string]any{"policy": "on_failure", "max_restarts": 3},
	})
	if runner.shouldRestart(runner.exitError(processExit{
		result: processpkg.Result{ExitCode: 7}, err: &processpkg.ExitError{Command: "worker", Code: 7},
	}), 0) {
		t.Fatal("an allowed exit restarted an on_failure service")
	}
	// A signal-terminated service is still a failure, so on_failure keeps replacing it.
	if !runner.shouldRestart(runner.exitError(processExit{
		result: processpkg.Result{ExitCode: -1}, err: &processpkg.ExitError{Command: "worker", Code: -1},
	}), 0) {
		t.Fatal("a signal-terminated service did not restart")
	}
}
