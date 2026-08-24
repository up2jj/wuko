package engine

import (
	"context"
	"testing"

	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/workflow"
)

func TestLoopRepeatsUntilExpressionMatches(t *testing.T) {
	var polls int
	registry := newTestRegistry(t, map[string]step.Builder{
		"poll": func(map[string]any) (step.Runner, error) {
			return runnerFunc(func(context.Context, step.Request) (step.Result, error) {
				polls++
				return step.Result{Outputs: map[string]any{
					"status":   map[bool]string{false: "in_progress", true: "completed"}[polls >= 2],
					"terminal": polls >= 2,
				}}, nil
			}), nil
		},
	})
	definition := testDefinition(t, "loop", workflow.Step{
		ID: "wait",
		Loop: &workflow.LoopGroup{
			Until:         "steps.poll.terminal",
			MaxIterations: 3,
			Steps:         []workflow.Step{{ID: "poll", Type: "poll", With: map[string]any{}}},
		},
	})

	state, err := New(registry).Run(t.Context(), definition, Options{RunDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if polls != 2 {
		t.Fatalf("polls = %d, want 2", polls)
	}
	if state.Steps["poll"].(map[string]any)["terminal"] != true {
		t.Fatalf("poll output = %#v", state.Steps["poll"])
	}
	loop := state.Steps["wait"].(map[string]any)
	if loop["iterations"] != 2 {
		t.Fatalf("loop output = %#v", loop)
	}
}

func TestLoopFailsAtMaximumIterations(t *testing.T) {
	registry := newTestRegistry(t, map[string]step.Builder{
		"poll": func(map[string]any) (step.Runner, error) {
			return runnerFunc(func(context.Context, step.Request) (step.Result, error) {
				return step.Result{Outputs: map[string]any{"terminal": false}}, nil
			}), nil
		},
	})
	definition := testDefinition(t, "loop", workflow.Step{
		ID: "wait",
		Loop: &workflow.LoopGroup{
			Until:         "steps.poll.terminal",
			MaxIterations: 2,
			Steps:         []workflow.Step{{ID: "poll", Type: "poll", With: map[string]any{}}},
		},
	})

	if _, err := New(registry).Run(t.Context(), definition, Options{RunDir: t.TempDir()}); err == nil {
		t.Fatal("expected max iteration error")
	}
}
