package engine_test

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/up2jj/wuko/engine"
	"github.com/up2jj/wuko/internal/steptest"
	"github.com/up2jj/wuko/process/processtest"
	"github.com/up2jj/wuko/step"
	assertstep "github.com/up2jj/wuko/steps/assert"
	"github.com/up2jj/wuko/steps/choice"
	"github.com/up2jj/wuko/steps/set"
	"github.com/up2jj/wuko/steps/shell"
	"github.com/up2jj/wuko/workflow"
)

func bindingEngine(t *testing.T) *engine.Engine {
	t.Helper()
	registry := step.NewRegistry()
	for _, register := range []func(*step.Registry) error{shell.Register, assertstep.Register, choice.Register, set.Register} {
		if err := register(registry); err != nil {
			t.Fatal(err)
		}
	}
	return engine.New(registry)
}

func TestStepBindingsResolveAtRuntime(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		args any
		want []string
	}{
		{"elements", []any{map[string]any{"expr": `join(filter(vars.selected, # in vars.running), " ")`}, "{{ .vars.name }}"}, []string{"api worker", "hello"}},
		{"whole list", map[string]any{"expr": `filter(vars.selected, # in vars.running)`}, []string{"api", "worker"}},
		{"result is not rendered", []any{map[string]any{"expr": `"{" + "{ .vars.missing }}"`}}, []string{"{{ .vars.missing }}"}},
		{"literal", []any{map[string]any{"literal": "{{ .vars.missing }}"}}, []string{"{{ .vars.missing }}"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			definition := steptest.Definition(t, "bindings", workflow.Step{ID: "out", Type: "shell", With: map[string]any{"command": "echo", "args": tt.args}})
			definition.Vars = map[string]any{"selected": []any{"api", "web", "worker"}, "running": []any{"api", "worker"}, "name": "hello"}
			executor := &processtest.Executor{}
			_, err := bindingEngine(t).Run(t.Context(), definition, engine.Options{RunDir: t.TempDir(), Executor: executor, Stdout: io.Discard, Stderr: io.Discard})
			if err != nil {
				t.Fatal(err)
			}
			if len(executor.Calls()) != 1 {
				t.Fatalf("calls=%d", len(executor.Calls()))
			}
			if diff := cmp.Diff(tt.want, executor.Last().Args); diff != "" {
				t.Fatalf("args (-want +got):\n%s", diff)
			}
		})
	}
}

func TestStepBindingsValidateBeforeExecution(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		with map[string]any
		want string
	}{
		{"unknown reference", map[string]any{"command": "echo", "args": []any{map[string]any{"expr": "steps.missing.stdout"}}}, `step "missing"`},
		{"syntax", map[string]any{"command": "echo", "args": map[string]any{"expr": "vars."}}, "compiling expr"},
		{"empty", map[string]any{"command": "echo", "args": map[string]any{"expr": " "}}, "non-empty string"},
		{"nonstring", map[string]any{"command": "echo", "args": []any{map[string]any{"expr": "42"}}}, "want string"},
		{"static field", map[string]any{"command": "echo", "replace": map[string]any{"expr": "false"}}, "must be static"},
		{"unknown field", map[string]any{"command": "echo", "typo": true, "args": map[string]any{"expr": "vars.arguments"}}, "field typo not found"},
		{"static enum", map[string]any{"command": "echo", "stdout": "invalid", "args": map[string]any{"expr": "vars.arguments"}}, "stdout"},
		{"missing command", map[string]any{"args": map[string]any{"expr": "vars.arguments"}}, "exactly one of command or script"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			definition := steptest.Definition(t, "validation", workflow.Step{ID: "out", Type: "shell", With: tt.with})
			definition.Vars = map[string]any{"arguments": []any{"ok"}}
			executor := &processtest.Executor{}
			_, err := bindingEngine(t).Run(t.Context(), definition, engine.Options{RunDir: t.TempDir(), Executor: executor, Stdout: io.Discard, Stderr: io.Discard})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Run() error = %v, want %q", err, tt.want)
			}
			if len(executor.Calls()) != 0 {
				t.Fatal("executed before validation failed")
			}
		})
	}
}

func TestAssertExpressionMessagesAreLazy(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct{ name, assert, source, want string }{
		{"pass", "true", `vars.problem.detail`, ""},
		{"fail", "false", `"missing: " + join(vars.missing, ", ")`, "assertion failed: missing: api, web"},
		{"empty", "false", `""`, "assertion failed: false"},
		{"result not rendered", "false", `"{" + "{ .vars.unknown }}"`, "assertion failed: {{ .vars.unknown }}"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			definition := steptest.Definition(t, "assert", workflow.Step{ID: "check", Type: "assert", With: map[string]any{"expr": tt.assert, "message": map[string]any{"expr": tt.source}}})
			definition.Vars = map[string]any{"problem": "", "missing": []any{"api", "web"}}
			_, err := bindingEngine(t).Run(t.Context(), definition, engine.Options{RunDir: t.TempDir(), Stdout: io.Discard, Stderr: io.Discard})
			if tt.want == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error=%v, want %q", err, tt.want)
			}
		})
	}
}

