package engine

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/up2jj/wuko/internal/steptest"
	"github.com/up2jj/wuko/internal/testfs"
	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/validation"
	"github.com/up2jj/wuko/workflow"
)

func TestReferenceIssueCarriesYAMLPathSpanExcerptAndReferenceTarget(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "workflow.yaml")
	data := `version: 1
name: source
steps:
  - id: fetch
    type: producer
  - id: use
    type: capture
    with:
      value: "{{ .steps.fetch.sttaus }}"
`
	testfs.WriteFile(t, path, []byte(data), 0o644)
	definition, err := workflow.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	registry := step.NewRegistry()
	if err := registry.RegisterDefinition("producer", step.Registration{
		Builder: func(map[string]any) (step.Runner, error) {
			return steptest.RunnerFunc(func(context.Context, step.Request) (step.Result, error) { return step.Result{}, nil }), nil
		},
		Outputs: step.ClosedOutputs("status"),
	}); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register("capture", func(map[string]any) (step.Runner, error) {
		return steptest.RunnerFunc(func(context.Context, step.Request) (step.Result, error) { return step.Result{}, nil }), nil
	}); err != nil {
		t.Fatal(err)
	}
	err = New(registry).Validate(t.Context(), definition, Options{})
	issues := validation.Issues(err)
	if len(issues) != 1 {
		t.Fatalf("issues = %#v", issues)
	}
	issue := issues[0]
	if issue.Path != "steps[1]" || issue.Target != "steps.fetch.sttaus" || issue.Span.Source != path || issue.Span.Line != 6 {
		t.Fatalf("issue = %#v", issue)
	}
	if !strings.Contains(issue.SourceLine, "- id: use") {
		t.Fatalf("excerpt = %q", issue.SourceLine)
	}
}
