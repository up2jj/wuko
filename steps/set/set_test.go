package set

import (
	"math"
	"strings"
	"testing"

	"github.com/up2jj/wuko/step"
)

func TestLiteralValue(t *testing.T) {
	value := map[string]any{"enabled": true, "regions": []any{"eu", "us"}}
	runner, err := New(map[string]any{"variable": "deployment", "value": value})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(t.Context(), step.Request{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Variables["deployment"].(map[string]any)["enabled"] != true {
		t.Fatalf("result = %#v", result)
	}
}

func TestExpressionUsesAllRoots(t *testing.T) {
	runner, err := New(map[string]any{
		"variable": "artifact",
		"expr":     `steps.release.version + "-" + vars.target + "-" + inputs.suffix + "-" + env.CHANNEL + "-" + workflow.name + "-" + run.dir`,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(t.Context(), step.Request{
		WorkflowName: "publish", RunDir: "/work",
		Inputs: map[string]any{"suffix": "archive"}, Vars: map[string]any{"target": "linux"},
		Env: map[string]string{"CHANNEL": "stable"}, Steps: map[string]any{"release": map[string]any{"version": "v1"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := result.Outputs["value"]; got != "v1-linux-archive-stable-publish-/work" {
		t.Fatalf("value = %#v", got)
	}
}

func TestExpressionUsesControlBindings(t *testing.T) {
	runner, err := New(map[string]any{"variable": "value", "expr": `foreach.index + matrix.offset`})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(t.Context(), step.Request{Bindings: map[string]any{
		"foreach": map[string]any{"index": 2}, "matrix": map[string]any{"offset": 3},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outputs["value"] != 5 {
		t.Fatalf("result = %#v", result)
	}
}

func TestExpressionFailureDoesNotReturnResult(t *testing.T) {
	runner, err := New(map[string]any{"variable": "value", "expr": `vars.missing.name`})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(t.Context(), step.Request{Vars: map[string]any{}})
	if err == nil || len(result.Outputs) != 0 || len(result.Variables) != 0 {
		t.Fatalf("result = %#v, error = %v", result, err)
	}
}

func TestRejectsInvalidConfigurationAndValue(t *testing.T) {
	tests := []map[string]any{
		{"value": true},
		{"variable": "x"},
		{"variable": "x", "value": true, "expr": "true"},
		{"variable": "x", "expr": ""},
		{"variable": "x", "expr": "vars."},
		{"variable": "x", "value": math.Inf(1)},
		{"variable": "x", "value": true, "unknown": true},
	}
	for _, raw := range tests {
		if _, err := New(raw); err == nil {
			t.Fatalf("New(%#v) succeeded", raw)
		} else if strings.TrimSpace(err.Error()) == "" {
			t.Fatal("empty error")
		}
	}
}