func TestChoiceFromExpressionPreservesList(t *testing.T) {
	t.Parallel()
	definition := steptest.Definition(t, "choice", workflow.Step{ID: "pick", Type: "tui_choice", With: map[string]any{"variable": "service", "message": "Select", "from": map[string]any{"expr": `filter(vars.services, # != "prod")`}, "default_expr": "true", "noninteractive": "default"}})
	definition.Vars = map[string]any{"services": []any{"dev", "prod"}}
	state, err := bindingEngine(t).Run(t.Context(), definition, engine.Options{RunDir: t.TempDir(), Stdout: io.Discard, Stderr: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if state.Vars["service"] != "dev" {
		t.Fatalf("selection=%#v", state.Vars["service"])
	}
}

func TestLegacyPluginConfigurationIsNotInterpretedAsBindings(t *testing.T) {
	t.Parallel()
	registry := step.NewRegistry()
	var captured map[string]any
	registry.SetResolver(func(_ context.Context, name string, raw map[string]any) (step.Runner, error) {
		captured = raw
		return steptest.RunnerFunc(func(context.Context, step.Request) (step.Result, error) { return step.Result{}, nil }), nil
	})
	definition := steptest.Definition(t, "plugin", workflow.Step{ID: "plugin", Type: "acme.echo", With: map[string]any{"payload": map[string]any{"expr": "request.body"}, "literal": map[string]any{"literal": "{{ .vars.name }}"}}})
	definition.Vars = map[string]any{"name": "rendered"}
	_, err := engine.New(registry).Run(t.Context(), definition, engine.Options{RunDir: t.TempDir(), Stdout: io.Discard, Stderr: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"payload": map[string]any{"expr": "request.body"}, "literal": map[string]any{"literal": "rendered"}}
	if diff := cmp.Diff(want, captured); diff != "" {
		t.Fatalf("plugin config (-want +got):\n%s", diff)
	}
}

func TestSkippedStepDoesNotEvaluateBindings(t *testing.T) {
	t.Parallel()
	definition := steptest.Definition(t, "skipped", workflow.Step{ID: "out", Type: "shell", If: "false", With: map[string]any{"command": "echo", "args": []any{map[string]any{"expr": "vars.arguments[99]"}}}})
	definition.Vars = map[string]any{"arguments": []any{}}
	executor := &processtest.Executor{}
	if _, err := bindingEngine(t).Run(t.Context(), definition, engine.Options{RunDir: t.TempDir(), Executor: executor, Stdout: io.Discard, Stderr: io.Discard}); err != nil {
		t.Fatal(err)
	}
	if len(executor.Calls()) != 0 {
		t.Fatal("skipped step executed")
	}
}

func TestStepBindingRuntimeTypesAreCheckedBeforeRunning(t *testing.T) {
	t.Parallel()
	definition := steptest.Definition(t, "types", workflow.Step{ID: "out", Type: "shell", With: map[string]any{"command": "echo", "args": map[string]any{"expr": "vars.arguments"}}})
	definition.Vars = map[string]any{"arguments": []any{42}}
	subject := bindingEngine(t)
	if err := subject.Validate(t.Context(), definition, engine.Options{RunDir: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	executor := &processtest.Executor{}
	_, err := subject.Run(t.Context(), definition, engine.Options{RunDir: t.TempDir(), Executor: executor, Stdout: io.Discard, Stderr: io.Discard})
	if err == nil || !strings.Contains(err.Error(), "want string") {
		t.Fatalf("Run() error = %v", err)
	}
	if len(executor.Calls()) != 0 {
		t.Fatal("ran with invalid argument types")
	}
}

func TestStepBindingsPreserveNestedTypesAndLiteralObjects(t *testing.T) {
	t.Parallel()
	definition := steptest.Definition(t, "types", workflow.Step{ID: "data", Type: "set", With: map[string]any{"variable": "result", "value": map[string]any{
		"active":   map[string]any{"expr": "true"},
		"id":       map[string]any{"expr": "7"},
		"type":     map[string]any{"expr": `"service"`},
		"names":    map[string]any{"expr": "vars.names"},
		"returned": map[string]any{"expr": "vars.object"},
		"literal":  map[string]any{"literal": map[string]any{"expr": "not an expression", "text": "{{ .vars.unknown }}"}},
	}}})
	definition.Vars = map[string]any{"names": []any{"a", "b"}, "object": map[string]any{"expr": "not an expression"}}
	state, err := bindingEngine(t).Run(t.Context(), definition, engine.Options{RunDir: t.TempDir(), Stdout: io.Discard, Stderr: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"active": true, "id": 7, "type": "service", "names": []any{"a", "b"}, "returned": map[string]any{"expr": "not an expression"}, "literal": map[string]any{"expr": "not an expression", "text": "{{ .vars.unknown }}"}}
	if diff := cmp.Diff(want, state.Vars["result"]); diff != "" {
		t.Fatalf("value (-want +got):\n%s", diff)
	}
}

func TestAssertLiteralMessageIsNotRendered(t *testing.T) {
	t.Parallel()
	definition := steptest.Definition(t, "assert", workflow.Step{ID: "check", Type: "assert", With: map[string]any{"expr": "false", "message": map[string]any{"literal": "{{ .vars.unknown"}}})
	subject := bindingEngine(t)
	if err := subject.Validate(t.Context(), definition, engine.Options{RunDir: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	_, err := subject.Run(t.Context(), definition, engine.Options{RunDir: t.TempDir(), Stdout: io.Discard, Stderr: io.Discard})
	if err == nil || !strings.Contains(err.Error(), "assertion failed: {{ .vars.unknown") {
		t.Fatalf("error=%v", err)
	}
}
