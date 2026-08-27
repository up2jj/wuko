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
		Items: "vars.targets", Collect: `{"item": foreach.item, "value": steps.second.value, "local": vars.temporary, "input": inputs.seed, "env": env.COLLECT, "dir": run.dir}`, MaxConcurrency: 1, FailFast: true,
		Steps: []workflow.Step{
			{ID: "first", Type: "capture", With: map[string]any{"value": "{{ .foreach.index }}:{{ .foreach.item }}"}},
			{ID: "second", Type: "capture", If: "foreach.index >= 0", With: map[string]any{"value": "{{ .vars.temporary }}"}},
		},
	}})
	definition.Vars = map[string]any{"targets": []any{"linux", "darwin"}}
	definition.Env = workflow.Environment{"COLLECT": "available"}
	runDir := t.TempDir()
	state, err := New(registry).Run(t.Context(), definition, Options{inputs: map[string]any{"seed": 42}, RunDir: runDir})
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
	if first["value"] != "0:linux" || first["local"] != "0:linux" || first["input"] != 42 || first["env"] != "available" || first["dir"] != runDir {
		t.Fatalf("first result = %#v", first)
	}
	if _, exists := state.Vars["temporary"]; exists {
		t.Fatalf("iteration variable escaped: %#v", state.Vars)
	}
	if state.Stats.Total != 1 || state.Stats.Succeeded != 1 || len(state.Stats.Steps[0].Iterations) != 2 {
		t.Fatalf("stats = %#v", state.Stats)
	}
}

func TestRunBatchUsesDynamicSizeAndCollectsOrderedBindings(t *testing.T) {
	registry := newTestRegistry(t, map[string]step.Builder{"capture_batch": func(raw map[string]any) (step.Runner, error) {
		value := raw["value"]
		return runnerFunc(func(_ context.Context, request step.Request) (step.Result, error) {
			return step.Result{Outputs: map[string]any{"value": value, "binding": request.Bindings["batch"]}}, nil
		}), nil
	}})
	definition := testDefinition(t, "batch", workflow.Step{ID: "groups", Batch: &workflow.BatchGroup{
		Items: "vars.targets", Size: workflow.BatchSize{Expression: "vars.batch_size"},
		Collect: `{"index": batch.index, "items": batch.items, "value": steps.run.value}`, MaxConcurrency: 2, FailFast: true,
		Steps: []workflow.Step{{
			ID: "run", Type: "capture_batch", If: "batch.index >= 0",
			With: map[string]any{"value": "{{ .batch.items | toJSONCompact }}"},
		}},
	}})
	definition.Vars = map[string]any{"targets": []any{"api", "worker", "web"}, "batch_size": 2}
	state, err := New(registry).Run(t.Context(), definition, Options{})
	if err != nil {
		t.Fatal(err)
	}
	outputs := state.Steps["groups"].(map[string]any)
	if outputs["count"] != 2 {
		t.Fatalf("outputs = %#v", outputs)
	}
	got := outputs["results"].([]any)
	want := []any{
		map[string]any{"index": 0, "items": []any{"api", "worker"}, "value": `["api","worker"]`},
		map[string]any{"index": 1, "items": []any{"web"}, "value": `["web"]`},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("results = %#v, want %#v", got, want)
	}
	if len(state.Stats.Steps[0].Iterations) != 2 || state.Stats.Steps[0].Type != "batch" {
		t.Fatalf("stats = %#v", state.Stats)
	}
}

