package engine

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/workflow"
)

type environmentCaptureRunner struct {
	name     string
	expected string
}

func (runner environmentCaptureRunner) Validate(_ context.Context, request step.Request) error {
	if runner.name == "" {
		return fmt.Errorf("environment name is required")
	}
	if runner.expected != "" && request.Env[runner.name] != runner.expected {
		return fmt.Errorf("environment %s = %q, want %q", runner.name, request.Env[runner.name], runner.expected)
	}
	return nil
}

func (runner environmentCaptureRunner) Run(_ context.Context, request step.Request) (step.Result, error) {
	return step.Result{Outputs: map[string]any{"value": request.Env[runner.name]}}, nil
}

func environmentRegistry(t *testing.T) *step.Registry {
	t.Helper()
	registry := newTestRegistry(t, map[string]step.Builder{
		"capture_env": func(raw map[string]any) (step.Runner, error) {
			name, _ := raw["name"].(string)
			expected, _ := raw["expected"].(string)
			return environmentCaptureRunner{name: name, expected: expected}, nil
		},
		"literal": func(raw map[string]any) (step.Runner, error) {
			return countingRunner{value: raw["value"]}, nil
		},
		"fail": func(map[string]any) (step.Runner, error) {
			return runnerFunc(func(context.Context, step.Request) (step.Result, error) {
				return step.Result{}, fmt.Errorf("failed")
			}), nil
		},
	})
	return registry
}

