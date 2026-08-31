package engine

import (
	"context"
	"fmt"
	"io"
	"sync/atomic"
	"testing"

	"github.com/up2jj/wuko/executor"
	processpkg "github.com/up2jj/wuko/process"
	"github.com/up2jj/wuko/step"
	processstep "github.com/up2jj/wuko/steps/process"
	"github.com/up2jj/wuko/steps/shell"
	"github.com/up2jj/wuko/workflow"
)

type concurrentProcessSession struct {
	active atomic.Int64
	closed atomic.Bool
	want   int64
}

func (session *concurrentProcessSession) Run(ctx context.Context, options processpkg.Options) (processpkg.Result, error) {
	switch options.Command {
	case "service":
		session.active.Add(1)
		options.Started()
		<-ctx.Done()
		session.active.Add(-1)
		return processpkg.Result{}, ctx.Err()
	case "check-active":
		if got := session.active.Load(); got != session.want {
			return processpkg.Result{}, fmt.Errorf("active services = %d, want %d", got, session.want)
		}
	case "check-stopped":
		if got := session.active.Load(); got != 0 {
			return processpkg.Result{}, fmt.Errorf("services still active before executor finally: %d", got)
		}
	default:
		return processpkg.Result{}, fmt.Errorf("unexpected command %q", options.Command)
	}
	return processpkg.Result{}, nil
}

func (session *concurrentProcessSession) Close(context.Context) error {
	if got := session.active.Load(); got != 0 {
		return fmt.Errorf("closing session with %d active services", got)
	}
	session.closed.Store(true)
	return nil
}

type concurrentProcessProvider struct{ session *concurrentProcessSession }

func (provider concurrentProcessProvider) Open(context.Context, executor.Request) (executor.Session, error) {
	return provider.session, nil
}

func TestExecutorSerialReplicaStartupKeepsPoolAliveAndJoinsBeforeFinally(t *testing.T) {
	const replicas = 32
	steps := step.NewRegistry()
	if err := processstep.Register(steps); err != nil {
		t.Fatal(err)
	}
	if err := shell.Register(steps); err != nil {
		t.Fatal(err)
	}
	session := &concurrentProcessSession{want: replicas}
	executors := executor.NewRegistry()
	if err := executors.Register("concurrent-process", func(map[string]any) (executor.Provider, error) {
		return concurrentProcessProvider{session: session}, nil
	}); err != nil {
		t.Fatal(err)
	}
	items := make([]any, replicas)
	for index := range items {
		items[index] = index
	}
	block := workflow.Step{
		Executor: &workflow.ExecutorScope{Type: "concurrent-process", With: map[string]any{}},
		Steps: []workflow.Step{
			{
				ID: "pool",
				Foreach: &workflow.ForeachGroup{
					Items: "vars.replicas", MaxConcurrency: 1, MaxIterations: 100, FailFast: true,
					Steps: []workflow.Step{{ID: "worker", Type: "process", With: map[string]any{"command": "service"}}},
				},
			},
			{ID: "check", Type: "shell", With: map[string]any{"command": "check-active"}},
		},
		Finally: []workflow.Step{{ID: "check_shutdown", Type: "shell", With: map[string]any{"command": "check-stopped"}}},
	}
	definition := &workflow.Definition{Version: 1, Name: "executor-process-pool", Vars: map[string]any{"replicas": items}, Steps: []workflow.Step{block}}
	if _, err := New(steps, WithExecutors(executors)).Run(t.Context(), definition, Options{RunDir: t.TempDir(), Stdout: io.Discard, Stderr: io.Discard}); err != nil {
		t.Fatal(err)
	}
	if !session.closed.Load() {
		t.Fatal("executor session was not closed")
	}
}
