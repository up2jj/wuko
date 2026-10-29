package confirm

import (
	"maps"
	"strings"
	"testing"

	"github.com/up2jj/wuko/internal/steptest"
	"github.com/up2jj/wuko/step"
)

func TestPreSuppliedBoolean(t *testing.T) {
	runner := steptest.Build(t, New, map[string]any{"variable": "approved", "message": "Continue?"})
	result, err := runner.Run(t.Context(), step.Request{Vars: map[string]any{"approved": true}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outputs["value"] != true || result.Variables["approved"] != true {
		t.Fatalf("result = %#v", result)
	}
}

func TestNonInteractiveRequiresBooleanVariable(t *testing.T) {
	runner := steptest.Build(t, New, map[string]any{"variable": "approved", "message": "Continue?", "default": true})
	_, err := runner.Run(t.Context(), step.Request{Vars: map[string]any{}, Interactive: false})
	if err == nil || !strings.Contains(err.Error(), "supply it with --var") {
		t.Fatalf("error = %v", err)
	}
	_, err = runner.Run(t.Context(), step.Request{Vars: map[string]any{"approved": "true"}})
	if err == nil || !strings.Contains(err.Error(), "must be a boolean") {
		t.Fatalf("error = %v", err)
	}
}

func TestConfigIsStrict(t *testing.T) {
	for _, raw := range []map[string]any{
		{"message": "Continue?"},
		{"variable": "approved", "message": ""},
		{"variable": "approved", "message": "Continue?", "unknown": true},
	} {
		if _, err := New(raw); err == nil {
			t.Fatalf("New(%#v) succeeded", raw)
		}
	}
}

func TestUnattendedAnswers(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		raw     map[string]any
		request step.Request
		want    bool
	}{
		{"noninteractive default", map[string]any{"default": true, "noninteractive": "default"}, step.Request{}, true},
		{"defaults flag in a terminal", map[string]any{"default": true}, step.Request{Interactive: true, Prompts: step.PromptAnswers{Defaults: true}}, true},
		{"yes flag overrides default", map[string]any{"default": false, "noninteractive": "default"}, step.Request{Prompts: step.PromptAnswers{Yes: true}}, true},
		{"yes flag without default", map[string]any{}, step.Request{Prompts: step.PromptAnswers{Yes: true}}, true},
		{"supplied variable wins over yes", map[string]any{}, step.Request{Vars: map[string]any{"approved": false}, Prompts: step.PromptAnswers{Yes: true}}, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			raw := map[string]any{"variable": "approved", "message": "Continue?"}
			maps.Copy(raw, test.raw)
			result, err := steptest.Build(t, New, raw).Run(t.Context(), test.request)
			if err != nil {
				t.Fatal(err)
			}
			if result.Outputs["value"] != test.want || result.Variables["approved"] != test.want {
				t.Fatalf("result = %#v, want %t", result, test.want)
			}
		})
	}
}

func TestUnattendedWithoutPolicyHintsAtDefault(t *testing.T) {
	t.Parallel()
	runner := steptest.Build(t, New, map[string]any{"variable": "approved", "message": "Continue?", "default": true})
	_, err := runner.Run(t.Context(), step.Request{})
	if err == nil || !strings.Contains(err.Error(), "noninteractive: default") {
		t.Fatalf("error = %v", err)
	}
}

func TestNonInteractivePolicyIsValidated(t *testing.T) {
	t.Parallel()
	for _, raw := range []map[string]any{
		{"variable": "approved", "message": "Continue?", "noninteractive": "default"},
		{"variable": "approved", "message": "Continue?", "default": true, "noninteractive": "skip"},
	} {
		if _, err := New(raw); err == nil {
			t.Fatalf("New(%#v) succeeded", raw)
		}
	}
}
