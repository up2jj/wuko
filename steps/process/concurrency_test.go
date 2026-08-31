package process

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	processpkg "github.com/up2jj/wuko/process"
	"github.com/up2jj/wuko/step"
)

type serviceGroup struct {
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	mu     sync.Mutex
	errs   []error
}

func newServiceGroup(t *testing.T) *serviceGroup {
	ctx, cancel := context.WithCancel(t.Context())
	group := &serviceGroup{ctx: ctx, cancel: cancel}
	t.Cleanup(func() { cancel(); group.wait() })
	return group
}

func (group *serviceGroup) StartService(_ string, _ string, _ step.ServiceOptions, run func(context.Context) error) error {
	group.wg.Add(1)
	go func() {
		defer group.wg.Done()
		if err := run(group.ctx); err != nil && !errors.Is(err, context.Canceled) {
			group.mu.Lock()
			group.errs = append(group.errs, err)
			group.mu.Unlock()
		}
	}()
	return nil
}

func (group *serviceGroup) wait() []error {
	group.wg.Wait()
	group.mu.Lock()
	defer group.mu.Unlock()
	return append([]error(nil), group.errs...)
}

type lockedBuffer struct {
	mu sync.Mutex
	bytes.Buffer
}

func (buffer *lockedBuffer) Write(data []byte) (int, error) {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.Buffer.Write(data)
}
func (buffer *lockedBuffer) String() string {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.Buffer.String()
}

type streamingExecutor struct{ starts atomic.Int64 }

func (executor *streamingExecutor) Run(ctx context.Context, options processpkg.Options) (processpkg.Result, error) {
	executor.starts.Add(1)
	options.Started()
	var writers sync.WaitGroup
	writers.Add(2)
	go func() {
		defer writers.Done()
		_, _ = io.WriteString(options.Stdout, "boot ")
		_, _ = io.WriteString(options.Stdout, "ready\n")
	}()
	go func() { defer writers.Done(); _, _ = io.WriteString(options.Stderr, "diagnostic\n") }()
	writers.Wait()
	<-ctx.Done()
	return processpkg.Result{}, ctx.Err()
}

func TestConcurrentReplicaReadinessOutputAndShutdown(t *testing.T) {
	const replicas = 64
	runner, err := New(map[string]any{
		"command": "worker", "readiness": map[string]any{"log": map[string]any{"pattern": "ready", "timeout": "2s"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	group := newServiceGroup(t)
	executor := &streamingExecutor{}
	output := &lockedBuffer{}
	errorsByReplica := make(chan error, replicas)
	var replicasReady sync.WaitGroup
	replicasReady.Add(replicas)
	for index := range replicas {
		go func() {
			defer replicasReady.Done()
			_, runErr := runner.Run(t.Context(), step.Request{
				StepID: fmt.Sprintf("worker-%02d", index), Services: group, Executor: executor,
				Env: map[string]string{}, Stdout: output, Stderr: output,
			})
			errorsByReplica <- runErr
		}()
	}
	replicasReady.Wait()
	close(errorsByReplica)
	for runErr := range errorsByReplica {
		if runErr != nil {
			t.Fatalf("replica readiness: %v", runErr)
		}
	}
	if got := executor.starts.Load(); got != replicas {
		t.Fatalf("starts = %d, want %d", got, replicas)
	}
	group.cancel()
	if errs := group.wait(); len(errs) != 0 {
		t.Fatalf("shutdown errors = %v", errs)
	}
	text := output.String()
	for index := range replicas {
		label := fmt.Sprintf("[worker-%02d] ", index)
		if !strings.Contains(text, label+"boot ready\n") || !strings.Contains(text, label+"diagnostic\n") {
			t.Fatalf("missing complete prefixed streams for %s", label)
		}
	}
}

type immediateExitExecutor struct {
	starts   atomic.Int64
	restarts int64
}

func (executor *immediateExitExecutor) Run(_ context.Context, options processpkg.Options) (processpkg.Result, error) {
	executor.starts.Add(1)
	options.Started()
	result := processpkg.Result{ExitCode: 7}
	return result, &processpkg.ExitError{Command: options.Command, Code: 7}
}

func TestSpawnAndImmediateExitOrderingIsDeterministic(t *testing.T) {
	const iterations = 256
	runner, err := New(map[string]any{"command": "short-lived"})
	if err != nil {
		t.Fatal(err)
	}
	group := newServiceGroup(t)
	executor := &immediateExitExecutor{}
	for index := range iterations {
		if _, err := runner.Run(t.Context(), step.Request{StepID: fmt.Sprintf("short-%d", index), Services: group, Executor: executor, Env: map[string]string{}, Stdout: io.Discard, Stderr: io.Discard}); err != nil {
			t.Fatalf("iteration %d treated an established spawn as startup failure: %v", index, err)
		}
	}
	if errs := group.wait(); len(errs) != iterations {
		t.Fatalf("terminal errors = %d, want %d", len(errs), iterations)
	}
}

func TestConcurrentRestartBudgetsAreIsolated(t *testing.T) {
	const replicas = 32
	runner, err := New(map[string]any{
		"command": "unstable", "restart": map[string]any{"policy": "on_failure", "backoff": "1ms", "max_restarts": 3},
	})
	if err != nil {
		t.Fatal(err)
	}
	group := newServiceGroup(t)
	executors := make([]*immediateExitExecutor, replicas)
	var ready sync.WaitGroup
	ready.Add(replicas)
	errCh := make(chan error, replicas)
	for index := range replicas {
		executors[index] = &immediateExitExecutor{}
		go func() {
			defer ready.Done()
			_, runErr := runner.Run(t.Context(), step.Request{StepID: fmt.Sprintf("restart-%d", index), Services: group, Executor: executors[index], Env: map[string]string{}, Stdout: io.Discard, Stderr: io.Discard})
			errCh <- runErr
		}()
	}
	ready.Wait()
	close(errCh)
	for runErr := range errCh {
		if runErr != nil {
			t.Fatal(runErr)
		}
	}
	if errs := group.wait(); len(errs) != replicas {
		t.Fatalf("terminal errors = %d, want %d", len(errs), replicas)
	}
	for index, executor := range executors {
		if got := executor.starts.Load(); got != 4 {
			t.Fatalf("replica %d starts = %d, want 4", index, got)
		}
	}
}
