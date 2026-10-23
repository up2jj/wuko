package cmd

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/up2jj/wuko/diagnostic"
	"github.com/up2jj/wuko/engine"
	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/workflow"
)

func formRegistry(t *testing.T) *step.Registry {
	t.Helper()
	registry := step.NewRegistry()
	if err := registerBuiltinSteps(registry); err != nil {
		t.Fatal(err)
	}
	return registry
}

func formDefinition(t *testing.T, steps ...workflow.Step) *workflow.Definition {
	t.Helper()
	return &workflow.Definition{Version: 1, Name: "form-test", Dir: t.TempDir(), Steps: steps}
}

func formStep(fields ...map[string]any) workflow.Step {
	items := make([]any, len(fields))
	for i, field := range fields {
		items[i] = field
	}
	return workflow.Step{ID: "settings", Type: "tui_form", With: map[string]any{"title": "Settings", "fields": items}}
}

func TestTUIFormPublishesDirectOutputsWithoutVariables(t *testing.T) {
	definition := formDefinition(t,
		formStep(
			map[string]any{"id": "name", "type": "text", "label": "Name", "value": map[string]any{"expr": "vars.person"}},
			map[string]any{"id": "notify", "type": "checkbox", "label": "Notify", "value": true},
			map[string]any{"id": "count", "type": "number", "label": "Count", "value": 2},
		),
		workflow.Step{Return: &workflow.ReturnControl{Outputs: map[string]string{"name": "steps.settings.name", "notify": "steps.settings.notify", "count": "steps.settings.count"}}},
	)
	state, err := engine.New(formRegistry(t)).Run(t.Context(), definition, engine.Options{Vars: map[string]any{"person": "Ada"}})
	if err != nil {
		t.Fatal(err)
	}
	if state.Outputs["name"] != "Ada" || state.Outputs["notify"] != true || state.Outputs["count"] != int64(2) {
		t.Fatalf("outputs=%#v", state.Outputs)
	}
	if len(state.Vars) != 1 || state.Vars["person"] != "Ada" {
		t.Fatalf("form changed vars: %#v", state.Vars)
	}
}

func TestTUIFormReferencesAreValidated(t *testing.T) {
	tests := []struct {
		name           string
		field          map[string]any
		consumer, want string
	}{
		{"valid output", map[string]any{"id": "name", "type": "text", "label": "Name"}, "steps.settings.name", ""},
		{"unknown output", map[string]any{"id": "name", "type": "text", "label": "Name"}, "steps.settings.typo", `has no output "typo"`},
		{"no variable writer", map[string]any{"id": "name", "type": "text", "label": "Name"}, "vars.name", `variable "name" is not declared`},
		{"unknown choice source", map[string]any{"id": "name", "type": "select", "label": "Name", "from": "steps.missing.value"}, "steps.settings.name", `step "missing" is not available`},
		{"unknown value expression", map[string]any{"id": "name", "type": "text", "label": "Name", "value": map[string]any{"expr": "vars.missing"}}, "steps.settings.name", `variable "missing" is not declared`},
		{"unknown default expression", map[string]any{"id": "name", "type": "text", "label": "Name", "default": map[string]any{"expr": "vars.missing"}}, "steps.settings.name", `variable "missing" is not declared`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			definition := formDefinition(t, formStep(test.field), workflow.Step{Return: &workflow.ReturnControl{Outputs: map[string]string{"value": test.consumer}}})
			err := engine.New(formRegistry(t)).Validate(t.Context(), definition, engine.Options{})
			if test.want == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error=%v want=%q", err, test.want)
			}
		})
	}
}

