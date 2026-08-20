package confirm

import (
	"context"
	"strings"
	"testing"

	"github.com/up2jj/wuko/step"
)

func TestPreSuppliedBoolean(t *testing.T) {
	runner, err := New(map[string]any{"variable": "approved", "message": "Continue?"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(t.Context(), step.Request{Vars: map[string]any{"approved": true}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outputs["value"] != true || result.Variables["approved"] != true {
		t.Fatalf("result = %#v", result)
	}
}

func TestNonInteractiveRequiresBooleanVariable(t *testing.T) {
	runner, err := New(map[string]any{"variable": "approved", "message": "Continue?", "default": true})
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.Run(context.Background(), step.Request{Vars: map[string]any{}, Interactive: false})
	if err == nil || !strings.Contains(err.Error(), "supply it with --var") {
		t.Fatalf("error = %v", err)
	}
	_, err = runner.Run(context.Background(), step.Request{Vars: map[string]any{"approved": "true"}})
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
