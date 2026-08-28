package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/steps/decode"
	"github.com/up2jj/wuko/steps/extract"
	luastep "github.com/up2jj/wuko/steps/lua"
	"github.com/up2jj/wuko/steps/set"
	"github.com/up2jj/wuko/workflow"
)

func referenceTestRegistry(t *testing.T, runs *int) *step.Registry {
	t.Helper()
	return newTestRegistry(t, map[string]step.Builder{"capture": func(raw map[string]any) (step.Runner, error) {
		return runnerFunc(func(context.Context, step.Request) (step.Result, error) {
			if runs != nil {
				*runs++
			}
			return step.Result{Outputs: map[string]any{"value": raw["value"]}}, nil
		}), nil
	}})
}

func TestReferenceValidationFailsBeforeAnyStepRuns(t *testing.T) {
	for _, test := range []struct {
		name  string
		steps []workflow.Step
		want  string
	}{
		{
			name: "unknown condition variable",
			steps: []workflow.Step{
				{ID: "a", Type: "capture", If: `vars.typooo == "x"`, With: map[string]any{"value": "a"}},
			},
			want: `variable "typooo" is not declared`,
		},
		{
			name: "unknown template step",
			steps: []workflow.Step{
				{ID: "a", Type: "capture", With: map[string]any{"value": "a"}},
				{ID: "b", Type: "capture", With: map[string]any{"value": `{{ .steps.nosuchstep.stdout }}`}},
			},
			want: `step "nosuchstep" is not available here`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			var runs int
			definition := testDefinition(t, test.name, test.steps...)
			state, err := New(referenceTestRegistry(t, &runs)).Run(t.Context(), definition, Options{})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
			if state != nil || runs != 0 {
				t.Fatalf("preflight executed work: state = %#v, runs = %d", state, runs)
			}
		})
	}
}

