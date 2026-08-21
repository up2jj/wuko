package engine

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/up2jj/wuko/diagnostic"
	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/workflow"
)

func conditionalTestRegistry(t *testing.T, runs *int) *step.Registry {
	t.Helper()
	registry := step.NewRegistry()
	if err := registry.Register("capture", func(raw map[string]any) (step.Runner, error) {
		return countingRunner{value: raw["value"], runs: runs}, nil
	}); err != nil {
		t.Fatal(err)
	}
	return registry
}

func TestConditionalBlockRunsSequentiallyAfterOneConditionEvaluation(t *testing.T) {
	var runs int
	registry := conditionalTestRegistry(t, &runs)
	definition := &workflow.Definition{
		Version: 1, Name: "conditional", Dir: t.TempDir(),
		Steps: []workflow.Step{{If: `!hasKey(vars, "result")`, Steps: []workflow.Step{
			{ID: "first", Type: "capture", With: map[string]any{"value": "produced"}},
			{ID: "second", Type: "capture", If: `steps.first.value == "produced"`, With: map[string]any{"value": "{{ .vars.result }}"}},
		}}},
	}
	state, err := New(registry).Run(t.Context(), definition, Options{Stdout: io.Discard, Stderr: io.Discard})
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
	var runs int
	registry := conditionalTestRegistry(t, &runs)
	definition := &workflow.Definition{
		Version: 1, Name: "skipped", Dir: t.TempDir(),
		Steps: []workflow.Step{{If: "false", Steps: []workflow.Step{
			{ID: "ordinary", Type: "capture", With: map[string]any{"value": "{{ .vars.missing }}"}},
			{Concurrent: &workflow.ConcurrentGroup{MaxConcurrency: 2, FailFast: true, Steps: []workflow.Step{
				{ID: "parallel_one", Type: "capture", With: map[string]any{"value": "one"}},
				{ID: "parallel_two", Type: "capture", With: map[string]any{"value": "two"}},
			}}},
			{ID: "loop", Foreach: &workflow.ForeachGroup{Items: "[1]", MaxConcurrency: 1, FailFast: true, Steps: []workflow.Step{
				{ID: "iteration", Type: "capture", With: map[string]any{"value": "iteration"}},
			}}},
		}}},
	}
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

func TestConditionalBlockStopsAfterChildFailure(t *testing.T) {
	var runs int
	registry := conditionalTestRegistry(t, &runs)
	if err := registry.Register("fail", func(map[string]any) (step.Runner, error) { return alwaysFailRunner{}, nil }); err != nil {
		t.Fatal(err)
	}
	definition := &workflow.Definition{
		Version: 1, Name: "failure", Dir: t.TempDir(),
		Steps: []workflow.Step{{If: "true", Steps: []workflow.Step{
			{ID: "before", Type: "capture", With: map[string]any{"value": "before"}},
			{ID: "broken", Type: "fail", With: map[string]any{}},
			{ID: "after", Type: "capture", With: map[string]any{"value": "after"}},
		}}},
	}
	if _, err := New(registry).Run(t.Context(), definition, Options{Stdout: io.Discard, Stderr: io.Discard}); err == nil || !strings.Contains(err.Error(), "broken") {
		t.Fatalf("error = %v", err)
	}
	if runs != 1 {
		t.Fatalf("capture runs = %d, want 1", runs)
	}
}

func TestConditionalBlockInsideForeachCollectsTransparentOutputs(t *testing.T) {
	var runs int
	registry := conditionalTestRegistry(t, &runs)
	definition := &workflow.Definition{
		Version: 1, Name: "foreach-conditional", Dir: t.TempDir(), Vars: map[string]any{"items": []any{"one"}},
		Steps: []workflow.Step{{ID: "loop", Foreach: &workflow.ForeachGroup{
			Items: "vars.items", MaxConcurrency: 1, FailFast: true,
			Steps: []workflow.Step{{If: "true", Steps: []workflow.Step{
				{ID: "inside", Type: "capture", With: map[string]any{"value": "{{ .foreach.item }}"}},
			}}},
		}}},
	}
	state, err := New(registry).Run(t.Context(), definition, Options{Stdout: io.Discard, Stderr: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	record := state.Steps["loop"].(map[string]any)["results"].([]any)[0].(map[string]any)
	inside := record["steps"].(map[string]any)["inside"].(map[string]any)
	if inside["value"] != "one" || runs != 1 {
		t.Fatalf("record = %#v, runs = %d", record, runs)
	}
}

func TestConditionalBlocksRunInCompositeActionsAndFinally(t *testing.T) {
	var runs int
	registry := conditionalTestRegistry(t, &runs)
	action := &workflow.Action{
		Version: 1, Name: "composite", Dir: t.TempDir(),
		Outputs: map[string]workflow.ActionOutput{"value": {Value: "steps.inside.value"}},
		Steps: []workflow.Step{{If: "true", Steps: []workflow.Step{
			{ID: "inside", Type: "capture", With: map[string]any{"value": "action"}},
		}}},
	}
	definition := &workflow.Definition{
		Version: 1, Name: "caller", Dir: t.TempDir(),
		Steps: []workflow.Step{{ID: "call", Uses: workflow.ActionSource{URL: "https://example.test/action"}, Action: action, With: map[string]any{}}},
		Finally: []workflow.Step{{If: `finally.status == "succeeded"`, Steps: []workflow.Step{
			{ID: "cleanup", Type: "capture", With: map[string]any{"value": "cleanup"}},
		}}},
	}
	state, err := New(registry).Run(t.Context(), definition, Options{Stdout: io.Discard, Stderr: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if state.Steps["call"].(map[string]any)["value"] != "action" || state.Steps["cleanup"].(map[string]any)["value"] != "cleanup" || runs != 2 {
		t.Fatalf("state = %#v, runs = %d", state, runs)
	}
}

func TestConditionalBlockDryRunDisplaysNestedPlan(t *testing.T) {
	var runs int
	registry := conditionalTestRegistry(t, &runs)
	definition := &workflow.Definition{
		Version: 1, Name: "dry", Dir: t.TempDir(),
		Steps: []workflow.Step{{If: "vars.enabled", Steps: []workflow.Step{
			{ID: "first", Type: "capture", With: map[string]any{"value": "first"}},
			{ID: "second", Type: "capture", With: map[string]any{"value": "second"}},
		}}},
	}
	var output bytes.Buffer
	if _, err := New(registry).Run(t.Context(), definition, Options{DryRun: true, Stdout: &output, Stderr: io.Discard}); err != nil {
		t.Fatal(err)
	}
	want := "1. if: vars.enabled\n   1.1 first (capture)\n   1.2 second (capture)\n"
	if output.String() != want || runs != 0 {
		t.Fatalf("output = %q, runs = %d", output.String(), runs)
	}
}

func TestConditionalBlockValidationDiagnosticUsesWrapperLocation(t *testing.T) {
	var runs int
	registry := conditionalTestRegistry(t, &runs)
	wrapperLocation := diagnostic.Location{Source: "workflow.yaml", Line: 7, Column: 3}
	definition := &workflow.Definition{
		Version: 1, Name: "invalid", Dir: t.TempDir(),
		Steps: []workflow.Step{{If: "vars.", Location: wrapperLocation, Steps: []workflow.Step{
			{ID: "run", Type: "capture", With: map[string]any{"value": "unused"}},
		}}},
	}
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
