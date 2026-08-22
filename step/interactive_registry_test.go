package step_test

import (
	"strings"
	"testing"

	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/steps/choice"
	"github.com/up2jj/wuko/steps/confirm"
	inputstep "github.com/up2jj/wuko/steps/input"
	passwordstep "github.com/up2jj/wuko/steps/password"
	pathstep "github.com/up2jj/wuko/steps/path"
	"github.com/up2jj/wuko/steps/review"
)

func TestInteractiveStepRegistrationsUseTUIPrefix(t *testing.T) {
	registry := step.NewRegistry()
	for _, register := range []func(*step.Registry) error{
		inputstep.Register, passwordstep.Register, choice.Register, pathstep.Register, review.Register, confirm.Register,
	} {
		if err := register(registry); err != nil {
			t.Fatal(err)
		}
	}

	steps := []struct {
		name string
		raw  map[string]any
	}{
		{name: "tui_input", raw: map[string]any{"variable": "value", "message": "Value"}},
		{name: "tui_password", raw: map[string]any{"variable": "value", "message": "Value"}},
		{name: "tui_choice", raw: map[string]any{
			"variable": "value", "message": "Value",
			"choices": []any{map[string]any{"label": "Value", "value": "value"}},
		}},
		{name: "tui_path", raw: map[string]any{"variable": "value", "message": "Value"}},
		{name: "tui_review", raw: map[string]any{"variable": "value", "message": "Value", "content": "Change"}},
		{name: "tui_confirm", raw: map[string]any{"variable": "value", "message": "Value"}},
	}
	for _, workflowStep := range steps {
		t.Run(workflowStep.name, func(t *testing.T) {
			if _, err := registry.Build(workflowStep.name, workflowStep.raw); err != nil {
				t.Fatalf("Build(%q) error = %v", workflowStep.name, err)
			}
		})
	}

	for _, legacyName := range []string{"input", "password", "choice", "path", "review", "confirm"} {
		t.Run("rejects_"+legacyName, func(t *testing.T) {
			_, err := registry.Build(legacyName, nil)
			if err == nil || !strings.Contains(err.Error(), "unknown step type") {
				t.Fatalf("Build(%q) error = %v", legacyName, err)
			}
		})
	}
}
