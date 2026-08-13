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
	registry := step.NewRegistry()
	var runs int
	if err := registry.Register("capture", func(raw map[string]any) (step.Runner, error) {
		return countingRunner{value: raw["value"], runs: &runs}, nil
	}); err != nil {
		t.Fatal(err)
	}
	definition := &workflow.Definition{
		Version: 1,
		Name:    "conditional",
		Dir:     t.TempDir(),
		Vars:    map[string]any{"prepare": false},
		Steps: []workflow.Step{
			{ID: "prepare", Type: "capture", If: "vars.prepare", With: map[string]any{"value": "{{ .vars.artifact_path }}"}},
			{ID: "upload", Type: "capture", If: `"prepare" in steps`, With: map[string]any{"value": "{{ .vars.artifact_path }}"}},
			{ID: "fallback", Type: "capture", If: `"prepare" not in steps`, With: map[string]any{"value": "fallback"}},
		},
	}
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
	registry := step.NewRegistry()
	var runs int
	if err := registry.Register("capture", func(raw map[string]any) (step.Runner, error) {
		return countingRunner{value: raw["value"], runs: &runs}, nil
	}); err != nil {
		t.Fatal(err)
	}
	definition := &workflow.Definition{
		Version: 1,
		Name:    "conditional",
		Dir:     t.TempDir(),
		Steps: []workflow.Step{
			{ID: "prepare", Type: "capture", With: map[string]any{"value": true}},
			{
				ID: "consume", Type: "capture",
				If:   `steps.prepare.value && vars.result && env.MODE == "test" && workflow.name == "conditional" && run.dir != ""`,
				With: map[string]any{"value": "consumed"},
			},
		},
	}
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
	registry := step.NewRegistry()
	if err := registry.Register("capture", func(raw map[string]any) (step.Runner, error) {
		return countingRunner{}, nil
	}); err != nil {
		t.Fatal(err)
	}
	tests := []workflow.Condition{"vars.", "42"}
	for _, condition := range tests {
		t.Run(string(condition), func(t *testing.T) {
			definition := &workflow.Definition{
				Version: 1, Name: "invalid", Dir: t.TempDir(),
				Steps: []workflow.Step{{ID: "run", Type: "capture", If: condition, With: map[string]any{}}},
			}
			if err := New(registry).Validate(t.Context(), definition, Options{}); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestRunRejectsNonBooleanConditionAtRuntime(t *testing.T) {
	registry := step.NewRegistry()
	if err := registry.Register("capture", func(raw map[string]any) (step.Runner, error) {
		return countingRunner{}, nil
	}); err != nil {
		t.Fatal(err)
	}
	definition := &workflow.Definition{
		Version: 1, Name: "invalid", Dir: t.TempDir(), Vars: map[string]any{"enabled": "yes"},
		Steps: []workflow.Step{{ID: "run", Type: "capture", If: "vars.enabled", With: map[string]any{}}},
	}
	_, err := New(registry).Run(t.Context(), definition, Options{Stdout: io.Discard, Stderr: io.Discard})
	if err == nil {
		t.Fatal("expected runtime condition error")
	}
	if !strings.Contains(err.Error(), "evaluating if") {
		t.Fatalf("error = %q", err)
	}
}

func TestDryRunPrintsButDoesNotEvaluateCondition(t *testing.T) {
	registry := step.NewRegistry()
	if err := registry.Register("capture", func(raw map[string]any) (step.Runner, error) {
		return countingRunner{}, nil
	}); err != nil {
		t.Fatal(err)
	}
	timeout := workflow.Duration(2 * time.Second)
	definition := &workflow.Definition{
		Version: 1, Name: "dry-run", Dir: t.TempDir(),
		Steps: []workflow.Step{{
			ID: "run", Type: "capture", If: "vars.missing",
			Timeout: &timeout, Retry: immediateRetry(2), With: map[string]any{"value": "{{ .vars.also_missing }}"},
		}},
	}
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
	registry := step.NewRegistry()
	var seen step.Request
	if err := registry.Register("capture", func(raw map[string]any) (step.Runner, error) {
		return captureRunner{value: raw["value"], seen: &seen}, nil
	}); err != nil {
		t.Fatal(err)
	}
	definition := &workflow.Definition{
		Version: 1, Name: "test", Dir: t.TempDir(), Vars: map[string]any{"name": "workflow"},
		Env: map[string]string{
			"DERIVED":              "{{ .env.WUKO_ENGINE_HOST }}",
			"WUKO_ENGINE_PRIORITY": "workflow",
		},
		Steps: []workflow.Step{{ID: "capture", Type: "capture", With: map[string]any{"value": "{{ .vars.name }}:{{ .env.DERIVED }}"}}},
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