func TestRunBatchValidatesDynamicSizeResult(t *testing.T) {
	registry := newTestRegistry(t, map[string]step.Builder{"noop": func(map[string]any) (step.Runner, error) {
		return runnerFunc(func(context.Context, step.Request) (step.Result, error) { return step.Result{}, nil }), nil
	}})
	tests := []struct {
		name string
		size any
	}{
		{name: "zero", size: 0},
		{name: "negative", size: -1},
		{name: "fractional", size: 1.5},
		{name: "string", size: "two"},
		{name: "list", size: []any{2}},
		{name: "overflow", size: ^uint64(0)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			definition := testDefinition(t, test.name, workflow.Step{ID: "groups", Batch: &workflow.BatchGroup{
				Items: "vars.items", Size: workflow.BatchSize{Expression: "vars.size"}, MaxConcurrency: 1, FailFast: true,
				Steps: []workflow.Step{{ID: "run", Type: "noop", With: map[string]any{}}},
			}})
			definition.Vars = map[string]any{"items": []any{1, 2}, "size": test.size}
			_, err := New(registry).Run(t.Context(), definition, Options{})
			if err == nil || !strings.Contains(err.Error(), "want positive integer") {
				t.Fatalf("Run() error = %v", err)
			}
		})
	}

	valid := testDefinition(t, "integral-float", workflow.Step{ID: "groups", Batch: &workflow.BatchGroup{
		Items: "vars.items", Size: workflow.BatchSize{Expression: "vars.size"}, MaxConcurrency: 1, FailFast: true,
		Steps: []workflow.Step{{ID: "run", Type: "noop", With: map[string]any{}}},
	}})
	valid.Vars = map[string]any{"items": []any{1, 2, 3}, "size": 2.0}
	state, err := New(registry).Run(t.Context(), valid, Options{})
	if err != nil || state.Steps["groups"].(map[string]any)["count"] != 2 {
		t.Fatalf("integral float state = %#v, error = %v", state, err)
	}
}