func TestReferenceValidationChecksConstantKeysAndAllowsDynamicKeys(t *testing.T) {
	registry := referenceTestRegistry(t, nil)
	for _, test := range []struct {
		name   string
		value  string
		ifExpr string
		want   string
	}{
		{name: "declared template field", value: `{{ .vars.known }}`},
		{name: "constant template index", value: `{{ index .vars "known" }}`},
		{name: "dynamic template index", value: `{{ index .vars .vars.key }}`},
		{name: "constant expression index", ifExpr: `vars["known"] == "x"`},
		{name: "dynamic expression index", ifExpr: `vars[vars.key] == "x"`},
		{name: "expression local", ifExpr: `let local = {"ok": true}; local.ok`},
		{name: "unknown template field", value: `{{ .vars.unknown }}`, want: `variable "unknown" is not declared`},
		{name: "unknown constant template index", value: `{{ index .vars "unknown" }}`, want: `variable "unknown" is not declared`},
		{name: "unknown template get", value: `{{ get "unknown" .vars }}`, want: `variable "unknown" is not declared`},
		{name: "unknown piped template get", value: `{{ .vars | get "unknown" }}`, want: `variable "unknown" is not declared`},
		{name: "template hasKey probes an undeclared key", value: `{{ hasKey "unknown" .vars }}`},
		{name: "piped template hasKey probes an undeclared key", value: `{{ .vars | hasKey "unknown" }}`},
		{name: "template hasKey on an unknown container", value: `{{ hasKey "unknown" .vars.unknown }}`, want: `variable "unknown" is not declared`},
		{name: "unknown constant expression index", ifExpr: `vars["unknown"] == "x"`, want: `variable "unknown" is not declared`},
		{name: "unknown expression get", ifExpr: `get(vars, "unknown") == nil`, want: `variable "unknown" is not declared`},
		{name: "expression hasKey probes an undeclared key", ifExpr: `hasKey(vars, "unknown")`},
		{name: "expression hasKey on an unknown container", ifExpr: `hasKey(vars.unknown, "key")`, want: `variable "unknown" is not declared`},
		{name: "undeclared environment value", value: `{{ .env.WUKO_UNSET_ANYWHERE }}`},
		{name: "unknown expression root", ifExpr: `varz.unknown == nil`, want: `data root "varz" is not available here`},
	} {
		t.Run(test.name, func(t *testing.T) {
			definition := testDefinition(t, test.name, workflow.Step{
				ID: "use", Type: "capture", If: workflow.Condition(test.ifExpr), With: map[string]any{"value": test.value},
			})
			definition.Vars = map[string]any{"known": "x", "key": "runtime_key"}
			err := New(registry).Validate(t.Context(), definition, Options{})
			if test.want == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}

	t.Run("invocation variable", func(t *testing.T) {
		definition := testDefinition(t, "invocation", workflow.Step{ID: "use", Type: "capture", With: map[string]any{"value": `{{ .vars.provided }}`}})
		if err := New(registry).Validate(t.Context(), definition, Options{Vars: map[string]any{"provided": "value"}}); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("dependency contract", func(t *testing.T) {
		definition := testDefinition(t, "dependency", workflow.Step{ID: "use", Type: "capture", With: map[string]any{"value": `{{ .dependencies.build.artifact.path }}`}})
		dependencies := map[string]map[string]any{"build": {"artifact": nil}}
		if err := New(registry).Validate(t.Context(), definition, Options{Dependencies: dependencies}); err != nil {
			t.Fatal(err)
		}
	})
}

func TestReferenceValidationTracksStepVisibility(t *testing.T) {
	registry := referenceTestRegistry(t, nil)
	foreach := func() workflow.Step {
		return workflow.Step{ID: "group", Foreach: &workflow.ForeachGroup{
			Items: "[1]", MaxConcurrency: 1, FailFast: true,
			Steps: []workflow.Step{{ID: "child", Type: "capture", With: map[string]any{"value": "child"}}},
		}}
	}
	for _, test := range []struct {
		name  string
		steps []workflow.Step
		want  string
	}{
		{
			name: "later step",
			steps: []workflow.Step{
				{ID: "early", Type: "capture", With: map[string]any{"value": `{{ .steps.later.value }}`}},
				{ID: "later", Type: "capture", With: map[string]any{"value": "later"}},
			},
			want: `step "later" is not available here`,
		},
		{
			name: "concurrent sibling",
			steps: []workflow.Step{{Concurrent: &workflow.ConcurrentGroup{MaxConcurrency: 2, FailFast: true, Steps: []workflow.Step{
				{ID: "producer", Type: "capture", With: map[string]any{"value": "ready"}},
				{ID: "consumer", Type: "capture", With: map[string]any{"value": `{{ .steps.producer.value }}`}},
			}}}},
			want: `step "producer" is not available here`,
		},
		{
			name: "concurrent merge",
			steps: []workflow.Step{
				{Concurrent: &workflow.ConcurrentGroup{MaxConcurrency: 2, FailFast: true, Steps: []workflow.Step{
					{ID: "one", Type: "capture", With: map[string]any{"value": "one"}},
					{ID: "two", Type: "capture", With: map[string]any{"value": "two"}},
				}}},
				{ID: "after", Type: "capture", With: map[string]any{"value": `{{ .steps.one.value }}{{ .steps.two.value }}`}},
			},
		},
		{
			name:  "fanout child remains private",
			steps: []workflow.Step{foreach(), {ID: "after", Type: "capture", With: map[string]any{"value": `{{ .steps.child.value }}`}}},
			want:  `step "child" is not available here`,
		},
		{
			name:  "fanout parent is visible",
			steps: []workflow.Step{foreach(), {ID: "after", Type: "capture", With: map[string]any{"value": `{{ .steps.group.count }}`}}},
		},
		{
			name: "loop body flows forward",
			steps: []workflow.Step{
				{ID: "repeat", Loop: &workflow.LoopGroup{Until: `steps.tick.value == "done"`, MaxIterations: 1, Steps: []workflow.Step{{ID: "tick", Type: "capture", With: map[string]any{"value": "done"}}}}},
				{ID: "after", Type: "capture", With: map[string]any{"value": `{{ .steps.tick.value }}:{{ .steps.repeat.count }}`}},
			},
		},
		{
			name: "worktree body flows forward",
			steps: []workflow.Step{
				{ID: "tree", Worktree: &workflow.WorktreeGroup{Revision: "HEAD", Path: "auto", Steps: []workflow.Step{{ID: "inside", Type: "capture", With: map[string]any{"value": "inside"}}}}},
				{ID: "after", Type: "capture", With: map[string]any{"value": `{{ .steps.inside.value }}:{{ .steps.tree.path }}`}},
			},
		},
		{
			name: "try child remains private",
			steps: []workflow.Step{
				{ID: "attempt", Try: &workflow.TryBlock{Steps: []workflow.Step{{ID: "inside", Type: "capture", With: map[string]any{"value": "inside"}}}}, Catch: &workflow.CatchBlock{Steps: []workflow.Step{{ID: "recover", Type: "capture", With: map[string]any{"value": "recover"}}}}},
				{ID: "after", Type: "capture", With: map[string]any{"value": `{{ .steps.inside.value }}`}},
			},
			want: `step "inside" is not available here`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			definition := testDefinition(t, test.name, test.steps...)
			err := New(registry).Validate(t.Context(), definition, Options{})
			if test.want == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestReferenceValidationTracksCleanupLifecycleAndActions(t *testing.T) {
	registry := referenceTestRegistry(t, nil)

	t.Run("defer sees completed sequence", func(t *testing.T) {
		definition := testDefinition(t, "defer",
			workflow.Step{ID: "first", Type: "capture", With: map[string]any{"value": "first"}, Defer: []workflow.Step{{ID: "cleanup", Type: "capture", With: map[string]any{"value": `{{ .steps.later.value }}`}}}},
			workflow.Step{ID: "later", Type: "capture", With: map[string]any{"value": "later"}},
		)
		if err := New(registry).Validate(t.Context(), definition, Options{}); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("finally sees main steps", func(t *testing.T) {
		definition := testDefinition(t, "finally", workflow.Step{ID: "main", Type: "capture", With: map[string]any{"value": "main"}})
		definition.Finally = []workflow.Step{{ID: "cleanup", Type: "capture", With: map[string]any{"value": `{{ .steps.main.value }}:{{ .finally.status }}`}}}
		if err := New(registry).Validate(t.Context(), definition, Options{}); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("lifecycle is isolated", func(t *testing.T) {
		definition := testDefinition(t, "lifecycle", workflow.Step{ID: "main", Type: "capture", With: map[string]any{"value": "main"}})
		definition.Install = []workflow.Step{{ID: "install", Type: "capture", With: map[string]any{"value": `{{ .steps.main.value }}`}}}
		err := New(registry).Validate(t.Context(), definition, Options{})
		if err == nil || !strings.Contains(err.Error(), `step "main" is not available here`) {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("action inputs are closed", func(t *testing.T) {
		action := testAction(t, "inner", workflow.Step{ID: "inner", Type: "capture", With: map[string]any{"value": `{{ .inputs.typo }}`}})
		action.Inputs = map[string]workflow.ActionInput{"name": {Type: "string"}}
		definition := testDefinition(t, "caller", workflow.Step{ID: "call", Uses: workflow.ActionSource{URL: "https://example.test/action"}, Action: action, With: map[string]any{"name": "ok"}})
		err := New(registry).Validate(t.Context(), definition, Options{})
		if err == nil || !strings.Contains(err.Error(), `input "typo" is not declared`) {
			t.Fatalf("error = %v", err)
		}
	})
}

func TestReferenceValidationChecksNamedTemplatesGloballyAndAtUsePoint(t *testing.T) {
	registry := referenceTestRegistry(t, nil)

	t.Run("unused template", func(t *testing.T) {
		definition := testDefinition(t, "named", workflow.Step{ID: "only", Type: "capture", With: map[string]any{"value": "only"}})
		definition.Templates = map[string]workflow.TemplateDefinition{"unused": {Inline: `{{ .steps.missing.value }}`}}
		err := New(registry).Validate(t.Context(), definition, Options{})
		if err == nil || !strings.Contains(err.Error(), `template "unused": step "missing" is not available here`) {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("invocation scope", func(t *testing.T) {
		definition := testDefinition(t, "named",
			workflow.Step{ID: "early", Type: "capture", With: map[string]any{"value": `{{ template "show" . }}`}},
			workflow.Step{ID: "later", Type: "capture", With: map[string]any{"value": "later"}},
		)
		definition.Templates = map[string]workflow.TemplateDefinition{"show": {Inline: `{{ .steps.later.value }}`}}
		err := New(registry).Validate(t.Context(), definition, Options{})
		if err == nil || !strings.Contains(err.Error(), `step "later" is not available here`) {
			t.Fatalf("error = %v", err)
		}
	})
}

func TestReferenceValidationLeavesSkippedEarlierStepHandlingAtRuntime(t *testing.T) {
	var runs int
	definition := testDefinition(t, "skipped",
		workflow.Step{ID: "optional", Type: "capture", If: "false", With: map[string]any{"value": "optional"}},
		workflow.Step{ID: "consumer", Type: "capture", With: map[string]any{"value": `{{ .steps.optional.value }}`}},
	)
	engine := New(referenceTestRegistry(t, &runs))
	if err := engine.Validate(t.Context(), definition, Options{}); err != nil {
		t.Fatal(err)
	}
	state, err := engine.Run(t.Context(), definition, Options{})
	if err == nil || !strings.Contains(err.Error(), `map has no entry for key "optional"`) {
		t.Fatalf("error = %v", err)
	}
	if state != nil || runs != 0 {
		t.Fatalf("runtime handling changed: state = %#v, runs = %d", state, runs)
	}
}

func TestReferenceValidationSeesVariablesWrittenBySteps(t *testing.T) {
	registry := step.NewRegistry()
	if err := registry.Register("capture", func(raw map[string]any) (step.Runner, error) {
		return runnerFunc(func(context.Context, step.Request) (step.Result, error) {
			return step.Result{Outputs: map[string]any{"value": raw["value"]}}, nil
		}), nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, register := range []func(*step.Registry) error{set.Register, extract.Register, luastep.Register} {
		if err := register(registry); err != nil {
			t.Fatal(err)
		}
	}
	assign := workflow.Step{ID: "assign", Type: "set", With: map[string]any{"variable": "greeting", "expr": `"hi"`}}

	t.Run("set declares its variable", func(t *testing.T) {
		definition := testDefinition(t, "set", assign,
			workflow.Step{ID: "use", Type: "capture", With: map[string]any{"value": `{{ .vars.greeting }}`}},
		)
		if err := New(registry).Validate(t.Context(), definition, Options{}); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("extract declares its targets", func(t *testing.T) {
		definition := testDefinition(t, "extract",
			workflow.Step{ID: "parse", Type: "extract", With: map[string]any{
				"text": "v1", "pattern": `(?P<version>v\d+)`, "variables": map[string]any{"version": "release"},
			}},
			workflow.Step{ID: "use", Type: "capture", With: map[string]any{"value": `{{ .vars.release }}`}},
		)
		if err := New(registry).Validate(t.Context(), definition, Options{}); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("lua stops variable validation", func(t *testing.T) {
		definition := testDefinition(t, "lua",
			workflow.Step{ID: "script", Type: "lua", With: map[string]any{"source": "return {}"}},
			workflow.Step{ID: "use", Type: "capture", With: map[string]any{"value": `{{ .vars.decided_at_runtime }}`}},
		)
		if err := New(registry).Validate(t.Context(), definition, Options{}); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("concurrent and try bodies commit their writes", func(t *testing.T) {
		definition := testDefinition(t, "nested",
			workflow.Step{ID: "guarded",
				Try:   &workflow.TryBlock{Steps: []workflow.Step{{ID: "risky", Type: "set", With: map[string]any{"variable": "from_try", "expr": `"t"`}}}},
				Catch: &workflow.CatchBlock{Steps: []workflow.Step{{ID: "recover", Type: "capture", With: map[string]any{"value": "recovered"}}}},
			},
			workflow.Step{Concurrent: &workflow.ConcurrentGroup{MaxConcurrency: 2, Steps: []workflow.Step{
				{ID: "alpha", Type: "set", With: map[string]any{"variable": "from_concurrent", "expr": `"c"`}},
				{ID: "beta", Type: "capture", With: map[string]any{"value": "beta"}},
			}}},
			workflow.Step{ID: "report", Type: "capture", With: map[string]any{"value": `{{ .vars.from_try }}{{ .vars.from_concurrent }}`}},
		)
		if err := New(registry).Validate(t.Context(), definition, Options{}); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("iteration writes stay in the fanout", func(t *testing.T) {
		definition := testDefinition(t, "fanout",
			workflow.Step{ID: "fan", Foreach: &workflow.ForeachGroup{Items: `["a"]`, MaxConcurrency: 1, Steps: []workflow.Step{
				{ID: "each", Type: "set", With: map[string]any{"variable": "seen", "expr": "foreach.item"}},
			}}},
			workflow.Step{ID: "after", Type: "capture", With: map[string]any{"value": `{{ .vars.seen }}`}},
		)
		err := New(registry).Validate(t.Context(), definition, Options{})
		if err == nil || !strings.Contains(err.Error(), `variable "seen" is not declared`) {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("other variables stay declared", func(t *testing.T) {
		definition := testDefinition(t, "typo", assign,
			workflow.Step{ID: "use", Type: "capture", With: map[string]any{"value": `{{ .vars.greetign }}`}},
		)
		err := New(registry).Validate(t.Context(), definition, Options{})
		if err == nil || !strings.Contains(err.Error(), `variable "greetign" is not declared`) {
			t.Fatalf("error = %v", err)
		}
	})
}

func TestReferenceValidationDefersTemplatedLookupsToRuntime(t *testing.T) {
	registry := step.NewRegistry()
	if err := decode.Register(registry); err != nil {
		t.Fatal(err)
	}
	definition := testDefinition(t, "lookup", workflow.Step{
		ID: "read", Type: "decode", With: map[string]any{"format": "json", "from": "steps.{{ .vars.producer }}.stdout"},
	})
	definition.Vars = map[string]any{"producer": "build"}
	if err := New(registry).Validate(t.Context(), definition, Options{}); err != nil {
		t.Fatal(err)
	}
}

func TestReferenceValidationEndsTheFinallyBindingWithTheDeferredSteps(t *testing.T) {
	registry := referenceTestRegistry(t, nil)
	definition := testDefinition(t, "defer-scope", workflow.Step{
		ID: "main", Type: "capture", With: map[string]any{"value": "main"},
		Defer: []workflow.Step{{ID: "cleanup", Type: "capture", With: map[string]any{"value": `{{ .finally.status }}`}}},
	})
	definition.Outputs = map[string]workflow.WorkflowOutput{"status": {Type: "string", Value: "finally.status"}}
	err := New(registry).Validate(t.Context(), definition, Options{})
	if err == nil || !strings.Contains(err.Error(), `data root "finally" is not available here`) {
		t.Fatalf("error = %v", err)
	}
}
