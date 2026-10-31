package engine

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/up2jj/wuko/internal/steptest"
	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/workflow"
)

func TestLoopRepeatsUntilExpressionMatches(t *testing.T) {
	t.Parallel()
	var polls int
	registry := steptest.Registry(t, map[string]step.Builder{
		"poll": func(map[string]any) (step.Runner, error) {
			return steptest.RunnerFunc(func(context.Context, step.Request) (step.Result, error) {
				polls++
				return step.Result{Outputs: map[string]any{
					"status":   map[bool]string{false: "in_progress", true: "completed"}[polls >= 2],
					"terminal": polls >= 2,
				}}, nil
			}), nil
		},
	})
	definition := steptest.Definition(t, "loop", workflow.Step{
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
	t.Parallel()
	registry := steptest.Registry(t, map[string]step.Builder{
		"poll": func(map[string]any) (step.Runner, error) {
			return steptest.RunnerFunc(func(context.Context, step.Request) (step.Result, error) {
				return step.Result{Outputs: map[string]any{"terminal": false}}, nil
			}), nil
		},
	})
	definition := steptest.Definition(t, "loop", workflow.Step{
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

func TestLoopFinishedEventCountsOnlySucceededIterations(t *testing.T) {
	t.Parallel()
	runs := 0
	registry := steptest.Registry(t, map[string]step.Builder{
		"poll": func(map[string]any) (step.Runner, error) {
			return steptest.RunnerFunc(func(context.Context, step.Request) (step.Result, error) {
				runs++
				if runs == 3 {
					return step.Result{}, errors.New("probe failed")
				}
				return step.Result{Outputs: map[string]any{"terminal": false}}, nil
			}), nil
		},
	})
	definition := steptest.Definition(t, "loop", workflow.Step{
		ID: "wait",
		Loop: &workflow.LoopGroup{
			Until:         "steps.poll.terminal",
			MaxIterations: 5,
			Steps:         []workflow.Step{{ID: "poll", Type: "poll", With: map[string]any{}}},
		},
	})
	var finished *ProgressEvent
	_, err := New(registry).Run(t.Context(), definition, Options{RunDir: t.TempDir(), Progress: func(event ProgressEvent) {
		if event.Kind == ControlFinished && event.ControlKind == "loop" {
			finished = &event
		}
	}})
	if err == nil {
		t.Fatal("expected the failing iteration to fail the loop")
	}
	if finished == nil || finished.Started != 3 || finished.Succeeded != 2 {
		t.Fatalf("finished event = %#v, want 3 started and 2 succeeded", finished)
	}
}

func TestLoopValidationCompilesDelayExpression(t *testing.T) {
	t.Parallel()
	registry := steptest.Registry(t, map[string]step.Builder{"never": func(map[string]any) (step.Runner, error) {
		return steptest.RunnerFunc(func(context.Context, step.Request) (step.Result, error) {
			t.Fatal("runner executed after loop delay validation failure")
			return step.Result{}, nil
		}), nil
	}})
	definition := steptest.Definition(t, "loop", workflow.Step{ID: "wait", Loop: &workflow.LoopGroup{
		Until: "true", MaxIterations: 1, Delay: workflow.LoopDelay{Expression: "vars.delay +"},
		Steps: []workflow.Step{{ID: "poll", Type: "never", With: map[string]any{}}},
	}})
	if _, err := New(registry).Run(t.Context(), definition, Options{}); err == nil || !strings.Contains(err.Error(), "compiling loop delay") {
		t.Fatalf("error = %v", err)
	}
}
