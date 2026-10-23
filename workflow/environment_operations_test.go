package workflow

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/up2jj/wuko/internal/testfs"
	"go.yaml.in/yaml/v3"
)

func TestScopedEnvironmentDecodesValuesAndOperations(t *testing.T) {
	t.Parallel()
	var environment ScopedEnvironment
	if err := yaml.Unmarshal([]byte(`
- unset: ["AWS_*", LEGACY_TOKEN]
- set:
    REGION: "{{ .vars.region }}"
    EMPTY: ""
    TEXT: "null"
    REMOVE: null
    DYNAMIC: {expr: vars.token}
- unset: {expr: vars.cleanup}
`), &environment); err != nil {
		t.Fatal(err)
	}
	if len(environment) != 3 || environment[0].Unset.Patterns[0] != "AWS_*" || environment[2].Unset.Expression != "vars.cleanup" {
		t.Fatalf("operations = %#v", environment)
	}
	set := environment[1].Set
	if set["REMOVE"].Literal != nil || *set["EMPTY"].Literal != "" || *set["TEXT"].Literal != "null" || set["DYNAMIC"].Expression != "vars.token" {
		t.Fatalf("values = %#v", set)
	}
	encoded, err := yaml.Marshal(environment)
	if err != nil {
		t.Fatal(err)
	}
	var roundTrip ScopedEnvironment
	if err := yaml.Unmarshal(encoded, &roundTrip); err != nil {
		t.Fatalf("round trip: %v\n%s", err, encoded)
	}
	if roundTrip.Label() != environment.Label() {
		t.Fatalf("round-trip label = %q", roundTrip.Label())
	}
}

func TestScopedEnvironmentRejectsInvalidDeclarations(t *testing.T) {
	t.Parallel()
	for _, body := range []string{
		`[]`, `{}`, `value`,
		`[{set: {}}]`, `[{unset: []}]`, `[{unset: null}]`,
		`[{set: {A: value}, unset: [A]}]`, `[{unknown: [A]}]`, `[null]`,
		`[{set: null}]`, `[{set: []}]`,
		`{A: 1}`, `{A: true}`, `{A: []}`, `{A: {literal: text}}`,
		`{A: {expr: ""}}`, `{A: {expr: 42}}`, `{A: {expr: vars.a, other: value}}`,
		`{NOT-VALID: value}`, `{1: text}`, `{A: value, A: other}`,
		`[{unset: "A"}]`, `[{unset: [null]}]`, `[{unset: [{expr: vars.a}]}]`,
		`[{unset: {expr: ""}}]`, `[{unset: {expr: vars.a, other: []}}]`,
		`[{unset: [""]}]`, `[{unset: ["["]}]`,
	} {
		t.Run(body, func(t *testing.T) {
			var environment ScopedEnvironment
			if err := yaml.Unmarshal([]byte(body), &environment); err == nil {
				t.Fatalf("accepted %s", body)
			}
		})
	}
}

func TestScopedEnvironmentFlatMapKeepsOperationNamesLiteral(t *testing.T) {
	t.Parallel()
	var environment ScopedEnvironment
	if err := yaml.Unmarshal([]byte(`{set: value, unset: null}`), &environment); err != nil {
		t.Fatal(err)
	}
	if len(environment) != 1 || *environment[0].Set["set"].Literal != "value" || environment[0].Set["unset"].Literal != nil || environment.Label() != "env: set, unset" {
		t.Fatalf("environment = %#v, label = %q", environment, environment.Label())
	}
}

func TestScopedEnvironmentLabelPreservesOrderAndHidesDynamicSelectors(t *testing.T) {
	t.Parallel()
	var environment ScopedEnvironment
	if err := yaml.Unmarshal([]byte(`
- unset: ["AWS_*", "{{ .vars.private_selector }}"]
- set: {Z: secret-value, A: null}
- unset: {expr: vars.private_list}
`), &environment); err != nil {
		t.Fatal(err)
	}
	want := "env: [unset: AWS_*, <dynamic>; set: A, Z; unset: <dynamic>]"
	if got := environment.Label(); got != want {
		t.Fatalf("label = %q, want %q", got, want)
	}
}

func TestScopedEnvironmentOnlyExtendsStepDeclarations(t *testing.T) {
	t.Parallel()
	for _, declaration := range []string{`{TOKEN: null}`, `[{unset: [TOKEN]}]`, `{TOKEN: {expr: vars.token}}`} {
		for _, target := range []bool{false, true} {
			body := "version: 1\nname: test\nenv: " + declaration + "\nsteps: [{return: {outputs: {}}}]\n"
			if target {
				body = "version: 1\nname: test\ntargets:\n  build:\n    env: " + declaration + "\n    steps: [{return: {outputs: {}}}]\n"
			}
			var definition Definition
			if err := yaml.Unmarshal([]byte(body), &definition); err == nil {
				t.Fatalf("accepted root/target environment: %s", body)
			}
		}
	}
	var block Step
	if err := yaml.Unmarshal([]byte("env: [{unset: [TOKEN]}]\nsteps: [{return: {outputs: {}}}]"), &block); err != nil {
		t.Fatal(err)
	}
	if !block.IsEnvironmentBlock() || block.IsConditionalBlock() || block.ValidateBlock() != nil {
		t.Fatalf("unset-only block = %#v", block)
	}
	if err := yaml.Unmarshal([]byte("unset: [TOKEN]\nsteps: []"), &block); err == nil || !strings.Contains(err.Error(), "unset") {
		t.Fatalf("sibling unset error = %v", err)
	}
}

func TestScopedEnvironmentOperationsLoadActionsAndRequiredFragments(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	fragment := filepath.Join(dir, "steps.yaml")
	actionPath := filepath.Join(dir, "action.yaml")
	workflowPath := filepath.Join(dir, "workflow.yaml")
	testfs.Write(t, fragment, `- env:
    - unset: [TOKEN]
  steps:
    - id: action
      uses: ./action.yaml
`)
	testfs.Write(t, actionPath, `version: 1
name: action
steps:
  - env:
      - set: {MODE: action}
      - unset: [TOKEN]
    steps:
      - id: capture
        type: shell
        with: {command: echo, args: [done]}
`)
	testfs.Write(t, workflowPath, "version: 1\nname: caller\nsteps:\n  - require: steps.yaml\n")
	definition, err := NewLoader(nil).Load(t.Context(), workflowPath, LoadOptions{RunDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	block := definition.Steps[0]
	if !block.IsEnvironmentBlock() || block.Location.Source != fragment || block.Steps[0].Action == nil {
		t.Fatalf("required block = %#v", block)
	}
	canonicalAction, err := filepath.EvalSymlinks(actionPath)
	if err != nil {
		t.Fatal(err)
	}
	actionBlock := block.Steps[0].Action.Steps[0]
	if len(actionBlock.Env) != 2 || actionBlock.Location.Source != canonicalAction || actionBlock.Steps[0].Location.Line != 8 || actionBlock.Steps[0].ValidationPath() != "steps[0].steps[0]" {
		t.Fatalf("action operations = %d, source = %q, child location = %#v, path = %q", len(actionBlock.Env), actionBlock.Location.Source, actionBlock.Steps[0].Location, actionBlock.Steps[0].ValidationPath())
	}
}
