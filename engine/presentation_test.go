package engine

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sync"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/up2jj/wuko/correlation"
	"github.com/up2jj/wuko/internal/steptest"
	"github.com/up2jj/wuko/step"
	runworkflowstep "github.com/up2jj/wuko/steps/runworkflow"
	"github.com/up2jj/wuko/useroutput"
	"github.com/up2jj/wuko/workflow"
)

func TestPresentationInheritanceAcrossActionsChildWorkflowsAndCleanup(t *testing.T) {
	t.Parallel()
	registry := steptest.Registry(t, map[string]step.Builder{"capture": func(map[string]any) (step.Runner, error) {
		return steptest.RunnerFunc(func(_ context.Context, request step.Request) (step.Result, error) {
			request.PrintMessage(useroutput.Message{Text: request.StepID, Level: useroutput.Info})
			return step.Result{}, nil
		}), nil
	}})
	if err := runworkflowstep.Register(registry); err != nil {
		t.Fatal(err)
	}
	quiet, visible := true, false
	child := steptest.Definition(t, "child", workflow.Step{ID: "child_hidden", Type: "capture"}, workflow.Step{ID: "child_visible", Type: "capture", Quiet: &visible})
	child.Reporter = workflow.ReporterConfig{Quiet: []string{"capture"}}
	host := &nestedWorkflowHost{registry: registry, definition: child}
	action := testAction(t, "action", workflow.Step{ID: "action_hidden", Type: "capture"}, workflow.Step{ID: "action_visible", Type: "capture", Quiet: &visible})
	definition := steptest.Definition(t, "root", workflow.Step{Quiet: &quiet, Steps: []workflow.Step{
		{ID: "action", Uses: workflow.ActionSource{Path: "./action"}, Action: action},
		{ID: "invoke", Type: "run_workflow", With: map[string]any{"workflow": "child"}},
		{ID: "owner", Type: "capture", Defer: []workflow.Step{{ID: "deferred_hidden", Type: "capture"}, {ID: "deferred_visible", Type: "capture", Quiet: &visible}}},
	}}, workflow.Step{ID: "sibling", Type: "capture"})
	definition.Finally = []workflow.Step{{ID: "finally", Type: "capture"}}
	var events []ProgressEvent
	_, err := New(registry, WithWorkflowInvoker(host)).Run(t.Context(), definition, Options{Progress: func(event ProgressEvent) { events = append(events, event) }})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, event := range events {
		if event.Kind == StepStarted {
			got[event.StepID] = event.Quiet
		}
		if event.Kind == MessagePrinted && (event.RunID == "" || event.StepRunID == "") {
			t.Fatalf("uncorrelated message=%#v", event)
		}
	}
	want := map[string]bool{"action": true, "action_hidden": true, "action_visible": false, "invoke": true, "child_hidden": true, "child_visible": false, "owner": true, "sibling": false, "deferred_hidden": true, "deferred_visible": false, "finally": false}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("quiet (-want +got):\n%s", diff)
	}
}

func TestConcurrentMessagesAreSerializedInsideStepLifetimes(t *testing.T) {
	t.Parallel()
	registry := steptest.Registry(t, map[string]step.Builder{"message": func(map[string]any) (step.Runner, error) {
		return steptest.RunnerFunc(func(_ context.Context, request step.Request) (step.Result, error) {
			for index := range 10 {
				request.PrintMessage(useroutput.Message{Text: fmt.Sprint(index), Level: useroutput.Info})
			}
			return step.Result{}, nil
		}), nil
	}})
	definition := steptest.Definition(t, "parallel", workflow.Step{Concurrent: &workflow.ConcurrentGroup{Steps: []workflow.Step{{ID: "first", Type: "message"}, {ID: "second", Type: "message"}}, MaxConcurrency: 2}})
	var mu sync.Mutex
	var events []ProgressEvent
	state, err := New(registry).Run(t.Context(), definition, Options{Progress: func(event ProgressEvent) {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, event)
	}})
	if err != nil {
		t.Fatal(err)
	}
	started := map[correlation.StepRunID]bool{}
	counts := map[correlation.StepRunID]int{}
	for _, event := range events {
		switch event.Kind {
		case StepStarted:
			started[event.StepRunID] = true
		case MessagePrinted:
			if !started[event.StepRunID] || event.Message.Text != fmt.Sprint(counts[event.StepRunID]) {
				t.Fatalf("out of order message=%#v", event)
			}
			counts[event.StepRunID]++
		case StepFinished:
			if counts[event.StepRunID] != 10 {
				t.Fatalf("messages=%d", counts[event.StepRunID])
			}
			delete(started, event.StepRunID)
		}
	}
	if state.Stats.Succeeded != 2 || len(counts) != 2 {
		t.Fatalf("state=%#v counts=%v", state.Stats, counts)
	}
}

func TestDirectEngineWritesMessagesToStderr(t *testing.T) {
	t.Parallel()
	registry := steptest.Registry(t, map[string]step.Builder{"message": func(map[string]any) (step.Runner, error) {
		return steptest.RunnerFunc(func(_ context.Context, request step.Request) (step.Result, error) {
			request.PrintMessage(useroutput.Message{Text: "finding", Level: useroutput.Warn})
			return step.Result{}, nil
		}), nil
	}})
	var output bytes.Buffer
	_, err := New(registry).Run(t.Context(), steptest.Definition(t, "direct", workflow.Step{ID: "work", Type: "message"}), Options{Stdout: io.Discard, Stderr: &output})
	if err != nil || output.String() != "! finding\n" {
		t.Fatalf("output=%q err=%v", output.String(), err)
	}
}
