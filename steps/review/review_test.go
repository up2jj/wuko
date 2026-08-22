package review

import (
	"strings"
	"testing"

	"github.com/up2jj/wuko/step"
)

func TestConfigDefaultsAndValidation(t *testing.T) {
	runnerValue, err := New(map[string]any{"variable": "approved", "message": "Review", "content": "change"})
	if err != nil {
		t.Fatal(err)
	}
	if runnerValue.(*Runner).config.Format != formatPlain {
		t.Fatalf("format = %q", runnerValue.(*Runner).config.Format)
	}

	tests := []map[string]any{
		{"message": "Review", "content": "change"},
		{"variable": "approved", "content": "change"},
		{"variable": "approved", "message": "Review", "content": ""},
		{"variable": "approved", "message": "Review", "content": "change", "format": "markdown"},
		{"variable": "approved", "message": "Review", "content": "change", "unknown": true},
	}
	for _, raw := range tests {
		if _, err := New(raw); err == nil {
			t.Fatalf("New(%#v) succeeded", raw)
		}
	}
}

func TestPreSuppliedDecision(t *testing.T) {
	runner := newRunner(t)
	for _, approved := range []bool{false, true} {
		result, err := runner.Run(t.Context(), step.Request{Vars: map[string]any{"approved": approved}})
		if err != nil {
			t.Fatal(err)
		}
		if result.Outputs["value"] != approved || result.Variables["approved"] != approved {
			t.Fatalf("result = %#v", result)
		}
	}
}

func TestNonInteractiveAndInvalidSuppliedValues(t *testing.T) {
	runner := newRunner(t)
	_, err := runner.Run(t.Context(), step.Request{Vars: map[string]any{}, Interactive: false})
	if err == nil || !strings.Contains(err.Error(), "supply it with --var") {
		t.Fatalf("error = %v", err)
	}
	_, err = runner.Run(t.Context(), step.Request{Vars: map[string]any{"approved": "yes"}})
	if err == nil || !strings.Contains(err.Error(), "must be a boolean") {
		t.Fatalf("error = %v", err)
	}
}

func newRunner(t *testing.T) *Runner {
	t.Helper()
	runner, err := New(map[string]any{"variable": "approved", "message": "Review", "content": "change", "format": "diff"})
	if err != nil {
		t.Fatal(err)
	}
	return runner.(*Runner)
}
