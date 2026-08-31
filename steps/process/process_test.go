package process

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/up2jj/wuko/step"
)

type testServices struct {
	ctx     context.Context
	cancel  context.CancelFunc
	options step.ServiceOptions
	done    chan struct{}
	err     error
	mu      sync.Mutex
}

func newTestServices(t *testing.T) *testServices {
	ctx, cancel := context.WithCancel(t.Context())
	services := &testServices{ctx: ctx, cancel: cancel, done: make(chan struct{})}
	t.Cleanup(func() {
		cancel()
		select {
		case <-services.done:
		case <-time.After(3 * time.Second):
			t.Error("service did not stop")
		}
	})
	return services
}

func (services *testServices) StartService(_ string, _ string, options step.ServiceOptions, run func(context.Context) error) error {
	services.options = options
	go func() {
		services.mu.Lock()
		services.err = run(services.ctx)
		services.mu.Unlock()
		close(services.done)
	}()
	return nil
}

func TestProcessBecomesReadyOnSpawnAndStopsWithScope(t *testing.T) {
	runner, err := New(map[string]any{"command": "sh", "args": []any{"-c", "while :; do sleep 1; done"}, "shutdown": map[string]any{"timeout": "100ms"}})
	if err != nil {
		t.Fatal(err)
	}
	services := newTestServices(t)
	var output bytes.Buffer
	result, err := runner.Run(t.Context(), step.Request{StepID: "api", Services: services, Env: map[string]string{}, Stdout: &output, Stderr: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outputs["ready"] != true || result.Outputs["readiness"] != "spawn" {
		t.Fatalf("outputs = %#v", result.Outputs)
	}
	if services.options.KeepAlive {
		t.Fatal("keep_alive should default to false")
	}
}

func TestProcessWaitsForReadinessLogAndPrefixesOutput(t *testing.T) {
	runner, err := New(map[string]any{
		"script":    "printf 'booting\\n'; sleep 0.05; printf 'listening on 8080\\n'; while :; do sleep 1; done",
		"readiness": map[string]any{"log": map[string]any{"pattern": "listening on [0-9]+", "timeout": "2s"}},
		"shutdown":  map[string]any{"timeout": "100ms"},
	})
	if err != nil {
		t.Fatal(err)
	}
	services := newTestServices(t)
	var output bytes.Buffer
	result, err := runner.Run(t.Context(), step.Request{StepID: "web", Services: services, Env: map[string]string{}, Stdout: &output, Stderr: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outputs["readiness"] != "log" {
		t.Fatalf("outputs = %#v", result.Outputs)
	}
	services.cancel()
	select {
	case <-services.done:
	case <-time.After(3 * time.Second):
		t.Fatal("service did not stop")
	}
	if got := output.String(); !strings.Contains(got, "[web] booting\n[web] listening on 8080\n") {
		t.Fatalf("output = %q", got)
	}
}

func TestProcessReportsUnexpectedExitAfterReadiness(t *testing.T) {
	runner, err := New(map[string]any{"command": "sh", "args": []any{"-c", "sleep 0.05; exit 7"}, "exit_on_failure": true})
	if err != nil {
		t.Fatal(err)
	}
	services := newTestServices(t)
	if _, err := runner.Run(t.Context(), step.Request{StepID: "worker", Services: services, Env: map[string]string{}, Stdout: io.Discard, Stderr: io.Discard}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-services.done:
		services.mu.Lock()
		err := services.err
		services.mu.Unlock()
		var exitErr interface{ Error() string }
		if err == nil || !errors.As(err, &exitErr) || !strings.Contains(err.Error(), "status 7") {
			t.Fatalf("service error = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("service did not report its exit")
	}
}

func TestProcessRestartsUntilBudgetIsExhausted(t *testing.T) {
	directory := t.TempDir()
	attempts := directory + "/attempts"
	runner, err := New(map[string]any{
		"script": "printf x >> \"$1\"; sleep 0.03; exit 7", "args": []any{attempts},
		"restart": map[string]any{"policy": "on_failure", "backoff": "1ms", "max_restarts": 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	services := newTestServices(t)
	if _, err := runner.Run(t.Context(), step.Request{StepID: "worker", Services: services, RunDir: directory, Env: map[string]string{}, Stdout: io.Discard, Stderr: io.Discard}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-services.done:
	case <-time.After(3 * time.Second):
		t.Fatal("restart budget was not exhausted")
	}
	data, err := os.ReadFile(attempts)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(data); got != "xxx" {
		t.Fatalf("attempt markers = %q, want three launches", got)
	}
}

func TestProcessConfigurationValidation(t *testing.T) {
	tests := []struct {
		name string
		raw  map[string]any
		want string
	}{
		{name: "command and script", raw: map[string]any{"command": "true", "script": "true"}, want: "exactly one"},
		{name: "multiple readiness probes", raw: map[string]any{"command": "true", "readiness": map[string]any{"log": map[string]any{"pattern": "ok"}, "exec": map[string]any{"command": "true"}}}, want: "exactly one"},
		{name: "detached without shutdown", raw: map[string]any{"command": "true", "detached": true}, want: "shutdown.command"},
		{name: "capture stream", raw: map[string]any{"command": "true", "stdout": "capture"}, want: "inherit or discard"},
		{name: "log readiness without a stream", raw: map[string]any{"command": "true", "stdout": "discard", "stderr": "discard",
			"readiness": map[string]any{"log": map[string]any{"pattern": "ready"}}}, want: "readiness log requires"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := New(test.raw)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestLinePrefixWriterHandlesSplitLines(t *testing.T) {
	var output bytes.Buffer
	writer := prefixedWriter(&output, "pool-2", nil)
	var wait sync.WaitGroup
	wait.Add(1)
	go func() { defer wait.Done(); _, _ = writer.Write([]byte("one\ntw")); _, _ = writer.Write([]byte("o\n")) }()
	wait.Wait()
	if got := output.String(); got != "[pool-2] one\n[pool-2] two\n" {
		t.Fatalf("output = %q", got)
	}
}
