package engine

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/workflow"
)

type captureRunner struct {
	value any
	seen  *step.Request
}

func TestRunSkipsGuardedStepAndDependent(t *testing.T) {
	var runs int
	registry := newTestRegistry(t, map[string]step.Builder{"capture": func(raw map[string]any) (step.Runner, error) {
		return countingRunner{value: raw["value"], runs: &runs}, nil
	}})
	definition := testDefinition(t, "conditional",
		workflow.Step{ID: "prepare", Type: "capture", If: "vars.prepare", With: map[string]any{"value": "{{ .vars.artifact_path }}"}},
		workflow.Step{ID: "upload", Type: "capture", If: `"prepare" in steps`, With: map[string]any{"value": "{{ .vars.artifact_path }}"}},
		workflow.Step{ID: "fallback", Type: "capture", If: `"prepare" not in steps`, With: map[string]any{"value": "fallback"}},
	)
	definition.Vars = map[string]any{"prepare": false}
	var output bytes.Buffer
	state, err := New(registry).Run(t.Context(), definition, Options{
		RunDir: t.TempDir(), Stdout: &output, Stderr: io.Discard,
	})
	if err != nil {
		t.Fatal(err)
	}
	if runs != 1 {
		t.Fatalf("runs = %d, want 1", runs)
	}
	if _, ok := state.Steps["prepare"]; ok {
		t.Fatal("skipped prepare step is present in state")
	}
	if _, ok := state.Steps["upload"]; ok {
		t.Fatal("skipped upload step is present in state")
	}
	if got := state.Steps["fallback"].(map[string]any)["value"]; got != "fallback" {
		t.Fatalf("fallback value = %v", got)
	}
	if got := strings.Count(output.String(), "skipped"); got != 2 {
		t.Fatalf("skip messages = %d, output = %q", got, output.String())
	}
}

func TestRunConditionUsesRuntimeState(t *testing.T) {
	var runs int
	registry := newTestRegistry(t, map[string]step.Builder{"capture": func(raw map[string]any) (step.Runner, error) {
		return countingRunner{value: raw["value"], runs: &runs}, nil
	}})
	definition := testDefinition(t, "conditional",
		workflow.Step{ID: "prepare", Type: "capture", With: map[string]any{"value": true}},
		workflow.Step{
			ID: "consume", Type: "capture",
			If:   `hasKey(steps, "prepare") && steps.prepare.value && vars.result && env.MODE == "test" && workflow.name == "conditional" && run.dir != ""`,
			With: map[string]any{"value": "consumed"},
		},
	)
	state, err := New(registry).Run(t.Context(), definition, Options{
		Env: map[string]string{"MODE": "test"}, RunDir: t.TempDir(), Stdout: io.Discard, Stderr: io.Discard,
	})
	if err != nil {
		t.Fatal(err)
	}
	if runs != 2 {
		t.Fatalf("runs = %d, want 2", runs)
	}
	if got := state.Steps["consume"].(map[string]any)["value"]; got != "consumed" {
		t.Fatalf("consume value = %v", got)
	}
}

