package git

import (
	"context"
	"errors"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/up2jj/wuko/step"
	"gopkg.in/yaml.v3"
)

func TestNewConventionalCommitValidatesConfiguration(t *testing.T) {
	tests := []struct {
		name string
		raw  map[string]any
		want string
	}{
		{"missing operation", map[string]any{}, "operation is required"},
		{"unknown operation", map[string]any{"operation": "commit"}, "create or validate"},
		{"templated operation", map[string]any{"operation": "{{ .vars.operation }}"}, "must not be templated"},
		{"create missing type", map[string]any{"operation": "create", "subject": "message"}, "type is required"},
		{"create missing subject", map[string]any{"operation": "create", "type": "feat"}, "subject is required"},
		{"create message", map[string]any{"operation": "create", "type": "feat", "subject": "message", "message": "feat: message"}, "does not accept message"},
		{"create strict", map[string]any{"operation": "create", "type": "feat", "subject": "message", "strict": true}, "does not accept strict"},
		{"create regex without task", map[string]any{"operation": "create", "type": "feat", "subject": "message", "task_regex": `WUKO-[0-9]+`}, "requires task"},
		{"create invalid variable", map[string]any{"operation": "create", "type": "feat", "subject": "message", "variable": "bad name"}, "invalid variable"},
		{"validate missing message", map[string]any{"operation": "validate"}, "message is required"},
		{"validate create field", map[string]any{"operation": "validate", "message": "feat: message", "task": "WUKO-1"}, "does not accept task"},
		{"validate bad message", map[string]any{"operation": "validate", "message": "bad message"}, "invalid conventional commit"},
		{"unknown field", map[string]any{"operation": "create", "type": "feat", "subject": "message", "unknown": true}, "field unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewConventionalCommit(tt.raw)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestConventionalCommitCreateReturnsStructuredOutputAndVariable(t *testing.T) {
	runner, err := NewConventionalCommit(map[string]any{
		"operation": "create", "type": "feat", "scope": "workflow", "subject": "add commit support",
		"breaking": true, "body": "A body.", "task": "WUKO-142", "variable": "commit_message",
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(t.Context(), step.Request{})
	if err != nil {
		t.Fatal(err)
	}
	want := "feat(workflow)!: add commit support WUKO-142\n\nA body."
	if result.Outputs["message"] != want || result.Outputs["value"] != want || result.Outputs["cleaned_message"] != want ||
		result.Outputs["valid"] != true || result.Outputs["classification"] != "conventional" || result.Outputs["type"] != "feat" ||
		result.Outputs["scope"] != "workflow" || result.Outputs["subject"] != "add commit support" || result.Outputs["breaking"] != true ||
		result.Outputs["body"] != "A body." || result.Outputs["task"] != "WUKO-142" {
		t.Fatalf("outputs = %#v", result.Outputs)
	}
	if result.Variables["commit_message"] != want {
		t.Fatalf("variables = %#v", result.Variables)
	}
}

func TestConventionalCommitCreateOptionallyValidatesTask(t *testing.T) {
	valid, err := NewConventionalCommit(map[string]any{
		"operation": "create", "type": "fix", "subject": "correct sessions", "task": "WUKO-12", "task_regex": `WUKO-[0-9]+`,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := valid.Run(t.Context(), step.Request{})
	if err != nil || result.Outputs["task"] != "WUKO-12" || result.Outputs["subject"] != "correct sessions" {
		t.Fatalf("result = %#v, error = %v", result, err)
	}
	if _, err := NewConventionalCommit(map[string]any{
		"operation": "create", "type": "fix", "subject": "correct sessions", "task": "OTHER-12", "task_regex": `WUKO-[0-9]+`,
	}); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("mismatch error = %v", err)
	}
}

func TestConventionalCommitValidateReturnsStructuredOutput(t *testing.T) {
	message := "fix(auth): correct sessions WUKO-12\n\nClear cached credentials."
	runner, err := NewConventionalCommit(map[string]any{
		"operation": "validate", "message": message, "strict": true, "types": []any{"fix"},
		"scopes": []any{"auth"}, "force_scope": true, "task_regex": `WUKO-[0-9]+`,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(t.Context(), step.Request{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outputs["message"] != message || result.Outputs["task"] != "WUKO-12" || result.Outputs["subject"] != "correct sessions" ||
		result.Outputs["body"] != "Clear cached credentials." || result.Variables != nil {
		t.Fatalf("result = %#v", result)
	}
}

func TestConventionalCommitDefersTemplatedValues(t *testing.T) {
	runner, err := NewConventionalCommit(map[string]any{
		"operation": "create", "type": "{{ .vars.type }}", "subject": "{{ .vars.subject }}",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(t.Context(), step.Request{}); err == nil || !strings.Contains(err.Error(), "unresolved template") {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestConventionalCommitPropagatesCancellation(t *testing.T) {
	runner, err := NewConventionalCommit(map[string]any{"operation": "create", "type": "feat", "subject": "message"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := runner.Run(ctx, step.Request{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
}

func TestConventionalCommitDocumentationExamples(t *testing.T) {
	data, err := os.ReadFile("../../docs/steps-system.md")
	if err != nil {
		t.Fatal(err)
	}
	section := regexp.MustCompile(`(?s)## \x60git_conventional_commit\x60\n(.*?)\n## \x60git_commit\x60`).FindSubmatch(data)
	if section == nil {
		t.Fatal("git_conventional_commit documentation section not found")
	}
	blocks := regexp.MustCompile("(?s)```yaml\\n(.*?)```").FindAllSubmatch(section[1], -1)
	if len(blocks) < 7 {
		t.Fatalf("found %d YAML examples, want at least 7", len(blocks))
	}
	for index, block := range blocks {
		var steps []struct {
			Type string         `yaml:"type"`
			With map[string]any `yaml:"with"`
		}
		if err := yaml.Unmarshal(block[1], &steps); err != nil {
			t.Fatalf("example %d: %v", index+1, err)
		}
		if len(steps) != 1 || steps[0].Type != "git_conventional_commit" {
			t.Fatalf("example %d steps = %#v", index+1, steps)
		}
		if _, err := NewConventionalCommit(steps[0].With); err != nil {
			t.Fatalf("example %d: %v", index+1, err)
		}
	}
}

func TestConventionalCommitAllowsBracesInFreeText(t *testing.T) {
	message := "fix(tmpl): escape {{ .Values.name }} in the chart"
	runner, err := NewConventionalCommit(map[string]any{"operation": "validate", "message": message})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(t.Context(), step.Request{})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Outputs["subject"] != "escape {{ .Values.name }} in the chart" {
		t.Fatalf("result = %#v", result)
	}

	runner, err = NewConventionalCommit(map[string]any{
		"operation": "create", "type": "docs", "subject": "document templates",
		"body": "Use {{ .vars.name }} inside the workflow.",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(t.Context(), step.Request{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
}
