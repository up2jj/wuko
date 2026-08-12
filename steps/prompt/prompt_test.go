package prompt

import (
	"testing"

	"github.com/up2jj/wuko/step"
)

func TestPromptUsesPreSuppliedVariable(t *testing.T) {
	runner, err := New(map[string]any{"variable": "task", "message": "Task"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(t.Context(), step.Request{Vars: map[string]any{"task": "TASK-1"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Variables["task"] != "TASK-1" || result.Outputs["value"] != "TASK-1" {
		t.Fatalf("result = %#v", result)
	}
}

func TestPromptFailsNonInteractive(t *testing.T) {
	runner, err := New(map[string]any{"variable": "task", "message": "Task"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(t.Context(), step.Request{}); err == nil {
		t.Fatal("expected non-interactive prompt error")
	}
}