func TestTUIFormUsesDynamicChoicesAndTemplatedDefault(t *testing.T) {
	definition := formDefinition(t,
		workflow.Step{ID: "fetch", Type: "set", With: map[string]any{"variable": "available", "value": []any{"west", "east"}}},
		formStep(map[string]any{"id": "region", "type": "select", "label": "Region", "from": "steps.fetch.value", "value": map[string]any{"expr": "vars.region"}}),
	)
	state, err := engine.New(formRegistry(t)).Run(t.Context(), definition, engine.Options{Vars: map[string]any{"region": "east"}})
	if err != nil {
		t.Fatal(err)
	}
	outputs := state.Steps["settings"].(map[string]any)
	if outputs["region"] != "east" {
		t.Fatalf("outputs=%#v", outputs)
	}
	definition = formDefinition(t, formStep(map[string]any{"id": "name", "type": "text", "label": "Name", "default": "Hello {{ .vars.person }}"}))
	state, err = engine.New(formRegistry(t)).Run(t.Context(), definition, engine.Options{Vars: map[string]any{"person": "Ada"}, Interactive: true, Stdin: strings.NewReader("\x13"), Stdout: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if state.Steps["settings"].(map[string]any)["name"] != "Hello Ada" {
		t.Fatalf("steps=%#v", state.Steps)
	}
}

func TestTUIFormConcurrentExecutionRequiresValues(t *testing.T) {
	for _, supplied := range []bool{true, false} {
		t.Run(map[bool]string{true: "supplied", false: "missing"}[supplied], func(t *testing.T) {
			field := map[string]any{"id": "name", "type": "text", "label": "Name", "default": "ignored"}
			if supplied {
				field["value"] = "Ada"
			}
			definition := formDefinition(t, workflow.Step{Concurrent: &workflow.ConcurrentGroup{Steps: []workflow.Step{formStep(field), {ID: "other", Type: "assert", With: map[string]any{"expr": "true", "message": "Other step succeeds"}}}, MaxConcurrency: 2}})
			_, err := engine.New(formRegistry(t)).Run(t.Context(), definition, engine.Options{Interactive: true, Stdin: strings.NewReader("\x13"), Stdout: io.Discard})
			if supplied {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), "non-interactive form requires explicit value") {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestTUIFormCancellationDoesNotPublishOutputs(t *testing.T) {
	definition := formDefinition(t, formStep(map[string]any{"id": "name", "type": "text", "label": "Name", "default": "partial"}))
	state, err := engine.New(formRegistry(t)).Run(t.Context(), definition, engine.Options{Interactive: true, Stdin: strings.NewReader("\x03"), Stdout: io.Discard})
	if err == nil {
		t.Fatal("expected cancellation")
	}
	if state != nil {
		if _, exists := state.Steps["settings"]; exists {
			t.Fatalf("published failed form: %#v", state.Steps)
		}
	}
}

func TestTUIFormRedactsPasswordBindingsInDiagnostics(t *testing.T) {
	definition := formDefinition(t, formStep(map[string]any{"id": "credential", "type": "password", "label": "Credential", "value": "private-input", "default": "private-default"}))
	var diagnostics bytes.Buffer
	_, err := engine.New(formRegistry(t)).Run(t.Context(), definition, engine.Options{Diagnostics: func(event diagnostic.Event) {
		for _, attribute := range event.Attributes {
			diagnostics.WriteString(attribute.Value)
		}
	}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(diagnostics.String(), "private-input") || strings.Contains(diagnostics.String(), "private-default") || !strings.Contains(diagnostics.String(), diagnostic.Redacted) {
		t.Fatalf("diagnostics=%s", diagnostics.String())
	}
}

func TestTUIFormCLIReadsTypedInputs(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "form.yaml")
	source := `version: 1
name: cli-form
vars:
  name: Ada
steps:
  - id: settings
    type: tui_form
    with:
      title: Settings
      fields:
        - {id: name, type: text, label: Name, value: {expr: vars.name}}
  - id: verify
    type: assert
    with:
      expr: steps.settings.name == "Grace"
      message: Form reads the CLI variable
  - return:
      outputs:
        greeting: '"Hello " + steps.settings.name'
`
	if err := os.WriteFile(file, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	command := newRootCmd(dependencies{stdin: strings.NewReader(""), stdout: &output, stderr: io.Discard, cwd: func() (string, error) { return root, nil }, homeDir: func() (string, error) { return root, nil }, configDir: func() (string, error) { return root, nil }, registry: formRegistry(t), getenv: func(string) string { return "" }})
	command.SetArgs([]string{"run", "--file", file, "--var", "name=Grace"})
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestTUIFormCustomExpressionReferencesAreValidated(t *testing.T) {
	for _, test := range []struct{ expr, want string }{
		{"len(value) >= 3", ""},
		{"value == vars.missing", `variable "missing" is not declared`},
		{"value == steps.missing.value", `step "missing" is not available`},
	} {
		field := map[string]any{"id": "name", "type": "text", "label": "Name", "value": "Ada", "validation": map[string]any{"expr": test.expr}}
		definition := formDefinition(t, formStep(field))
		err := engine.New(formRegistry(t)).Validate(t.Context(), definition, engine.Options{})
		if test.want == "" {
			if err != nil {
				t.Fatal(err)
			}
		} else if err == nil || !strings.Contains(err.Error(), test.want) {
			t.Fatalf("error=%v want=%q", err, test.want)
		}
	}
}

func TestTUIFormLuaValidationSourceIsLiteral(t *testing.T) {
	field := map[string]any{"id": "name", "type": "text", "label": "Name", "value": "Ada", "validation": map[string]any{
		"lua": `function(value) return value ~= "{{ .vars.missing }}" end`,
	}}
	definition := formDefinition(t, formStep(field))
	state, err := engine.New(formRegistry(t)).Run(t.Context(), definition, engine.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if state.Steps["settings"].(map[string]any)["name"] != "Ada" || len(state.Vars) > 0 {
		t.Fatalf("state=%#v", state)
	}
}

func TestTUIFormLuaValidatorRunsInInteractiveProgram(t *testing.T) {
	field := map[string]any{"id": "name", "type": "text", "label": "Name", "default": "Ada", "validation": map[string]any{
		"lua": `function(value) return value == "Ada" end`, "message": "Use Ada",
	}}
	definition := formDefinition(t, formStep(field))
	state, err := engine.New(formRegistry(t)).Run(t.Context(), definition, engine.Options{Interactive: true, Stdin: strings.NewReader("\x13"), Stdout: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if state.Steps["settings"].(map[string]any)["name"] != "Ada" {
		t.Fatalf("steps=%#v", state.Steps)
	}
}

func TestTUIFormCrossFieldValidationUsesCurrentPasswordEdits(t *testing.T) {
	for _, kind := range []string{"expr", "lua"} {
		t.Run(kind, func(t *testing.T) {
			rules := map[string]any{"expr": "value == fields.password", "message": "Passwords must match"}
			if kind == "lua" {
				rules = map[string]any{"lua": "function(value, context) return value == context.fields.password end", "message": "Passwords must match"}
			}
			definition := formDefinition(t, formStep(
				map[string]any{"id": "password", "type": "password", "label": "Password", "default": "old"},
				map[string]any{"id": "confirmation", "type": "password", "label": "Confirm", "default": "old", "validation": rules},
			))
			// Edit the password, try submission with a stale confirmation, then correct it.
			input := strings.NewReader("\x01\x0bnew\t\x13\x01\x0bnew\x13")
			state, err := engine.New(formRegistry(t)).Run(t.Context(), definition, engine.Options{Interactive: true, Stdin: input, Stdout: io.Discard})
			if err != nil {
				t.Fatal(err)
			}
			outputs := state.Steps["settings"].(map[string]any)
			if outputs["password"] != "new" || outputs["confirmation"] != "new" || len(state.Vars) > 0 {
				t.Fatalf("outputs=%#v vars=%#v", outputs, state.Vars)
			}
		})
	}
}

func TestTUIFormCrossFieldReferencesRejectUnknownIDs(t *testing.T) {
	definition := formDefinition(t, formStep(map[string]any{
		"id": "name", "type": "text", "label": "Name", "value": "Ada",
		"validation": map[string]any{"expr": "value == fields.missing"},
	}))
	err := engine.New(formRegistry(t)).Validate(t.Context(), definition, engine.Options{})
	if err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("error=%v", err)
	}
}
