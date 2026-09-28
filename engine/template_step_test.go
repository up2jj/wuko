package engine

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/up2jj/wuko/step"
	setstep "github.com/up2jj/wuko/steps/set"
	templatestep "github.com/up2jj/wuko/steps/template"
	"github.com/up2jj/wuko/workflow"
)

func TestTemplateStepRendersRawSourceAfterResolvingData(t *testing.T) {
	registry := step.NewRegistry()
	if err := templatestep.Register(registry); err != nil {
		t.Fatal(err)
	}
	definition := testDefinition(t, "render",
		workflow.Step{ID: "manifest", Type: "template", With: map[string]any{
			"source": `{{ .data.name }}:{{ .data.replicas }}`,
			"data": map[string]any{
				"name":     `{{ .vars.application }}`,
				"replicas": map[string]any{"expr": "vars.replicas"},
			},
		}},
	)
	definition.Vars = map[string]any{"application": "billing", "replicas": 3}
	state, err := New(registry).Run(t.Context(), definition, Options{
		RunDir: t.TempDir(), Stdout: io.Discard, Stderr: io.Discard,
	})
	if err != nil {
		t.Fatal(err)
	}
	outputs := state.Steps["manifest"].(map[string]any)
	if outputs["content"] != "billing:3" {
		t.Fatalf("outputs = %#v", outputs)
	}
}

func TestTemplateStepSelectsPackagedFileWithRenderedPath(t *testing.T) {
	registry := step.NewRegistry()
	if err := templatestep.Register(registry); err != nil {
		t.Fatal(err)
	}
	definition := testDefinition(t, "render-file", workflow.Step{
		ID: "message", Type: "template", With: map[string]any{
			"file": "templates/{{ .vars.kind }}.tmpl",
			"data": map[string]any{"name": "{{ .vars.application }}"},
		},
	})
	definition.Vars = map[string]any{"kind": "service", "application": "billing"}
	if err := os.Mkdir(filepath.Join(definition.Dir, "templates"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(definition.Dir, "templates", "service.tmpl"), []byte("service={{ .data.name }}"), 0o644); err != nil {
		t.Fatal(err)
	}
	state, err := New(registry).Run(t.Context(), definition, Options{
		RunDir: t.TempDir(), Stdout: io.Discard, Stderr: io.Discard,
	})
	if err != nil {
		t.Fatal(err)
	}
	outputs := state.Steps["message"].(map[string]any)
	if outputs["content"] != "service=billing" {
		t.Fatalf("outputs = %#v", outputs)
	}
}

func TestTemplateStepAcceptsTemplatedExpressionBinding(t *testing.T) {
	registry := step.NewRegistry()
	if err := templatestep.Register(registry); err != nil {
		t.Fatal(err)
	}
	definition := testDefinition(t, "templated-expr", workflow.Step{
		ID: "render", Type: "template", With: map[string]any{
			"source": `{{ .data.name }}`,
			"data":   map[string]any{"name": map[string]any{"expr": "{{ .vars.expression }}"}},
		},
	})
	definition.Vars = map[string]any{"application": "billing", "expression": "vars.application"}
	// Validation sees the unrendered configuration, where the expression is still a
	// template: it must defer compilation rather than reject a workflow that runs.
	if err := New(registry).Validate(t.Context(), definition, Options{RunDir: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	state, err := New(registry).Run(t.Context(), definition, Options{
		RunDir: t.TempDir(), Stdout: io.Discard, Stderr: io.Discard,
	})
	if err != nil {
		t.Fatal(err)
	}
	outputs := state.Steps["render"].(map[string]any)
	if outputs["content"] != "billing" {
		t.Fatalf("outputs = %#v", outputs)
	}
}

func TestTemplateStepOutputContractFollowsMode(t *testing.T) {
	registry := step.NewRegistry()
	if err := templatestep.Register(registry); err != nil {
		t.Fatal(err)
	}
	if err := setstep.Register(registry); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name      string
		with      map[string]any
		reference string
		want      string
	}{
		{
			name: "memory mode omits path", with: map[string]any{"source": "body"},
			reference: "{{ .steps.render.path }}", want: `step "render" has no output "path"`,
		},
		{
			name: "memory mode publishes content", with: map[string]any{"source": "body"},
			reference: "{{ .steps.render.content }}",
		},
		{
			name: "file mode omits content", with: map[string]any{"source": "body", "destination": "out.txt"},
			reference: "{{ .steps.render.content }}", want: `step "render" has no output "content"`,
		},
		{
			name: "file mode publishes path", with: map[string]any{"source": "body", "destination": "out.txt"},
			reference: "{{ .steps.render.path }}",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			definition := testDefinition(t, "contract",
				workflow.Step{ID: "render", Type: "template", With: test.with},
				workflow.Step{ID: "use", Type: "set", With: map[string]any{"variable": "target", "value": test.reference}},
			)
			err := New(registry).Validate(t.Context(), definition, Options{RunDir: t.TempDir()})
			if test.want == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestTemplateStepReferenceValidation(t *testing.T) {
	registry := step.NewRegistry()
	if err := templatestep.Register(registry); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		source string
		data   map[string]any
		want   string
	}{
		{
			name: "valid", source: `{{ .data.name }} {{ .vars.application }}`,
			data: map[string]any{"name": map[string]any{"expr": "vars.application"}},
		},
		{
			name: "unknown data", source: `{{ .data.nmae }}`,
			data: map[string]any{"name": "billing"}, want: `field "nmae" is not available in data`,
		},
		{
			name: "unknown expression variable", source: `{{ .data.name }}`,
			data: map[string]any{"name": map[string]any{"expr": "vars.aplication"}}, want: `variable "aplication" is not declared`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			definition := testDefinition(t, test.name, workflow.Step{
				ID: "render", Type: "template", With: map[string]any{"source": test.source, "data": test.data},
			})
			definition.Vars = map[string]any{"application": "billing"}
			err := New(registry).Validate(t.Context(), definition, Options{RunDir: t.TempDir()})
			if test.want == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate() error = %v, want %q", err, test.want)
			}
		})
	}
}
