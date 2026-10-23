package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/up2jj/wuko/internal/steptest"
	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/workflow"
)

func TestPanickingStepFailsStepAndRunsFinally(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		step workflow.Step
	}{
		{name: "sequential", step: workflow.Step{ID: "boom", Type: "panic", With: map[string]any{}}},
		{name: "concurrent", step: workflow.Step{Concurrent: &workflow.ConcurrentGroup{
			MaxConcurrency: 2,
			Steps: []workflow.Step{
				{ID: "boom", Type: "panic", With: map[string]any{}},
				{ID: "fine", Type: "record", With: map[string]any{}},
			},
		}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			finallyRan := false
			registry := steptest.Registry(t, map[string]step.Builder{
				"panic": func(map[string]any) (step.Runner, error) {
					return steptest.RunnerFunc(func(context.Context, step.Request) (step.Result, error) {
						panic("runner exploded")
					}), nil
				},
				"record": func(map[string]any) (step.Runner, error) {
					return steptest.RunnerFunc(func(context.Context, step.Request) (step.Result, error) {
						return step.Result{}, nil
					}), nil
				},
				"finally": func(map[string]any) (step.Runner, error) {
					return steptest.RunnerFunc(func(context.Context, step.Request) (step.Result, error) {
						finallyRan = true
						return step.Result{}, nil
					}), nil
				},
			})
			definition := steptest.Definition(t, "panic", test.step)
			definition.Finally = []workflow.Step{{ID: "cleanup", Type: "finally", With: map[string]any{}}}

			_, err := New(registry).Run(t.Context(), definition, Options{})
			if err == nil || !strings.Contains(err.Error(), "panic: runner exploded") {
				t.Fatalf("error = %v, want contained panic", err)
			}
			if !finallyRan {
				t.Fatal("finally did not run after a panicking step")
			}
		})
	}
}
