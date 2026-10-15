package engine

import (
	"context"
	"errors"
	"io"
	"maps"
	"reflect"
	"strings"
	"testing"

	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/steps/shell"
	"github.com/up2jj/wuko/workflow"
	"gopkg.in/yaml.v3"
)

func scopedEnvironment(t *testing.T, body string) workflow.ScopedEnvironment {
	t.Helper()
	var environment workflow.ScopedEnvironment
	if err := yaml.Unmarshal([]byte(body), &environment); err != nil {
		t.Fatal(err)
	}
	return environment
}

func TestEnvironmentOperationsResolveInOrder(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		body string
		want map[string]string
	}{
		{"remove then replace", `[{unset: ["AWS_*"]}, {set: {AWS_REGION: eu-west-1}}]`, map[string]string{"AWS_REGION": "eu-west-1", "KEEP": "yes", "aws_lower": "yes"}},
		{"replace then remove", `[{set: {AWS_REGION: eu-west-1}}, {unset: ["AWS_*"]}]`, map[string]string{"KEEP": "yes", "aws_lower": "yes"}},
		{"null empty and text", `{AWS_REGION: null, AWS_TOKEN: "", KEEP: "null"}`, map[string]string{"AWS_TOKEN": "", "KEEP": "null", "aws_lower": "yes"}},
		{"glob forms duplicates and misses", `[{unset: ["AWS_?OKEN", "aws_[a-z]*", ABSENT, ABSENT]}]`, map[string]string{"AWS_REGION": "old", "KEEP": "yes"}},
		{"read prior operation", `[{set: {NEW: next}}, {set: {COPIED: "{{ .env.NEW }}", AWS_REGION: {expr: env.NEW}}}, {unset: ["{{ .vars.cleanup }}"]}]`, map[string]string{"NEW": "next", "COPIED": "next", "AWS_REGION": "next", "KEEP": "yes", "aws_lower": "yes"}},
		{"siblings use old snapshot", `{AWS_REGION: new, COPIED: "{{ .env.AWS_REGION }}", OTHER: {expr: env.AWS_REGION}}`, map[string]string{"AWS_REGION": "new", "AWS_TOKEN": "secret", "COPIED": "old", "OTHER": "old", "KEEP": "yes", "aws_lower": "yes"}},
		{"conditional nil and list", `[{set: {AWS_TOKEN: {expr: 'vars.keep ? env.AWS_TOKEN : nil'}}}, {unset: {expr: vars.names}}]`, map[string]string{"KEEP": "yes", "aws_lower": "yes"}},
		{"empty expression list", `[{unset: {expr: '[]'}}]`, map[string]string{"AWS_REGION": "old", "AWS_TOKEN": "secret", "KEEP": "yes", "aws_lower": "yes"}},
		{"expression string is not rendered", `{AWS_REGION: {expr: '"{{ .env.MISSING }}"'}}`, map[string]string{"AWS_REGION": "{{ .env.MISSING }}", "AWS_TOKEN": "secret", "KEEP": "yes", "aws_lower": "yes"}},
		{"expression list is not rendered", `[{unset: {expr: '["{{ .env.MISSING }}"]'}}]`, map[string]string{"AWS_REGION": "old", "AWS_TOKEN": "secret", "KEEP": "yes", "aws_lower": "yes"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			renderer, err := workflow.NewRenderer(nil)
			if err != nil {
				t.Fatal(err)
			}
			base := map[string]string{"AWS_REGION": "old", "AWS_TOKEN": "secret", "KEEP": "yes", "aws_lower": "yes"}
			before := maps.Clone(base)
			data := map[string]any{"vars": map[string]any{"cleanup": "AWS_TOKEN", "keep": false, "names": []string{"AWS_REGION"}}}
			got, resolved, err := renderEnvironmentOverlay(renderer, scopedEnvironment(t, test.body), data, base, false)
			if err != nil {
				t.Fatal(err)
			}
			if !resolved || !maps.Equal(got, test.want) {
				t.Fatalf("environment = %#v, resolved = %v; want %#v", got, resolved, test.want)
			}
			if !maps.Equal(base, before) {
				t.Fatalf("mutated parent: %#v", base)
			}
			if _, exists := data["env"]; exists {
				t.Fatal("mutated caller template data")
			}
		})
	}
}

