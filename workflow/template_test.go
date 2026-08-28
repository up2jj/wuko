package workflow

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestRendererExecutesInlineAndNestedTemplates(t *testing.T) {
	t.Parallel()
	renderer, err := NewRenderer(map[string]TemplateDefinition{
		"image":   {Inline: "{{ .vars.registry }}/app:{{ .vars.version }}"},
		"message": {Inline: `Deploying {{ template "image" . }}`},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := renderer.Render(`release: {{ template "message" . }}`, map[string]any{
		"vars": map[string]any{"registry": "example.test", "version": "v1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != "release: Deploying example.test/app:v1" {
		t.Fatalf("rendered = %q", got)
	}
}

func TestRendererExposesHelpersToNamedAndInlineTemplates(t *testing.T) {
	t.Parallel()
	renderer, err := NewRenderer(map[string]TemplateDefinition{
		"labels": {Inline: `{{ .vars.labels | keys | join "," }}`},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := renderer.Render(`{{ template "labels" . }}:{{ .vars.name | trim | lower }}`, map[string]any{
		"vars": map[string]any{
			"labels": map[string]any{"z": true, "a": true},
			"name":   " WUKO ",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != "a,z:wuko" {
		t.Fatalf("rendered = %q", got)
	}
}

func TestRendererValidatesNamedTemplateWithOptionalBranches(t *testing.T) {
	t.Parallel()
	const resultsTable = `Repository | Status
-----------|-------
{{ range .steps.release_drifts.results -}}
{{ .repository }} | {{ if .changed }}changed{{ else }}no changes{{ end }}
{{ end -}}`

	renderer, err := NewRenderer(map[string]TemplateDefinition{
		"results_table": {Inline: resultsTable},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := renderer.Validate(`{{ template "results_table" . }}`); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestRendererHelpersPreserveStrictMissingKeys(t *testing.T) {
	t.Parallel()
	renderer, err := NewRenderer(nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = renderer.Render(`{{ .vars.missing | default "fallback" }}`, map[string]any{"vars": map[string]any{}})
	if err == nil || !strings.Contains(err.Error(), "map has no entry for key") {
		t.Fatalf("error = %v", err)
	}
	got, err := renderer.Render(`{{ get "missing" .vars | default "fallback" }}`, map[string]any{"vars": map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	if got != "fallback" {
		t.Fatalf("rendered = %q", got)
	}
}

func TestRendererRejectsUnknownFunctionDuringValidation(t *testing.T) {
	t.Parallel()
	renderer, err := NewRenderer(nil)
	if err != nil {
		t.Fatal(err)
	}
	err = renderer.Validate(`{{ unknownHelper .vars.name }}`)
	if err == nil || !strings.Contains(err.Error(), `function "unknownHelper" not defined`) {
		t.Fatalf("error = %v", err)
	}
}

func TestRendererRejectsInvalidDefinitionsAndReferences(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		definitions map[string]TemplateDefinition
		want        string
	}{
		{name: "invalid name", definitions: map[string]TemplateDefinition{"bad-name": {Inline: "value"}}, want: "invalid template name"},
		{name: "empty body", definitions: map[string]TemplateDefinition{"empty": {Inline: "  "}}, want: "must not be empty"},
		{name: "parse error", definitions: map[string]TemplateDefinition{"broken": {Inline: "{{ if .value }}"}}, want: "unexpected EOF"},
		{name: "missing reference", definitions: map[string]TemplateDefinition{"broken": {Inline: `{{ template "missing" . }}`}}, want: "undefined template"},
		{name: "nested definition", definitions: map[string]TemplateDefinition{"outer": {Inline: `{{ define "inner" }}value{{ end }}`}}, want: "must not define nested template"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewRenderer(tt.definitions)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

func TestRendererValidatesInlineTemplateReferences(t *testing.T) {
	t.Parallel()
	renderer, err := NewRenderer(nil)
	if err != nil {
		t.Fatal(err)
	}
	err = renderer.Validate(`{{ template "missing" . }}`)
	if err == nil || !strings.Contains(err.Error(), "undefined template") {
		t.Fatalf("error = %v", err)
	}
}

func TestRendererRejectsDefinitionsInRenderedValues(t *testing.T) {
	t.Parallel()
	renderer, err := NewRenderer(map[string]TemplateDefinition{
		"declared": {Inline: "original"},
	})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name  string
		value string
		want  string
	}{
		{name: "new definition", value: `{{ define "hidden" }}hidden{{ end }}{{ template "hidden" . }}`, want: `nested template "hidden"`},
		{name: "declared override", value: `{{ define "declared" }}override{{ end }}{{ template "declared" . }}`, want: `nested template "declared"`},
		{name: "block", value: `{{ block "hidden" . }}hidden{{ end }}`, want: `nested template "hidden"`},
		{name: "execution root override", value: `{{ define "wuko:value" }}override{{ end }}`, want: `nested template "wuko:value"`},
		{name: "definition check override", value: `{{ define "wuko:definition-check" }}override{{ end }}`, want: `nested template "wuko:definition-check"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := renderer.Validate(tt.value)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

func TestLoadResolvesFileBackedTemplate(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "templates"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "templates", "message.tmpl"), []byte(`Hello {{ .vars.name }}`), 0o644); err != nil {
		t.Fatal(err)
	}
	workflowPath := filepath.Join(dir, "workflow.yaml")
	data := `version: 1
name: templates
templates:
  message:
    file: templates/message.tmpl
steps:
  - id: run
    type: shell
    with: {command: echo}
`
	if err := os.WriteFile(workflowPath, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	definition, err := loadLocal(workflowPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := definition.Templates["message"].Body; got != `Hello {{ .vars.name }}` {
		t.Fatalf("body = %q", got)
	}
}

func TestPrepareValuesRendersNamedTemplate(t *testing.T) {
	t.Parallel()
	definition := &Definition{
		Version: 1, Name: "environment", Vars: map[string]any{"stage": "production"},
		Templates: map[string]TemplateDefinition{"environment": {Inline: `{{ .vars.stage }}-west`}},
		Env:       Environment{"APP_ENV": `{{ template "environment" . }}`},
		Steps:     []Step{{ID: "run", Type: "shell"}},
	}
	_, environment, err := PrepareValues(definition, LoadOptions{BaseEnv: map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
	if environment["APP_ENV"] != "production-west" {
		t.Fatalf("APP_ENV = %q", environment["APP_ENV"])
	}
}

func TestLoadRejectsInvalidTemplateFiles(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		file string
		want string
	}{
		{name: "absolute", file: "/tmp/template.tmpl", want: "must be relative"},
		{name: "missing", file: "missing.tmpl", want: "reading template file"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			workflowPath := filepath.Join(dir, "workflow.yaml")
			data := "version: 1\nname: templates\ntemplates:\n  message:\n    file: " + tt.file + "\nsteps:\n  - id: run\n    type: shell\n"
			if err := os.WriteFile(workflowPath, []byte(data), 0o644); err != nil {
				t.Fatal(err)
			}
			_, err := loadLocal(workflowPath)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

func TestLoadRejectsOversizedTemplateFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "large.tmpl"), bytes.Repeat([]byte("x"), maxTemplateSize+1), 0o644); err != nil {
		t.Fatal(err)
	}
	workflowPath := filepath.Join(dir, "workflow.yaml")
	data := "version: 1\nname: templates\ntemplates:\n  large:\n    file: large.tmpl\nsteps:\n  - id: run\n    type: shell\n"
	if err := os.WriteFile(workflowPath, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := loadLocal(workflowPath)
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("error = %v", err)
	}
}

func TestTemplateDefinitionUnmarshalRejectsAmbiguousObjects(t *testing.T) {
	t.Parallel()
	for _, source := range []string{
		"template: {file: one.tmpl, extra: true}\n",
		"template: {file: ''}\n",
		"template: {inline: value}\n",
		"template: [value]\n",
	} {
		var value struct {
			Template TemplateDefinition `yaml:"template"`
		}
		if err := yaml.Unmarshal([]byte(source), &value); err == nil {
			t.Fatalf("expected %q to fail", source)
		}
	}
}

func TestRendererRendersArgumentLessTemplateInvocation(t *testing.T) {
	t.Parallel()
	renderer, err := NewRenderer(map[string]TemplateDefinition{"greeting": {Inline: "hello"}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := renderer.Render(`{{ template "greeting" }}!`, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if got != "hello!" {
		t.Fatalf("rendered = %q", got)
	}
}
