package engine

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/up2jj/wuko/diagnostic"
	"github.com/up2jj/wuko/internal/steptest"
	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/workflow"
)

func conditionalTestRegistry(t *testing.T, runs *int) *step.Registry {
	t.Helper()
	return steptest.Registry(t, map[string]step.Builder{"capture": func(raw map[string]any) (step.Runner, error) {
		return countingRunner{value: raw["value"], runs: runs}, nil
	}})
}

func TestConditionalBlockRunsSequentiallyAfterOneConditionEvaluation(t *testing.T) {
	t.Parallel()
	var runs int
	registry := conditionalTestRegistry(t, &runs)
	definition := steptest.Definition(
		t, "conditional",
		workflow.Step{If: `vars.result == nil`, Steps: []workflow.Step{
			{ID: "first", Type: "capture", With: map[string]any{"value": "produced"}},
			{ID: "second", Type: "capture", If: `steps.first.value == "produced"`, With: map[string]any{"value": "{{ .vars.result }}"}},
		}})
	definition.Vars = map[string]any{"result": nil}
	state, err := New(registry).Run(t.Context(), definition, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if runs != 2 || state.Steps["second"].(map[string]any)["value"] != "produced" {
		t.Fatalf("runs = %d, state = %#v", runs, state)
	}
	if state.Stats.Total != 2 || state.Stats.Succeeded != 2 || len(state.Stats.Steps) != 2 {
		t.Fatalf("stats = %#v", state.Stats)
	}
}

func TestConditionalBlockFalseRecordsLeafAndControlSkips(t *testing.T) {
	t.Parallel()
	var runs int
	registry := conditionalTestRegistry(t, &runs)
	definition := steptest.Definition(
		t, "skipped",
		workflow.Step{If: "false", Steps: []workflow.Step{
			{ID: "ordinary", Type: "capture", With: map[string]any{"value": "unused"}},
			{Concurrent: &workflow.ConcurrentGroup{MaxConcurrency: 2, FailFast: true, Steps: []workflow.Step{
				{ID: "parallel_one", Type: "capture", With: map[string]any{"value": "one"}},
				{ID: "parallel_two", Type: "capture", With: map[string]any{"value": "two"}},
			}}},
			{ID: "loop", Foreach: &workflow.ForeachGroup{Items: "[1]", MaxConcurrency: 1, FailFast: true, Steps: []workflow.Step{
				{ID: "iteration", Type: "capture", With: map[string]any{"value": "iteration"}},
			}}},
		}})

	var events []ProgressEvent
	state, err := New(registry).Run(t.Context(), definition, Options{
		Stdout: io.Discard, Stderr: io.Discard,
		Progress: func(event ProgressEvent) { events = append(events, event) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if runs != 0 || len(state.Steps) != 0 || len(state.Vars) != 0 {
		t.Fatalf("runs = %d, state = %#v", runs, state)
	}
	if state.Stats.Total != 4 || state.Stats.Skipped != 4 || len(state.Stats.Steps) != 4 {
		t.Fatalf("stats = %#v", state.Stats)
	}
	wantIDs := []string{"ordinary", "parallel_one", "parallel_two", "loop"}
	wantTypes := []string{"capture", "capture", "capture", "foreach"}
	for i, stats := range state.Stats.Steps {
		if stats.ID != wantIDs[i] || stats.Type != wantTypes[i] || stats.Index != i+1 || stats.Status != StatusSkipped {
			t.Fatalf("step stats %d = %#v", i, stats)
		}
	}
	var skippedEvents int
	for _, event := range events {
		if event.Kind == StepFinished && event.Status == StatusSkipped {
			skippedEvents++
		}
	}
	if skippedEvents != 4 {
		t.Fatalf("skipped events = %d, events = %#v", skippedEvents, events)
	}
}

func TestNestedConditionalBlocksEvaluateInnerConditionWhenReached(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name           string
		outer          string
		inner          string
		wantStatuses   []ExecutionStatus
		wantConditions int
	}{
		{name: "both true", outer: "true", inner: `steps.check.value == "missing"`, wantStatuses: []ExecutionStatus{StatusSucceeded, StatusSucceeded, StatusSucceeded, StatusSucceeded}, wantConditions: 2},
		{name: "inner false", outer: "true", inner: `steps.check.value == "present"`, wantStatuses: []ExecutionStatus{StatusSucceeded, StatusSkipped, StatusSkipped, StatusSucceeded}, wantConditions: 2},
		{name: "outer false", outer: "false", inner: `steps.check.value == "missing"`, wantStatuses: []ExecutionStatus{StatusSkipped, StatusSkipped, StatusSkipped, StatusSucceeded}, wantConditions: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var runs int
			registry := conditionalTestRegistry(t, &runs)
			definition := steptest.Definition(
				t, "nested",
				workflow.Step{If: workflow.Condition(test.outer), Steps: []workflow.Step{
					{ID: "check", Type: "capture", With: map[string]any{"value": "missing"}},
					{If: workflow.Condition(test.inner), Steps: []workflow.Step{
						{ID: "offer", Type: "capture", With: map[string]any{"value": "{{ .steps.check.value }}"}},
						{ID: "install", Type: "capture", With: map[string]any{"value": "installed"}},
					}},
				}},
				workflow.Step{ID: "after", Type: "capture", With: map[string]any{"value": "after"}})

			var conditions int
			state, err := New(registry).Run(t.Context(), definition, Options{
				Diagnostics: func(event diagnostic.Event) {
					if event.Phase == diagnostic.PhaseCondition && event.Status == diagnostic.StatusStarted && event.StepID == "" {
						conditions++
					}
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			if conditions != test.wantConditions {
				t.Fatalf("block condition evaluations = %d, want %d", conditions, test.wantConditions)
			}
			wantIDs := []string{"check", "offer", "install", "after"}
			if state.Stats.Total != 4 || len(state.Stats.Steps) != 4 {
				t.Fatalf("stats = %#v", state.Stats)
			}
			for i, stats := range state.Stats.Steps {
				if stats.ID != wantIDs[i] || stats.Index != i+1 || stats.Status != test.wantStatuses[i] {
					t.Fatalf("step stats %d = %#v, want id %q status %q", i, stats, wantIDs[i], test.wantStatuses[i])
				}
			}
			if test.wantStatuses[1] == StatusSucceeded && state.Steps["offer"].(map[string]any)["value"] != "missing" {
				t.Fatalf("offer = %#v", state.Steps["offer"])
			}
		})
	}
}

func TestReturnInsideNestedConditionalBlockStopsEnclosingSteps(t *testing.T) {
	t.Parallel()
	var runs int
	registry := conditionalTestRegistry(t, &runs)
	definition := steptest.Definition(
		t, "nested-return",
		workflow.Step{If: "true", Steps: []workflow.Step{
			{If: "true", Steps: []workflow.Step{
				{Return: &workflow.ReturnControl{Outputs: map[string]string{"result": `"early"`}}},
			}},
			{ID: "outer_after", Type: "capture", With: map[string]any{"value": "unused"}},
		}},
		workflow.Step{ID: "after", Type: "capture", With: map[string]any{"value": "unused"}})

	state, err := New(registry).Run(t.Context(), definition, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if runs != 0 || state.Stats.Skipped != 2 {
		t.Fatalf("runs = %d, stats = %#v", runs, state.Stats)
	}
}

func TestConditionalBlockStopsAfterChildFailure(t *testing.T) {
	t.Parallel()
	var runs int
	registry := conditionalTestRegistry(t, &runs)
	if err := registry.Register("fail", func(map[string]any) (step.Runner, error) { return alwaysFailRunner{}, nil }); err != nil {
		t.Fatal(err)
	}
	definition := steptest.Definition(
		t, "failure",
		workflow.Step{If: "true", Steps: []workflow.Step{
			{ID: "before", Type: "capture", With: map[string]any{"value": "before"}},
			{ID: "broken", Type: "fail", With: map[string]any{}},
			{ID: "after", Type: "capture", With: map[string]any{"value": "after"}},
		}})

	if _, err := New(registry).Run(t.Context(), definition, Options{}); err == nil || !strings.Contains(err.Error(), "broken") {
		t.Fatalf("error = %v", err)
	}
	if runs != 1 {
		t.Fatalf("capture runs = %d, want 1", runs)
	}
}

func TestConditionalBlockInsideForeachCollectsTransparentOutputs(t *testing.T) {
	t.Parallel()
	var runs int
	registry := conditionalTestRegistry(t, &runs)
	definition := steptest.Definition(t, "foreach-conditional", workflow.Step{ID: "loop", Foreach: &workflow.ForeachGroup{
		Items: "vars.items", Collect: "steps.inside", MaxConcurrency: 1, FailFast: true,
		Steps: []workflow.Step{{If: "true", Steps: []workflow.Step{
			{ID: "inside", Type: "capture", With: map[string]any{"value": "{{ .foreach.item }}"}},
		}}},
	}})
	definition.Vars = map[string]any{"items": []any{"one"}}
	state, err := New(registry).Run(t.Context(), definition, Options{})
	if err != nil {
		t.Fatal(err)
	}
	inside := state.Steps["loop"].(map[string]any)["results"].([]any)[0].(map[string]any)
	if inside["value"] != "one" || runs != 1 {
		t.Fatalf("result = %#v, runs = %d", inside, runs)
	}
}

func TestConditionalBlocksRunInCompositeActionsAndFinally(t *testing.T) {
	t.Parallel()
	var runs int
	registry := conditionalTestRegistry(t, &runs)
	action := testAction(t, "composite", workflow.Step{If: "true", Steps: []workflow.Step{
		{ID: "inside", Type: "capture", With: map[string]any{"value": "action"}},
	}})
	action.Outputs = map[string]workflow.ActionOutput{"value": {Value: "steps.inside.value"}}
	definition := steptest.Definition(t, "caller", workflow.Step{ID: "call", Uses: workflow.ActionSource{URL: "https://example.test/action"}, Action: action})
	definition.Finally = []workflow.Step{{If: `finally.status == "succeeded"`, Steps: []workflow.Step{
		{ID: "cleanup", Type: "capture", With: map[string]any{"value": "cleanup"}},
	}}}
	state, err := New(registry).Run(t.Context(), definition, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if state.Steps["call"].(map[string]any)["value"] != "action" || state.Steps["cleanup"].(map[string]any)["value"] != "cleanup" || runs != 2 {
		t.Fatalf("state = %#v, runs = %d", state, runs)
	}
}

func TestConditionalBlockDryRunDisplaysNestedPlan(t *testing.T) {
	t.Parallel()
	var runs int
	registry := conditionalTestRegistry(t, &runs)
	definition := steptest.Definition(
		t, "dry",
		workflow.Step{If: "vars.enabled", Steps: []workflow.Step{
			{ID: "first", Type: "capture", With: map[string]any{"value": "first"}},
			{If: "vars.second", Steps: []workflow.Step{
				{ID: "second", Type: "capture", With: map[string]any{"value": "second"}},
			}},
		}})
	definition.Vars = map[string]any{"enabled": false, "second": true}

	var output bytes.Buffer
	if _, err := New(registry).Run(t.Context(), definition, Options{DryRun: true, Stdout: &output, Stderr: io.Discard}); err != nil {
		t.Fatal(err)
	}
	want := "1. if: vars.enabled\n   1.1 first (capture)\n   1.2 if: vars.second\n      1.2.1 second (capture)\n"
	if output.String() != want || runs != 0 {
		t.Fatalf("output = %q, runs = %d", output.String(), runs)
	}
}

func TestConditionalBlockValidationDiagnosticUsesWrapperLocation(t *testing.T) {
	t.Parallel()
	var runs int
	registry := conditionalTestRegistry(t, &runs)
	wrapperLocation := diagnostic.Location{Source: "workflow.yaml", Line: 7, Column: 3}
	definition := steptest.Definition(
		t, "invalid",
		workflow.Step{If: "vars.", Location: wrapperLocation, Steps: []workflow.Step{
			{ID: "run", Type: "capture", With: map[string]any{"value": "unused"}},
		}})

	var events []diagnostic.Event
	err := New(registry).Validate(t.Context(), definition, Options{
		Diagnostics: func(event diagnostic.Event) { events = append(events, event) },
	})
	if err == nil {
		t.Fatal("expected validation error")
	}
	for _, event := range events {
		if event.Phase == diagnostic.PhaseValidation && event.Status == diagnostic.StatusFailed && event.Location == wrapperLocation {
			return
		}
	}
	t.Fatalf("missing wrapper validation diagnostic: %#v", events)
}