func TestEnvironmentOperationsRejectRuntimeTypesAndPatternsAtomically(t *testing.T) {
	t.Parallel()
	for _, test := range []struct{ body, want string }{
		{`[{unset: [TOKEN]}, {set: {A: {expr: '42'}}}]`, "string or nil"},
		{`{A: {expr: 'true'}}`, "string or nil"},
		{`{A: {expr: '[]'}}`, "string or nil"},
		{`{A: {expr: '{}'}}`, "string or nil"},
		{`[{unset: {expr: '"TOKEN"'}}]`, "list of strings"},
		{`[{unset: {expr: 'nil'}}]`, "list of strings"},
		{`[{unset: {expr: '[1]'}}]`, "list of strings"},
		{`[{unset: {expr: '[""]'}}]`, "must not be empty"},
		{`[{unset: {expr: '["["]'}}]`, "malformed"},
		{`[{unset: ['{{ .vars.bad }}']}]`, "malformed"},
	} {
		t.Run(test.body, func(t *testing.T) {
			renderer, err := workflow.NewRenderer(nil)
			if err != nil {
				t.Fatal(err)
			}
			base := map[string]string{"TOKEN": "secret"}
			for _, validating := range []bool{false, true} {
				_, _, err := renderEnvironmentOverlay(renderer, scopedEnvironment(t, test.body), map[string]any{"vars": map[string]any{"bad": "["}}, base, validating)
				if err == nil || !strings.Contains(err.Error(), test.want) {
					t.Fatalf("validating=%v: error = %v, want %q", validating, err, test.want)
				}
				if base["TOKEN"] != "secret" {
					t.Fatalf("mutated parent after error: %#v", base)
				}
			}
		})
	}
}

func TestEnvironmentOperationsValidationRetainsKnownRemovals(t *testing.T) {
	t.Parallel()
	renderer, err := workflow.NewRenderer(nil)
	if err != nil {
		t.Fatal(err)
	}
	environment := scopedEnvironment(t, `
- set: {TOKEN: null, DYNAMIC: "{{ .steps.prepare.value }}"}
- unset: [REMOVE, "{{ .steps.prepare.selector }}"]
`)
	base := map[string]string{"TOKEN": "secret", "REMOVE": "secret", "KEEP": "yes"}
	got, resolved, err := renderEnvironmentOverlay(renderer, environment, map[string]any{"steps": map[string]any{}}, base, true)
	if err != nil {
		t.Fatal(err)
	}
	_, token := got["TOKEN"]
	_, remove := got["REMOVE"]
	if resolved || token || remove || got["KEEP"] != "yes" {
		t.Fatalf("validation environment = %#v, resolved = %v", got, resolved)
	}
}

