package engine

import (
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/workflow"
)

func TestRunForeachAggregatesOrderedIsolatedResults(t *testing.T) {
	registry := newTestRegistry(t, map[string]step.Builder{"capture": func(raw map[string]any) (step.Runner, error) {
		value := raw["value"]
		return runnerFunc(func(_ context.Context, _ step.Request) (step.Result, error) {
			return step.Result{Outputs: map[string]any{"value": value}, Variables: map[string]any{"temporary": value}}, nil
		}), nil
	}})
	definition := testDefinition(t, "foreach", workflow.Step{ID: "deploy", Foreach: &workflow.ForeachGroup{
		Items: "vars.targets", MaxConcurrency: 1, FailFast: true,
		Steps: []workflow.Step{
			{ID: "first", Type: "capture", With: map[string]any{"value": "{{ .foreach.index }}:{{ .foreach.item }}"}},
			{ID: "second", Type: "capture", If: "foreach.index >= 0", With: map[string]any{"value": "{{ .vars.temporary }}"}},
		},
	}})
	definition.Vars = map[string]any{"targets": []any{"linux", "darwin"}}
	state, err := New(registry).Run(t.Context(), definition, Options{})
	if err != nil {
		t.Fatal(err)
	}
	result := state.Steps["deploy"].(map[string]any)
	if result["count"] != 2 {
		t.Fatalf("result = %#v", result)
	}
	records := result["results"].([]any)
	first := records[0].(map[string]any)
	second := records[1].(map[string]any)
	if first["item"] != "linux" || second["item"] != "darwin" {
		t.Fatalf("records = %#v", records)
	}
	firstSteps := first["steps"].(map[string]any)
	if firstSteps["second"].(map[string]any)["value"] != "0:linux" {
		t.Fatalf("first steps = %#v", firstSteps)
	}
	if _, exists := state.Vars["temporary"]; exists {
		t.Fatalf("iteration variable escaped: %#v", state.Vars)
	}
	if state.Stats.Total != 1 || state.Stats.Succeeded != 1 || len(state.Stats.Steps[0].Iterations) != 2 {
		t.Fatalf("stats = %#v", state.Stats)
	}
}

