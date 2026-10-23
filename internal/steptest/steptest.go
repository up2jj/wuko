// Package steptest builds the step runners, registries and minimal workflows that tests of
// steps and the engine share.
package steptest

import (
	"context"
	"testing"

	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/workflow"
)

// RunnerFunc adapts a function to step.Runner.
type RunnerFunc func(context.Context, step.Request) (step.Result, error)

// Run calls run(ctx, request).
func (run RunnerFunc) Run(ctx context.Context, request step.Request) (step.Result, error) {
	return run(ctx, request)
}

// Build builds a runner from raw configuration and fails the test when the builder rejects it.
func Build(t testing.TB, build step.Builder, raw map[string]any) step.Runner {
	t.Helper()
	runner, err := build(raw)
	if err != nil {
		t.Fatal(err)
	}
	return runner
}

// BuildAs builds a runner like Build and returns it as the concrete type R, failing the test
// when the builder returns another type.
func BuildAs[R step.Runner](t testing.TB, build step.Builder, raw map[string]any) R {
	t.Helper()
	runner := Build(t, build, raw)
	typed, ok := runner.(R)
	if !ok {
		t.Fatalf("builder returned %T, want %T", runner, *new(R))
	}
	return typed
}

// Registry returns a registry holding builders, failing the test when one cannot be registered.
func Registry(t testing.TB, builders map[string]step.Builder) *step.Registry {
	t.Helper()
	registry := step.NewRegistry()
	for name, builder := range builders {
		if err := registry.Register(name, builder); err != nil {
			t.Fatal(err)
		}
	}
	return registry
}

// Definition returns a version-1 workflow named name that runs steps from a new temporary
// directory.
func Definition(t testing.TB, name string, steps ...workflow.Step) *workflow.Definition {
	t.Helper()
	return &workflow.Definition{Version: 1, Name: name, Dir: t.TempDir(), Steps: steps}
}