func TestEnvironmentBlockScopesRequestValidation(t *testing.T) {
	definition := &workflow.Definition{Version: 1, Name: "validation", Steps: []workflow.Step{{
		Env: workflow.Environment{"MODE": "scoped"}, Steps: []workflow.Step{{
			ID: "inside", Type: "capture_env", With: map[string]any{"name": "MODE", "expected": "scoped"},
		}},
	}}}
	if err := New(environmentRegistry(t)).Validate(t.Context(), definition, Options{
		BaseEnv: map[string]string{"MODE": "outer"}, RunDir: t.TempDir(), Stdout: io.Discard, Stderr: io.Discard,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestEnvironmentBlockRestoresEnvironmentBeforeFinallyAfterFailure(t *testing.T) {
	registry := environmentRegistry(t)
	var finallyMode string
	if err := registry.Register("capture_finally_env", func(map[string]any) (step.Runner, error) {
		return runnerFunc(func(_ context.Context, request step.Request) (step.Result, error) {
			finallyMode = request.Env["MODE"]
			return step.Result{}, nil
		}), nil
	}); err != nil {
		t.Fatal(err)
	}
	definition := &workflow.Definition{
		Version: 1, Name: "failure", Steps: []workflow.Step{{
			Env: workflow.Environment{"MODE": "scoped"}, Steps: []workflow.Step{{ID: "fail", Type: "fail", With: map[string]any{}}},
		}},
		Finally: []workflow.Step{{ID: "cleanup", Type: "capture_finally_env", With: map[string]any{}}},
	}
	_, err := New(registry).Run(t.Context(), definition, Options{
		BaseEnv: map[string]string{"MODE": "outer"}, RunDir: t.TempDir(), Stdout: io.Discard, Stderr: io.Discard,
	})
	if err == nil {
		t.Fatal("expected failure")
	}
	if finallyMode != "outer" {
		t.Fatalf("finally environment = %q", finallyMode)
	}
}

func envCapture(id, name string) workflow.Step {
	return workflow.Step{ID: id, Type: "capture_env", With: map[string]any{"name": name}}
}

func TestEnvironmentBlockScopesNestsAndRestoresEnvironment(t *testing.T) {
	definition := &workflow.Definition{
		Version: 1, Name: "scoped", Env: workflow.Environment{"MODE": "workflow"},
		Steps: []workflow.Step{
			{ID: "prepare", Type: "literal", With: map[string]any{"value": "ready"}},
			{Env: workflow.Environment{"MODE": "outer", "DERIVED": "{{ .steps.prepare.value }}:{{ .env.BASE }}", "SIBLING": "{{ .env.MODE }}"}, Steps: []workflow.Step{
				envCapture("outer", "MODE"),
				{ID: "derived", Type: "capture_env", With: map[string]any{"name": "DERIVED", "expected": "ready:host"}},
				envCapture("sibling", "SIBLING"),
				{Env: workflow.Environment{"MODE": "inner"}, Steps: []workflow.Step{envCapture("inner", "MODE")}},
				envCapture("restored_outer", "MODE"),
			}},
			envCapture("outside", "MODE"),
		},
		Finally: []workflow.Step{envCapture("finally", "MODE")},
	}
	state, err := New(environmentRegistry(t)).Run(t.Context(), definition, Options{
		BaseEnv: map[string]string{"BASE": "host", "MODE": "host"},
		Env:     map[string]string{"MODE": "invocation"},
		RunDir:  t.TempDir(), Stdout: io.Discard, Stderr: io.Discard,
	})
	if err != nil {
		t.Fatal(err)
	}
	wants := map[string]string{
		"outer": "outer", "derived": "ready:host", "sibling": "invocation", "inner": "inner",
		"restored_outer": "outer", "outside": "invocation", "finally": "invocation",
	}
	for id, want := range wants {
		if got := state.Steps[id].(map[string]any)["value"]; got != want {
			t.Errorf("%s = %#v, want %q", id, got, want)
		}
	}
	if state.Env["MODE"] != "invocation" {
		t.Fatalf("final environment = %#v", state.Env)
	}
}

func TestEnvironmentBlockPreservesEnvironmentForDeferredSteps(t *testing.T) {
	owner := envCapture("owner", "MODE")
	owner.Defer = []workflow.Step{envCapture("cleanup", "MODE")}
	definition := &workflow.Definition{
		Version: 1, Name: "deferred", Steps: []workflow.Step{
			{Env: workflow.Environment{"MODE": "scoped"}, Steps: []workflow.Step{owner}},
			envCapture("outside", "MODE"),
		},
	}
	state, err := New(environmentRegistry(t)).Run(t.Context(), definition, Options{
		BaseEnv: map[string]string{"MODE": "outer"}, RunDir: t.TempDir(), Stdout: io.Discard, Stderr: io.Discard,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := state.Steps["cleanup"].(map[string]any)["value"]; got != "scoped" {
		t.Fatalf("cleanup environment = %#v", got)
	}
	if got := state.Steps["outside"].(map[string]any)["value"]; got != "outer" {
		t.Fatalf("outside environment = %#v", got)
	}
}

func TestEnvironmentBlockIsolatedAcrossConcurrentBranches(t *testing.T) {
	definition := &workflow.Definition{Version: 1, Name: "parallel", Steps: []workflow.Step{{
		Concurrent: &workflow.ConcurrentGroup{MaxConcurrency: 2, FailFast: true, Steps: []workflow.Step{
			{Env: workflow.Environment{"MODE": "one"}, Steps: []workflow.Step{envCapture("one", "MODE")}},
			{Env: workflow.Environment{"MODE": "two"}, Steps: []workflow.Step{envCapture("two", "MODE")}},
		}},
	}}}
	state, err := New(environmentRegistry(t)).Run(t.Context(), definition, Options{
		BaseEnv: map[string]string{"MODE": "outer"}, RunDir: t.TempDir(), Stdout: io.Discard, Stderr: io.Discard,
	})
	if err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]string{"one": "one", "two": "two"} {
		if got := state.Steps[id].(map[string]any)["value"]; got != want {
			t.Errorf("%s = %#v, want %q", id, got, want)
		}
	}
}

func TestEnvironmentBlockPropagatesIntoFanoutControls(t *testing.T) {
	definition := &workflow.Definition{Version: 1, Name: "fanout", Steps: []workflow.Step{{
		Env: workflow.Environment{"MODE": "scoped"}, Steps: []workflow.Step{
			{ID: "each", Foreach: &workflow.ForeachGroup{
				Items: "[1]", Collect: "steps.foreach_child.value", MaxConcurrency: 1, FailFast: true,
				Steps: []workflow.Step{envCapture("foreach_child", "MODE")},
			}},
			{ID: "matrix", Matrix: &workflow.MatrixGroup{
				Axes: workflow.MatrixAxes{{Name: "os", Values: []any{"linux"}}}, Collect: "steps.matrix_child.value", MaxConcurrency: 1, FailFast: true,
				Steps: []workflow.Step{envCapture("matrix_child", "MODE")},
			}},
		},
	}}}
	state, err := New(environmentRegistry(t)).Run(t.Context(), definition, Options{
		BaseEnv: map[string]string{"MODE": "outer"}, RunDir: t.TempDir(), Stdout: io.Discard, Stderr: io.Discard,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"each", "matrix"} {
		results := state.Steps[id].(map[string]any)["results"].([]any)
		if len(results) != 1 || results[0] != "scoped" {
			t.Errorf("%s results = %#v", id, results)
		}
	}
}

func TestEnvironmentBlockPropagatesIntoCompositeAction(t *testing.T) {
	action := &workflow.Action{
		Version: 1, Name: "capture", Outputs: map[string]workflow.ActionOutput{"value": {Value: "steps.inside.value"}},
		Steps: []workflow.Step{envCapture("inside", "MODE")},
	}
	definition := &workflow.Definition{Version: 1, Name: "caller", Steps: []workflow.Step{{
		Env: workflow.Environment{"MODE": "scoped"}, Steps: []workflow.Step{{
			ID: "call", Uses: workflow.ActionSource{URL: "https://example.test/action"}, Action: action, With: map[string]any{},
		}},
	}}}
	state, err := New(environmentRegistry(t)).Run(t.Context(), definition, Options{
		BaseEnv: map[string]string{"MODE": "outer"}, RunDir: t.TempDir(), Stdout: io.Discard, Stderr: io.Discard,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := state.Steps["call"].(map[string]any)["value"]; got != "scoped" {
		t.Fatalf("action environment = %#v", got)
	}
}

func TestEnvironmentBlockReportsRenderedVariable(t *testing.T) {
	definition := &workflow.Definition{Version: 1, Name: "invalid", Steps: []workflow.Step{{
		Env: workflow.Environment{"BROKEN": `{{ template "missing" . }}`}, Steps: []workflow.Step{envCapture("inside", "BROKEN")},
	}}}
	_, err := New(environmentRegistry(t)).Run(t.Context(), definition, Options{RunDir: t.TempDir(), Stdout: io.Discard, Stderr: io.Discard})
	if err == nil || !strings.Contains(err.Error(), "BROKEN") || !strings.Contains(err.Error(), "template") {
		t.Fatalf("error = %v", err)
	}
}

func TestEnvironmentBlockDryRunListsNamesWithoutValues(t *testing.T) {
	definition := testDefinition(t, "dry", workflow.Step{
		Env: workflow.Environment{"TOKEN": "secret", "GOOS": "linux"}, Steps: []workflow.Step{envCapture("run", "GOOS")},
	})
	var output strings.Builder
	if _, err := New(environmentRegistry(t)).Run(t.Context(), definition, Options{DryRun: true, Stdout: &output, Stderr: io.Discard}); err != nil {
		t.Fatal(err)
	}
	if want := "1. env: GOOS, TOKEN\n   1.1 run (capture_env)\n"; output.String() != want {
		t.Fatalf("output = %q, want %q", output.String(), want)
	}
	if strings.Contains(output.String(), "secret") {
		t.Fatalf("dry run exposed environment value: %q", output.String())
	}
}