func TestRunMatrixUsesDeterministicBindings(t *testing.T) {
	registry := newTestRegistry(t, map[string]step.Builder{"binding": func(map[string]any) (step.Runner, error) {
		return runnerFunc(func(_ context.Context, request step.Request) (step.Result, error) {
			return step.Result{Outputs: map[string]any{"matrix": request.Bindings["matrix"]}}, nil
		}), nil
	}})
	definition := testDefinition(t, "matrix", workflow.Step{ID: "checks", Matrix: &workflow.MatrixGroup{
		Axes: workflow.MatrixAxes{
			{Name: "os", Values: []any{"linux", "darwin"}},
			{Name: "version", Expression: "vars.versions"},
		},
		MaxConcurrency: 1, FailFast: true,
		Steps: []workflow.Step{{ID: "test", Type: "binding", With: map[string]any{}}},
	}})
	definition.Vars = map[string]any{"versions": []any{"1", "2"}}
	state, err := New(registry).Run(t.Context(), definition, Options{})
	if err != nil {
		t.Fatal(err)
	}
	records := state.Steps["checks"].(map[string]any)["results"].([]any)
	var got []map[string]any
	for _, raw := range records {
		got = append(got, raw.(map[string]any)["matrix"].(map[string]any))
	}
	want := []map[string]any{
		{"os": "linux", "version": "1"}, {"os": "linux", "version": "2"},
		{"os": "darwin", "version": "1"}, {"os": "darwin", "version": "2"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("matrix = %#v, want %#v", got, want)
	}
}

func TestRunControlFailureDoesNotCommitAggregate(t *testing.T) {
	registry := newTestRegistry(t, map[string]step.Builder{"fail": func(map[string]any) (step.Runner, error) {
		return runnerFunc(func(_ context.Context, request step.Request) (step.Result, error) {
			if request.Bindings["foreach"].(map[string]any)["item"] == "bad" {
				return step.Result{}, errors.New("broken")
			}
			return step.Result{Outputs: map[string]any{"ok": true}}, nil
		}), nil
	}})
	definition := testDefinition(t, "failure", workflow.Step{ID: "loop", Foreach: &workflow.ForeachGroup{
		Items: "vars.items", MaxConcurrency: 1, FailFast: false,
		Steps: []workflow.Step{{ID: "run", Type: "fail", With: map[string]any{}}},
	}})
	definition.Vars = map[string]any{"items": []any{"good", "bad", "later"}}
	var finished ProgressEvent
	state, err := New(registry).Run(t.Context(), definition, Options{
		Stdout: io.Discard, Stderr: io.Discard,
		Progress: func(event ProgressEvent) {
			if event.Kind == ControlFinished {
				finished = event
			}
		},
	})
	if err == nil {
		t.Fatal("expected failure")
	}
	if state != nil {
		t.Fatalf("failed run returned state %#v", state)
	}
	if finished.Started != 3 || finished.Succeeded != 2 {
		t.Fatalf("control progress = %#v, want 3 started and 2 succeeded", finished)
	}
}

func TestRunControlRejectsExpansionAboveWorkflowLimit(t *testing.T) {
	runs := 0
	registry := newTestRegistry(t, map[string]step.Builder{"count": func(map[string]any) (step.Runner, error) {
		return runnerFunc(func(context.Context, step.Request) (step.Result, error) {
			runs++
			return step.Result{}, nil
		}), nil
	}})
	definition := testDefinition(t, "bounded", workflow.Step{ID: "loop", Foreach: &workflow.ForeachGroup{
		Items: "vars.items", MaxConcurrency: 1, MaxIterations: 2, FailFast: true,
		Steps: []workflow.Step{{ID: "run", Type: "count", With: map[string]any{}}},
	}})
	definition.Vars = map[string]any{"items": []any{"one", "two", "three"}}
	_, err := New(registry).Run(t.Context(), definition, Options{})
	if err == nil || !strings.Contains(err.Error(), "exceeds max_iterations 2") {
		t.Fatalf("Run() error = %v", err)
	}
	if runs != 0 {
		t.Fatalf("runner executed %d times before expansion rejection", runs)
	}
}

func TestForeachPassesBindingToCompositeActionInput(t *testing.T) {
	registry := newTestRegistry(t, map[string]step.Builder{"echo": func(raw map[string]any) (step.Runner, error) {
		return runnerFunc(func(_ context.Context, _ step.Request) (step.Result, error) {
			return step.Result{Outputs: map[string]any{"value": raw["value"]}}, nil
		}), nil
	}})
	action := testAction(t, "echo-action", workflow.Step{ID: "echo", Type: "echo", With: map[string]any{"value": "{{ .inputs.target }}"}})
	action.Inputs = map[string]workflow.ActionInput{"target": {Type: "string", Required: true}}
	action.Outputs = map[string]workflow.ActionOutput{"result": {Value: "steps.echo.value"}}
	definition := testDefinition(t, "caller", workflow.Step{ID: "calls", Foreach: &workflow.ForeachGroup{
		Items: "vars.targets", MaxConcurrency: 1, FailFast: true,
		Steps: []workflow.Step{{
			ID: "remote", Uses: workflow.ActionSource{URL: "https://example.test/action"}, Action: action,
			With: map[string]any{"target": map[string]any{"expr": "foreach.item"}},
		}},
	}})
	definition.Vars = map[string]any{"targets": []any{"api"}}
	state, err := New(registry).Run(t.Context(), definition, Options{})
	if err != nil {
		t.Fatal(err)
	}
	record := state.Steps["calls"].(map[string]any)["results"].([]any)[0].(map[string]any)
	remote := record["steps"].(map[string]any)["remote"].(map[string]any)
	if remote["result"] != "api" {
		t.Fatalf("remote output = %#v", remote)
	}
}

func TestControlInteractivePolicyAndNestedConcurrent(t *testing.T) {
	seen := make(map[string]bool)
	var mu sync.Mutex
	registry := newTestRegistry(t, map[string]step.Builder{"observe": func(raw map[string]any) (step.Runner, error) {
		label := raw["label"].(string)
		return runnerFunc(func(_ context.Context, request step.Request) (step.Result, error) {
			mu.Lock()
			seen[label] = request.Interactive
			mu.Unlock()
			return step.Result{Outputs: map[string]any{"label": label}}, nil
		}), nil
	}})
	definition := testDefinition(t, "policies",
		workflow.Step{ID: "serial", Foreach: &workflow.ForeachGroup{
			Items: "vars.items", MaxConcurrency: 1, FailFast: true,
			Steps: []workflow.Step{{ID: "prompt", Type: "observe", With: map[string]any{"label": "serial"}}},
		}},
		workflow.Step{ID: "parallel", Matrix: &workflow.MatrixGroup{
			Axes: workflow.MatrixAxes{{Name: "os", Values: []any{"linux"}}}, MaxConcurrency: 2, FailFast: true,
			Steps: []workflow.Step{{Concurrent: &workflow.ConcurrentGroup{
				MaxConcurrency: 2, FailFast: true,
				Steps: []workflow.Step{
					{ID: "first", Type: "observe", With: map[string]any{"label": "parallel-first"}},
					{ID: "second", Type: "observe", With: map[string]any{"label": "parallel-second"}},
				},
			}}},
		}},
	)
	definition.Vars = map[string]any{"items": []any{"one"}}
	state, err := New(registry).Run(t.Context(), definition, Options{Interactive: true, Stdout: io.Discard, Stderr: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if !seen["serial"] || seen["parallel-first"] || seen["parallel-second"] {
		t.Fatalf("interactive observations = %#v", seen)
	}
	parallel := state.Steps["parallel"].(map[string]any)["results"].([]any)[0].(map[string]any)
	if len(parallel["steps"].(map[string]any)) != 2 {
		t.Fatalf("parallel result = %#v", parallel)
	}
}
