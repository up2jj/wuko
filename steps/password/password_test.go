package password

import (
	"bytes"
	"strings"
	"testing"

	"github.com/up2jj/wuko/step"
)

func TestPasswordMasksInteractiveInput(t *testing.T) {
	runner, err := New(map[string]any{"variable": "token", "message": "Token"})
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	result, err := runner.Run(t.Context(), step.Request{
		Vars:        map[string]any{},
		Stdin:       bytes.NewBufferString("secret\r"),
		Stdout:      &output,
		Interactive: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outputs["value"] != "secret" {
		t.Fatalf("value = %q", result.Outputs["value"])
	}
	if strings.Contains(output.String(), "secret") {
		t.Fatalf("output exposes password: %q", output.String())
	}
}

func TestPasswordUsesPreSuppliedVariable(t *testing.T) {
	runner, err := New(map[string]any{"variable": "token", "message": "Token"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(t.Context(), step.Request{Vars: map[string]any{"token": "secret"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Variables["token"] != "secret" || result.Outputs["value"] != "secret" {
		t.Fatalf("result = %#v", result)
	}
}

func TestPasswordRejectsEmptyRequiredVariable(t *testing.T) {
	runner, err := New(map[string]any{"variable": "token", "message": "Token"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(t.Context(), step.Request{Vars: map[string]any{"token": " "}}); err == nil {
		t.Fatal("expected empty password error")
	}
}

func TestPasswordValidationDoesNotExposeValue(t *testing.T) {
	runner, err := New(map[string]any{
		"variable":   "token",
		"message":    "Token",
		"validation": map[string]any{"min_length": 12},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.Run(t.Context(), step.Request{Vars: map[string]any{"token": "secret"}})
	if err == nil {
		t.Fatal("expected password validation error")
	}
	if strings.Contains(err.Error(), "secret") {
		t.Fatalf("error exposes password: %q", err)
	}
}

func TestPasswordFailsNonInteractive(t *testing.T) {
	runner, err := New(map[string]any{"variable": "token", "message": "Token"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(t.Context(), step.Request{}); err == nil {
		t.Fatal("expected non-interactive password error")
	}
}
