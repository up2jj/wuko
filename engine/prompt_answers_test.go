package engine

import (
	"context"
	"io"
	"sync"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/up2jj/wuko/internal/steptest"
	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/workflow"
)

type interactivityObservation struct {
	Rendered    any
	Interactive bool
	RunValue    bool
	Prompts     step.PromptAnswers
}

func TestRunExposesInteractivityAndPromptAnswers(t *testing.T) {
	t.Parallel()
	var mutex sync.Mutex
	observed := map[string]interactivityObservation{}
	registry := steptest.Registry(t, map[string]step.Builder{"observe_run": func(raw map[string]any) (step.Runner, error) {
		return steptest.RunnerFunc(func(_ context.Context, request step.Request) (step.Result, error) {
			mutex.Lock()
			defer mutex.Unlock()
			observed[request.StepID] = interactivityObservation{
				Rendered: raw["value"], Interactive: request.Interactive,
				RunValue: request.RunValue().Interactive, Prompts: request.Prompts,
			}
			return step.Result{}, nil
		}), nil
	}})
	observe := func(id string, condition workflow.Condition) workflow.Step {
		return workflow.Step{ID: id, Type: "observe_run", If: condition, With: map[string]any{"value": "{{ .run.interactive }}"}}
	}
	for _, interactive := range []bool{true, false} {
		definition := steptest.Definition(t, "interactivity",
			observe("when_interactive", "run.interactive"),
			observe("when_unattended", "!run.interactive"),
			workflow.Step{Concurrent: &workflow.ConcurrentGroup{MaxConcurrency: 2, Steps: []workflow.Step{observe("branch", ""), observe("sibling", "")}}},
		)
		prompts := step.PromptAnswers{Yes: true, Defaults: interactive}
		clear(observed)
		_, err := New(registry).Run(t.Context(), definition, Options{
			RunDir: t.TempDir(), Stdout: io.Discard, Stderr: io.Discard, Interactive: interactive, Prompts: prompts,
		})
		if err != nil {
			t.Fatal(err)
		}
		active := "when_unattended"
		if interactive {
			active = "when_interactive"
		}
		want := map[string]interactivityObservation{
			active: {Rendered: map[bool]string{true: "true", false: "false"}[interactive], Interactive: interactive, RunValue: interactive, Prompts: prompts},
			// Concurrent branches cannot read the terminal, so they report the run as unattended.
			"branch":  {Rendered: "false", Prompts: prompts},
			"sibling": {Rendered: "false", Prompts: prompts},
		}
		if diff := cmp.Diff(want, observed); diff != "" {
			t.Fatalf("interactive=%t observations (-want +got):\n%s", interactive, diff)
		}
	}
}