func TestEnvironmentOperationsReachChildProcessesAndRestore(t *testing.T) {
	t.Parallel()
	registry := environmentRegistry(t)
	if err := shell.Register(registry); err != nil {
		t.Fatal(err)
	}
	definition := testDefinition(t, "process-env",
		workflow.Step{ID: "prepare", Type: "literal", With: map[string]any{"value": "TOKEN"}},
		workflow.Step{Env: scopedEnvironment(t, `
- unset: {expr: '[steps.prepare.value]'}
- set: {EMPTY: "", NULL_TEXT: "null", REMOVE: null}
`), Steps: []workflow.Step{{ID: "inspect", Type: "shell", With: map[string]any{
			"command": "/bin/sh", "args": []any{"-c", `test "${TOKEN+x}" = "" && test "${REMOVE+x}" = "" && test "${EMPTY+x}" = x && test "$EMPTY" = "" && test "$NULL_TEXT" = null`},
		}}}},
		envCapture("after", "TOKEN"),
	)
	state, err := New(registry).Run(t.Context(), definition, Options{
		BaseEnv: map[string]string{"TOKEN": "original", "REMOVE": "original"}, RunDir: t.TempDir(), Stdout: io.Discard, Stderr: io.Discard,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := state.Steps["after"].(map[string]any)["value"]; got != "original" {
		t.Fatalf("restored token = %#v", got)
	}
}

func TestEnvironmentOperationsRestoreAcrossExitPaths(t *testing.T) {
	for _, exit := range []string{"success", "failure", "cancel", "return"} {
		t.Run(exit, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			registry := environmentRegistry(t)
			snapshots := map[string]map[string]string{}
			if err := registry.Register("snapshot", func(raw map[string]any) (step.Runner, error) {
				name, _ := raw["name"].(string)
				return runnerFunc(func(_ context.Context, request step.Request) (step.Result, error) {
					snapshots[name] = maps.Clone(request.Env)
					if name == "exit" {
						switch exit {
						case "failure":
							return step.Result{}, errors.New("failed")
						case "cancel":
							cancel()
							return step.Result{}, context.Canceled
						}
					}
					return step.Result{}, nil
				}), nil
			}); err != nil {
				t.Fatal(err)
			}
			snapshot := func(name string) workflow.Step {
				return workflow.Step{ID: name, Type: "snapshot", With: map[string]any{"name": name}}
			}
			owner := snapshot("owner")
			owner.Defer = []workflow.Step{snapshot("deferred")}
			children := []workflow.Step{owner,
				{Env: scopedEnvironment(t, `{TOKEN: inner}`), Steps: []workflow.Step{snapshot("inner")}},
				snapshot("after_inner"), snapshot("exit"),
			}
			if exit == "return" {
				children = append(children, workflow.Step{Return: &workflow.ReturnControl{Outputs: map[string]string{}}})
			}
			definition := testDefinition(t, "exits", workflow.Step{Env: scopedEnvironment(t, `[{unset: [TOKEN]}]`), Steps: children})
			definition.Finally = []workflow.Step{snapshot("finally")}
			state, err := New(registry).Run(ctx, definition, Options{BaseEnv: map[string]string{"TOKEN": "original"}, Stdout: io.Discard, Stderr: io.Discard})
			if (err != nil) != (exit == "failure" || exit == "cancel") {
				t.Fatalf("exit %s error = %v", exit, err)
			}
			for _, name := range []string{"owner", "after_inner", "exit", "deferred"} {
				if environment, exists := snapshots[name]; !exists {
					t.Errorf("missing snapshot %s", name)
				} else if _, exists := environment["TOKEN"]; exists {
					t.Errorf("%s restored removed TOKEN", name)
				}
			}
			if snapshots["inner"]["TOKEN"] != "inner" || snapshots["finally"]["TOKEN"] != "original" || (state != nil && state.Env["TOKEN"] != "original") {
				t.Fatalf("snapshots = %#v, state = %#v", snapshots, state)
			}
		})
	}
}

func TestEnvironmentOperationsIsolateBranchesAndActions(t *testing.T) {
	t.Parallel()
	registry := environmentRegistry(t)
	if err := registry.Register("snapshot", func(map[string]any) (step.Runner, error) {
		return runnerFunc(func(_ context.Context, request step.Request) (step.Result, error) {
			_, exists := request.Env["TOKEN"]
			return step.Result{Outputs: map[string]any{"present": exists}}, nil
		}), nil
	}); err != nil {
		t.Fatal(err)
	}
	action := &workflow.Action{Version: 1, Name: "snapshot-action", Steps: []workflow.Step{{ID: "capture", Type: "snapshot"}}, Outputs: map[string]workflow.ActionOutput{
		"present": {Value: "steps.capture.present"},
	}}
	definition := testDefinition(t, "branches", workflow.Step{Concurrent: &workflow.ConcurrentGroup{MaxConcurrency: 2, Steps: []workflow.Step{
		{Env: scopedEnvironment(t, `{TOKEN: null}`), Steps: []workflow.Step{
			{ID: "removed", Type: "snapshot"},
			{ID: "action", Uses: workflow.ActionSource{Path: "./action"}, Action: action},
		}},
		{ID: "inherited", Type: "snapshot"},
	}}})
	state, err := New(registry).Run(t.Context(), definition, Options{BaseEnv: map[string]string{"TOKEN": "original"}, Stdout: io.Discard, Stderr: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]bool{"removed": false, "action": false, "inherited": true} {
		if got := state.Steps[id].(map[string]any)["present"]; got != want {
			t.Errorf("%s presence = %#v, want %v", id, got, want)
		}
	}
}

func TestEnvironmentOperationsValidateSyntaxAndReferences(t *testing.T) {
	t.Parallel()
	for _, test := range []struct{ body, want string }{
		{`{A: {expr: 'vars.missing'}}`, "missing"},
		{`[{unset: {expr: 'vars.missing'}}]`, "missing"},
		{`[{unset: ['{{ .vars.missing }}']}]`, "missing"},
		{`{A: {expr: 'vars.'}}`, "expr"},
		{`[{unset: {expr: 'vars.'}}]`, "expr"},
		{`[{unset: ['{{ invalid_function }}']}]`, "template"},
	} {
		t.Run(test.body, func(t *testing.T) {
			definition := testDefinition(t, "invalid", workflow.Step{Env: scopedEnvironment(t, test.body), Steps: []workflow.Step{envCapture("child", "A")}})
			err := New(environmentRegistry(t)).Validate(t.Context(), definition, Options{})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestEnvironmentOperationsDryRunHidesValuesAndSelectors(t *testing.T) {
	t.Parallel()
	definition := testDefinition(t, "dry", workflow.Step{Env: scopedEnvironment(t, `
- set: {TOKEN: private-value, REMOVE: null}
- unset: ["AWS_*", "{{ .vars.private_selector }}"]
- unset: {expr: vars.private_list}
`), Steps: []workflow.Step{envCapture("run", "TOKEN")}})
	definition.Vars = map[string]any{"private_selector": "PRIVATE_*", "private_list": []string{"EXTRA_*"}}
	var output strings.Builder
	_, err := New(environmentRegistry(t)).Run(t.Context(), definition, Options{DryRun: true, Stdout: &output, Stderr: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	want := "1. env: [set: REMOVE, TOKEN; unset: AWS_*, <dynamic>; unset: <dynamic>]\n   1.1 run (capture_env)\n"
	if !reflect.DeepEqual(output.String(), want) {
		t.Fatalf("dry run = %q, want %q", output.String(), want)
	}
}

func TestEnvironmentOperationsValidationDefersAndScopesUnresolved(t *testing.T) {
	t.Parallel()
	renderer, err := workflow.NewRenderer(nil)
	if err != nil {
		t.Fatal(err)
	}
	data := func() map[string]any {
		return map[string]any{"vars": map[string]any{"tag": nil, "bad": "["}}
	}

	_, resolved, err := renderEnvironmentOverlay(renderer, scopedEnvironment(t, `{TAG: "{{ .vars.tag }}"}`), data(), nil, true)
	if err != nil || resolved {
		t.Fatalf("nil placeholder: resolved=%v err=%v, want deferred", resolved, err)
	}

	// An earlier unresolved operation must not hide a malformed pattern rendered from known data.
	_, _, err = renderEnvironmentOverlay(renderer, scopedEnvironment(t, `
- set: {A: "{{ .steps.build.outputs.tag }}"}
- unset: ['{{ .vars.bad }}']
`), data(), nil, true)
	if err == nil || !strings.Contains(err.Error(), "malformed") {
		t.Fatalf("error = %v, want malformed pattern", err)
	}
}

func TestEnvironmentOperationsLiteralUnsetRefreshesSnapshot(t *testing.T) {
	t.Parallel()
	renderer, err := workflow.NewRenderer(nil)
	if err != nil {
		t.Fatal(err)
	}
	environment, _, err := renderEnvironmentOverlay(renderer, scopedEnvironment(t, `
- unset: [TOKEN, "AWS_*"]
- set: {SEEN: {expr: 'env.TOKEN ?? env.AWS_KEY ?? "gone"'}}
`), map[string]any{}, map[string]string{"TOKEN": "secret", "AWS_KEY": "key", "KEEP": "yes"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if want := map[string]string{"SEEN": "gone", "KEEP": "yes"}; !reflect.DeepEqual(environment, want) {
		t.Fatalf("environment = %#v, want %#v", environment, want)
	}
}
