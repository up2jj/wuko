package steptest

import (
	"context"
	"errors"
	"testing"

	"github.com/up2jj/wuko/step"
)

type namedRunner struct{ name string }

func (runner *namedRunner) Run(context.Context, step.Request) (step.Result, error) {
	return step.Result{Outputs: map[string]any{"name": runner.name}}, nil
}

func newNamedRunner(raw map[string]any) (step.Runner, error) {
	name, _ := raw["name"].(string)
	if name == "" {
		return nil, errors.New("name is required")
	}
	return &namedRunner{name: name}, nil
}

func TestBuildAsReturnsConcreteRunner(t *testing.T) {
	t.Parallel()
	runner := BuildAs[*namedRunner](t, newNamedRunner, map[string]any{"name": "first"})
	if runner.name != "first" {
		t.Fatalf("name = %q, want first", runner.name)
	}
}

func TestRegistryResolvesRegisteredBuilders(t *testing.T) {
	t.Parallel()
	registry := Registry(t, map[string]step.Builder{
		"echo": func(map[string]any) (step.Runner, error) {
			return RunnerFunc(func(_ context.Context, request step.Request) (step.Result, error) {
				return step.Result{Outputs: map[string]any{"id": request.StepID}}, nil
			}), nil
		},
	})
	if names := registry.Names(); len(names) != 1 || names[0] != "echo" {
		t.Fatalf("names = %v, want [echo]", names)
	}
}

func TestDefinitionRunsFromTemporaryDirectory(t *testing.T) {
	t.Parallel()
	definition := Definition(t, "demo")
	if definition.Version != 1 || definition.Name != "demo" || definition.Dir == "" {
		t.Fatalf("definition = %#v", definition)
	}
}
