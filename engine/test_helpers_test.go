package engine

import (
	"context"
	"testing"

	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/workflow"
)

type runnerFunc func(context.Context, step.Request) (step.Result, error)

func (run runnerFunc) Run(ctx context.Context, request step.Request) (step.Result, error) {
	return run(ctx, request)
}

func testDefinition(t *testing.T, name string, steps ...workflow.Step) *workflow.Definition {
	t.Helper()
	return &workflow.Definition{
		Version: 1,
		Name:    name,
		Dir:     t.TempDir(),
		Steps:   steps,
	}
}

func testAction(t *testing.T, name string, steps ...workflow.Step) *workflow.Action {
	t.Helper()
	return &workflow.Action{
		Version: 1,
		Name:    name,
		Dir:     t.TempDir(),
		Steps:   steps,
	}
}

func newTestRegistry(t *testing.T, builders map[string]step.Builder) *step.Registry {
	t.Helper()
	registry := step.NewRegistry()
	for name, builder := range builders {
		if err := registry.Register(name, builder); err != nil {
			t.Fatal(err)
		}
	}
	return registry
}

func immediateRetry(maximum int) *workflow.RetryPolicy {
	return &workflow.RetryPolicy{MaxAttempts: maximum, BackoffMultiplier: 1}
}
