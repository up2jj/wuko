package assert

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/up2jj/wuko/step"
)

func TestAssertionUsesAllExpressionRoots(t *testing.T) {
	runner, err := New(map[string]any{
		"expr":    `inputs.enabled && vars.enabled && env.MODE == "test" && steps.prepare.ready && workflow.name == "release" && workflow.dir == "/workflow" && run.dir == "/run"`,
		"message": "release is not ready",
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(t.Context(), step.Request{
		Inputs:       map[string]any{"enabled": true},
		Vars:         map[string]any{"enabled": true},
		Env:          map[string]string{"MODE": "test"},
		Steps:        map[string]any{"prepare": map[string]any{"ready": true}},
		WorkflowName: "release",
		WorkflowDir:  "/workflow",
		RunDir:       "/run",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outputs != nil || result.Variables != nil {
		t.Fatalf("result = %#v, want empty result", result)
	}
}

func TestAssertionUsesControlBindings(t *testing.T) {
	runner, err := New(map[string]any{"expr": `foreach.item == "api" && matrix.os == "linux"`, "message": "wrong binding"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.Run(t.Context(), step.Request{Bindings: map[string]any{
		"foreach": map[string]any{"item": "api"}, "matrix": map[string]any{"os": "linux"},
	}})
	if err != nil {
		t.Fatal(err)
	}
}

func TestFalseAssertionReturnsConfiguredMessage(t *testing.T) {
	runner, err := New(map[string]any{"expr": "false", "message": "build must succeed"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(t.Context(), step.Request{})
	if err == nil || err.Error() != "assertion failed: build must succeed" {
		t.Fatalf("error = %v", err)
	}
	if result.Outputs != nil || result.Variables != nil {
		t.Fatalf("result = %#v, want empty result", result)
	}
}

func TestExpressionEvaluationFailure(t *testing.T) {
	runner, err := New(map[string]any{"expr": "vars.missing.ready", "message": "not ready"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(t.Context(), step.Request{Vars: map[string]any{}})
	if err == nil || !strings.Contains(err.Error(), "evaluating expr:") {
		t.Fatalf("error = %v", err)
	}
	if strings.Contains(err.Error(), "assertion failed:") {
		t.Fatalf("evaluation error reported as assertion failure: %v", err)
	}
	if result.Outputs != nil || result.Variables != nil {
		t.Fatalf("result = %#v, want empty result", result)
	}
}

func TestRejectsInvalidConfiguration(t *testing.T) {
	tests := []struct {
		name string
		raw  map[string]any
	}{
		{name: "missing expr", raw: map[string]any{"message": "failed"}},
		{name: "empty expr", raw: map[string]any{"expr": "  ", "message": "failed"}},
		{name: "invalid expr", raw: map[string]any{"expr": "vars.", "message": "failed"}},
		{name: "non boolean expr", raw: map[string]any{"expr": "42", "message": "failed"}},
		{name: "missing message", raw: map[string]any{"expr": "true"}},
		{name: "empty message", raw: map[string]any{"expr": "true", "message": "  "}},
		{name: "unknown field", raw: map[string]any{"expr": "true", "message": "failed", "unknown": true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := New(tt.raw); err == nil {
				t.Fatalf("New(%#v) succeeded", tt.raw)
			}
		})
	}
}

func TestAssertionPropagatesCancellation(t *testing.T) {
	runner, err := New(map[string]any{"expr": "true", "message": "failed"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	result, err := runner.Run(ctx, step.Request{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context canceled", err)
	}
	if result.Outputs != nil || result.Variables != nil {
		t.Fatalf("result = %#v, want empty result", result)
	}
}
