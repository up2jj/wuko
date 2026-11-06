package check

import (
	"context"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/up2jj/wuko/internal/steptest"
	"github.com/up2jj/wuko/step"
)

func TestChecksEvaluateEveryEntry(t *testing.T) {
	t.Parallel()
	runner := steptest.Build(t, New, map[string]any{"checks": []any{
		map[string]any{"id": "passing", "expr": "true", "message": "ready"},
		map[string]any{"id": "failing", "expr": "false", "message": "missing", "fix": "install\nconfigure"},
		map[string]any{"id": "warning", "expr": "false", "message": "optional", "severity": "warn"},
	}})
	result, err := runner.Run(t.Context(), step.Request{})
	if err != nil {
		t.Fatal(err)
	}
	want := []step.CheckFinding{{ID: "passing", Status: "ok", Message: "ready"}, {ID: "failing", Status: "fail", Message: "missing", Fix: "install\nconfigure"}, {ID: "warning", Status: "warn", Message: "optional"}}
	if diff := cmp.Diff(want, result.Checks); diff != "" {
		t.Fatalf("findings mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(step.CheckValues(want), result.Outputs["checks"]); diff != "" {
		t.Fatalf("outputs mismatch (-want +got):\n%s", diff)
	}
}

func TestChecksRejectInvalidConfiguration(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		config map[string]any
		want   string
	}{
		{"missing checks", map[string]any{}, "checks must not be empty"},
		{"empty checks", map[string]any{"checks": []any{}}, "checks must not be empty"},
		{"missing id", map[string]any{"expr": "true", "message": "ready"}, "id is required"},
		{"missing expression", map[string]any{"id": "tool", "message": "ready"}, "expr is required"},
		{"missing message", map[string]any{"id": "tool", "expr": "true"}, "message is required"},
		{"invalid severity", map[string]any{"id": "tool", "expr": "true", "message": "ready", "severity": "error"}, "severity must be fail or warn"},
		{"nonboolean expression", map[string]any{"id": "tool", "expr": "42", "message": "ready"}, "expr"},
		{"invalid expression", map[string]any{"id": "tool", "expr": "(", "message": "ready"}, "expr"},
		{"unknown field", map[string]any{"id": "tool", "expr": "true", "message": "ready", "typo": true}, "typo"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			config := test.config
			if test.name != "missing checks" && test.name != "empty checks" {
				config = map[string]any{"checks": []any{config}}
			}
			_, err := New(config)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestChecksDiscardPartialResultsOnExecutionError(t *testing.T) {
	t.Parallel()
	runner := steptest.Build(t, New, map[string]any{"checks": []any{
		map[string]any{"id": "first", "expr": "false", "message": "missing"},
		map[string]any{"id": "second", "expr": "vars.absent.value == 1", "message": "missing"},
	}})
	result, err := runner.Run(t.Context(), step.Request{Vars: map[string]any{}})
	if err == nil || len(result.Checks) != 0 || len(result.Outputs) != 0 {
		t.Fatalf("result = %#v, error = %v", result, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	result, err = runner.Run(ctx, step.Request{})
	if err != context.Canceled || len(result.Checks) != 0 {
		t.Fatalf("result = %#v, error = %v", result, err)
	}
}