func TestValidateBatchRejectsInvalidSizeExpression(t *testing.T) {
	registry := newTestRegistry(t, map[string]step.Builder{"noop": func(map[string]any) (step.Runner, error) {
		return runnerFunc(func(context.Context, step.Request) (step.Result, error) { return step.Result{}, nil }), nil
	}})
	definition := testDefinition(t, "invalid-size", workflow.Step{ID: "groups", Batch: &workflow.BatchGroup{
		Items: "[]", Size: workflow.BatchSize{Expression: "vars."}, MaxConcurrency: 1, FailFast: true,
		Steps: []workflow.Step{{ID: "run", Type: "noop", With: map[string]any{}}},
	}})
	if err := New(registry).Validate(t.Context(), definition, Options{}); err == nil || !strings.Contains(err.Error(), "batch size") {
		t.Fatalf("Validate() error = %v", err)
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
		Collect: "matrix", MaxConcurrency: 2, FailFast: true,
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
		got = append(got, raw.(map[string]any))
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
		Items: "vars.targets", Collect: "steps.remote", MaxConcurrency: 1, FailFast: true,
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
	remote := state.Steps["calls"].(map[string]any)["results"].([]any)[0].(map[string]any)
	if remote["result"] != "api" {
		t.Fatalf("remote output = %#v", remote)
	}
}

func TestBatchPassesBindingToCompositeActionInput(t *testing.T) {
	registry := newTestRegistry(t, map[string]step.Builder{"echo": func(raw map[string]any) (step.Runner, error) {
		return runnerFunc(func(_ context.Context, _ step.Request) (step.Result, error) {
			return step.Result{Outputs: map[string]any{"value": raw["value"]}}, nil
		}), nil
	}})
	action := testAction(t, "batch-action", workflow.Step{ID: "echo", Type: "echo", With: map[string]any{"value": "{{ .inputs.targets | toJSONCompact }}"}})
	action.Inputs = map[string]workflow.ActionInput{"targets": {Type: "array", Required: true}}
	action.Outputs = map[string]workflow.ActionOutput{"result": {Value: "steps.echo.value"}}
	definition := testDefinition(t, "caller", workflow.Step{ID: "calls", Batch: &workflow.BatchGroup{
		Items: "vars.targets", Size: workflow.BatchSize{Literal: 2}, Collect: "steps.remote", MaxConcurrency: 1, FailFast: true,
		Steps: []workflow.Step{{
			ID: "remote", Uses: workflow.ActionSource{URL: "https://example.test/action"}, Action: action,
			With: map[string]any{"targets": map[string]any{"expr": "batch.items"}},
		}},
	}})
	definition.Vars = map[string]any{"targets": []any{"api", "worker", "web"}}
	state, err := New(registry).Run(t.Context(), definition, Options{})
	if err != nil {
		t.Fatal(err)
	}
	results := state.Steps["calls"].(map[string]any)["results"].([]any)
	if results[0].(map[string]any)["result"] != `["api","worker"]` || results[1].(map[string]any)["result"] != `["web"]` {
		t.Fatalf("results = %#v", results)
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
			Axes: workflow.MatrixAxes{{Name: "os", Values: []any{"linux"}}}, Collect: `{"first": steps.first, "second": steps.second}`, MaxConcurrency: 2, FailFast: true,
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
	if len(parallel) != 2 {
		t.Fatalf("parallel result = %#v", parallel)
	}
}

func TestRunControlWithoutCollectExposesCountOnly(t *testing.T) {
	registry := newTestRegistry(t, map[string]step.Builder{"value": func(map[string]any) (step.Runner, error) {
		return runnerFunc(func(context.Context, step.Request) (step.Result, error) {
			return step.Result{Outputs: map[string]any{"hidden": true}}, nil
		}), nil
	}})
	definition := testDefinition(t, "count-only", workflow.Step{ID: "loop", Foreach: &workflow.ForeachGroup{
		Items: "[1, 2]", MaxConcurrency: 1, FailFast: true,
		Steps: []workflow.Step{{ID: "run", Type: "value", With: map[string]any{}}},
	}})
	state, err := New(registry).Run(t.Context(), definition, Options{})
	if err != nil {
		t.Fatal(err)
	}
	got := state.Steps["loop"].(map[string]any)
	if !reflect.DeepEqual(got, map[string]any{"count": 2}) {
		t.Fatalf("outputs = %#v", got)
	}
}

func TestRunControlCollectsEmptyResults(t *testing.T) {
	registry := newTestRegistry(t, map[string]step.Builder{"noop": func(map[string]any) (step.Runner, error) {
		return runnerFunc(func(context.Context, step.Request) (step.Result, error) { return step.Result{}, nil }), nil
	}})
	definition := testDefinition(t, "empty", workflow.Step{ID: "loop", Foreach: &workflow.ForeachGroup{
		Items: "[]", Collect: "foreach.item", MaxConcurrency: 1, FailFast: true,
		Steps: []workflow.Step{{ID: "run", Type: "noop", With: map[string]any{}}},
	}})
	state, err := New(registry).Run(t.Context(), definition, Options{})
	if err != nil {
		t.Fatal(err)
	}
	outputs := state.Steps["loop"].(map[string]any)
	if outputs["count"] != 0 || len(outputs["results"].([]any)) != 0 {
		t.Fatalf("outputs = %#v", outputs)
	}
}

func TestRunControlCollectPreservesNestedTypedValues(t *testing.T) {
	registry := newTestRegistry(t, map[string]step.Builder{"noop": func(map[string]any) (step.Runner, error) {
		return runnerFunc(func(context.Context, step.Request) (step.Result, error) { return step.Result{}, nil }), nil
	}})
	definition := testDefinition(t, "typed", workflow.Step{ID: "loop", Foreach: &workflow.ForeachGroup{
		Items: "[true]", Collect: `[foreach.item, nil, {"nested": [1, "two"]}]`, MaxConcurrency: 1, FailFast: true,
		Steps: []workflow.Step{{ID: "run", Type: "noop", With: map[string]any{}}},
	}})
	state, err := New(registry).Run(t.Context(), definition, Options{})
	if err != nil {
		t.Fatal(err)
	}
	got := state.Steps["loop"].(map[string]any)["results"].([]any)[0]
	want := []any{true, nil, map[string]any{"nested": []any{1, "two"}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("result = %#v, want %#v", got, want)
	}
}

func TestValidateControlRejectsInvalidCollectExpression(t *testing.T) {
	registry := newTestRegistry(t, map[string]step.Builder{"noop": func(map[string]any) (step.Runner, error) {
		return runnerFunc(func(context.Context, step.Request) (step.Result, error) { return step.Result{}, nil }), nil
	}})
	definition := testDefinition(t, "invalid", workflow.Step{ID: "loop", Foreach: &workflow.ForeachGroup{
		Items: "[1]", Collect: "steps.", MaxConcurrency: 1, FailFast: true,
		Steps: []workflow.Step{{ID: "run", Type: "noop", With: map[string]any{}}},
	}})
	if err := New(registry).Validate(t.Context(), definition, Options{}); err == nil || !strings.Contains(err.Error(), "foreach collect") {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestRunControlCollectFailureIsAtomic(t *testing.T) {
	registry := newTestRegistry(t, map[string]step.Builder{
		"value": func(map[string]any) (step.Runner, error) {
			return runnerFunc(func(context.Context, step.Request) (step.Result, error) {
				return step.Result{Outputs: map[string]any{"ok": true}}, nil
			}), nil
		},
		"unsupported": func(map[string]any) (step.Runner, error) {
			return runnerFunc(func(context.Context, step.Request) (step.Result, error) {
				return step.Result{Outputs: map[string]any{"value": make(chan struct{})}}, nil
			}), nil
		},
	})
	for _, test := range []struct {
		name       string
		stepType   string
		expression string
		want       string
	}{
		{name: "missing output", stepType: "value", expression: "steps.missing.value", want: "collect iteration 0"},
		{name: "unsupported value", stepType: "unsupported", expression: "steps.run.value", want: "YAML/JSON-compatible"},
	} {
		t.Run(test.name, func(t *testing.T) {
			definition := testDefinition(t, test.name, workflow.Step{ID: "loop", Foreach: &workflow.ForeachGroup{
				Items: "[1]", Collect: test.expression, MaxConcurrency: 1, FailFast: true,
				Steps: []workflow.Step{{ID: "run", Type: test.stepType, With: map[string]any{}}},
			}})
			var finished ProgressEvent
			state, err := New(registry).Run(t.Context(), definition, Options{Progress: func(event ProgressEvent) {
				if event.Kind == ControlFinished {
					finished = event
				}
			}})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
			if state != nil || finished.Status != StatusFailed || finished.Succeeded != 1 {
				t.Fatalf("state = %#v, progress = %#v", state, finished)
			}
		})
	}
}

func TestAllStepsSkippedTreatsABranchThatRecordedNothingAsSkipped(t *testing.T) {
	tests := []struct {
		name  string
		stats RunStats
		want  bool
	}{
		{name: "no steps recorded", stats: RunStats{}, want: true},
		{name: "declared steps all skipped", stats: RunStats{Steps: []StepStats{
			{ID: "first", Status: StatusSkipped}, {ID: "second", Status: StatusSkipped},
		}}, want: true},
		{name: "one step ran", stats: RunStats{Steps: []StepStats{
			{ID: "first", Status: StatusSkipped}, {ID: "second", Status: StatusSucceeded},
		}}, want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := allStepsSkipped(test.stats); got != test.want {
				t.Fatalf("allStepsSkipped(%#v) = %t, want %t", test.stats, got, test.want)
			}
		})
	}
}

func TestCatchPhaseThatRecordedNoStepsIsSkipped(t *testing.T) {
	if status := catchPhaseStatus(nil, RunStats{}); status != StatusSkipped {
		t.Fatalf("catchPhaseStatus = %q, want %q", status, StatusSkipped)
	}
}