func TestValidateRejectsInvalidOrNonBooleanCondition(t *testing.T) {
	registry := newTestRegistry(t, map[string]step.Builder{"capture": func(raw map[string]any) (step.Runner, error) {
		return countingRunner{}, nil
	}})
	tests := []workflow.Condition{"vars.", "42"}
	for _, condition := range tests {
		t.Run(string(condition), func(t *testing.T) {
			definition := testDefinition(t, "invalid", workflow.Step{ID: "run", Type: "capture", If: condition})
			if err := New(registry).Validate(t.Context(), definition, Options{}); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestValidateUsesWorkflowStructuralValidation(t *testing.T) {
	tests := []struct {
		name string
		step workflow.Step
	}{
		{name: "conditional missing if", step: workflow.Step{Steps: []workflow.Step{{ID: "run", Type: "capture"}}}},
		{name: "conditional mixed fields", step: workflow.Step{ID: "block", If: "true", Steps: []workflow.Step{{ID: "run", Type: "capture"}}}},
		{name: "working directory empty children", step: workflow.Step{WorkingDirectory: "build", Steps: []workflow.Step{}}},
		{name: "working directory mixed fields", step: workflow.Step{ID: "block", WorkingDirectory: "build", Steps: []workflow.Step{{ID: "run", Type: "capture"}}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			definition := testDefinition(t, "invalid", test.step)
			structureErr := definition.ValidateStructure()
			if structureErr == nil {
				t.Fatal("expected workflow structural validation error")
			}
			err := New(newTestRegistry(t, nil)).Validate(t.Context(), definition, Options{})
			if err == nil || !strings.Contains(err.Error(), structureErr.Error()) {
				t.Fatalf("engine error = %v, want workflow error %q", err, structureErr)
			}
		})
	}
}

func TestStructuralValidationPrecedesStepConstruction(t *testing.T) {
	built := false
	registry := newTestRegistry(t, map[string]step.Builder{"capture": func(map[string]any) (step.Runner, error) {
		built = true
		return countingRunner{}, nil
	}})
	definition := testDefinition(t, "invalid", workflow.Step{ID: "not-valid", Type: "capture"})
	err := New(registry).Validate(t.Context(), definition, Options{})
	if err == nil || !strings.Contains(err.Error(), "invalid id") {
		t.Fatalf("error = %v, want invalid id", err)
	}
	if built {
		t.Fatal("step was constructed before structural validation completed")
	}
}

func TestRunRejectsNonBooleanConditionAtRuntime(t *testing.T) {
	registry := newTestRegistry(t, map[string]step.Builder{"capture": func(raw map[string]any) (step.Runner, error) {
		return countingRunner{}, nil
	}})
	definition := testDefinition(t, "invalid", workflow.Step{ID: "run", Type: "capture", If: "vars.enabled"})
	definition.Vars = map[string]any{"enabled": "yes"}
	_, err := New(registry).Run(t.Context(), definition, Options{})
	if err == nil {
		t.Fatal("expected runtime condition error")
	}
	if !strings.Contains(err.Error(), "evaluating if") {
		t.Fatalf("error = %q", err)
	}
}

func TestDryRunPrintsButDoesNotEvaluateCondition(t *testing.T) {
	registry := newTestRegistry(t, map[string]step.Builder{"capture": func(raw map[string]any) (step.Runner, error) {
		return countingRunner{}, nil
	}})
	timeout := workflow.Duration(2 * time.Second)
	definition := testDefinition(t, "dry-run", workflow.Step{
		ID: "run", Type: "capture", If: "vars.missing",
		Timeout: &timeout, Retry: immediateRetry(2), With: map[string]any{"value": "{{ .vars.also_missing }}"},
	})
	var output bytes.Buffer
	if _, err := New(registry).Run(t.Context(), definition, Options{
		DryRun: true, Stdout: &output, Stderr: io.Discard,
	}); err != nil {
		t.Fatal(err)
	}
	if got := output.String(); !strings.Contains(got, "[timeout 2s, 2 attempts] if: vars.missing") {
		t.Fatalf("output = %q", got)
	}
}

func TestDryRunPrintsUnexpandedControls(t *testing.T) {
	registry := newTestRegistry(t, map[string]step.Builder{"capture": func(map[string]any) (step.Runner, error) { return countingRunner{}, nil }})
	definition := testDefinition(t, "fanout-dry-run", workflow.Step{ID: "loop", Foreach: &workflow.ForeachGroup{
		Items: "vars.missing", Collect: "steps.run.value", MaxConcurrency: 1, FailFast: true,
		Steps: []workflow.Step{{ID: "run", Type: "capture", With: map[string]any{"value": "{{ .foreach.item }}"}}},
	}})
	var output bytes.Buffer
	if _, err := New(registry).Run(t.Context(), definition, Options{DryRun: true, Stdout: &output, Stderr: io.Discard}); err != nil {
		t.Fatal(err)
	}
	if want := "1. loop (foreach vars.missing; collect steps.run.value) [max 1, max 10000 iterations, fail fast]\n   1.1 run (capture)\n"; output.String() != want {
		t.Fatalf("output = %q, want %q", output.String(), want)
	}
}

type countingRunner struct {
	value any
	runs  *int
}

func (r countingRunner) Run(_ context.Context, _ step.Request) (step.Result, error) {
	if r.runs != nil {
		(*r.runs)++
	}
	return step.Result{
		Outputs:   map[string]any{"value": r.value},
		Variables: map[string]any{"result": r.value},
	}, nil
}

func (r captureRunner) Run(_ context.Context, request step.Request) (step.Result, error) {
	*r.seen = request
	return step.Result{Outputs: map[string]any{"value": r.value}, Variables: map[string]any{"result": r.value}}, nil
}

func TestRunRendersStateAndEnvironment(t *testing.T) {
	var seen step.Request
	registry := newTestRegistry(t, map[string]step.Builder{"capture": func(raw map[string]any) (step.Runner, error) {
		return captureRunner{value: raw["value"], seen: &seen}, nil
	}})
	definition := testDefinition(t, "test", workflow.Step{ID: "capture", Type: "capture", With: map[string]any{"value": "{{ .vars.name }}:{{ .env.DERIVED }}"}})
	definition.Vars = map[string]any{"name": "workflow"}
	definition.Env = map[string]string{
		"DERIVED":              "{{ .env.WUKO_ENGINE_HOST }}",
		"WUKO_ENGINE_PRIORITY": "workflow",
	}
	state, err := New(registry).Run(t.Context(), definition, Options{
		Vars:    map[string]any{"name": "cli"},
		Env:     map[string]string{"CLI": "yes", "WUKO_ENGINE_PRIORITY": "cli"},
		BaseEnv: map[string]string{"WUKO_ENGINE_HOST": "host-value", "WUKO_ENGINE_PRIORITY": "host"},
		RunDir:  t.TempDir(), Stdout: io.Discard, Stderr: io.Discard,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := state.Vars["result"]; got != "cli:host-value" {
		t.Fatalf("result = %v", got)
	}
	if seen.Env["DERIVED"] != "host-value" || seen.Env["CLI"] != "yes" {
		t.Fatalf("environment = %#v", seen.Env)
	}
	if seen.Env["WUKO_ENGINE_PRIORITY"] != "cli" {
		t.Fatalf("priority = %q", seen.Env["WUKO_ENGINE_PRIORITY"])
	}
}

func TestRunRendersNamedTemplatesWithRuntimeState(t *testing.T) {
	registry := newTestRegistry(t, map[string]step.Builder{"capture": func(raw map[string]any) (step.Runner, error) {
		return countingRunner{value: raw["value"]}, nil
	}})
	definition := testDefinition(t, "named",
		workflow.Step{ID: "prepare", Type: "capture", With: map[string]any{"value": "ready"}},
		workflow.Step{ID: "consume", Type: "capture", With: map[string]any{"value": `{{ template "result" . }}`}},
	)
	definition.Vars = map[string]any{"prefix": "artifact"}
	definition.Templates = map[string]workflow.TemplateDefinition{
		"result": {Inline: `{{ .vars.prefix }}={{ .steps.prepare.value }}`},
	}
	state, err := New(registry).Run(t.Context(), definition, Options{RunDir: t.TempDir(), Stdout: io.Discard, Stderr: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if got := state.Steps["consume"].(map[string]any)["value"]; got != "artifact=ready" {
		t.Fatalf("result = %v", got)
	}
}
