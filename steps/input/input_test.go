package input

import (
	"bytes"
	"encoding/json"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/up2jj/wuko/step"
)

func TestInputAcceptsPrepopulatedValue(t *testing.T) {
	runner, err := New(map[string]any{"variable": "name", "message": "Enter the release name", "value": "from an earlier step"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(t.Context(), step.Request{
		Vars:        map[string]any{},
		Stdin:       bytes.NewBufferString("\r"),
		Stdout:      io.Discard,
		Interactive: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outputs["value"] != "from an earlier step" {
		t.Fatalf("value = %q", result.Outputs["value"])
	}
}

func TestInputUsesPreSuppliedVariable(t *testing.T) {
	runner, err := New(map[string]any{"variable": "name", "message": "Name", "value": "suggested"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(t.Context(), step.Request{Vars: map[string]any{"name": "chosen"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Variables["name"] != "chosen" || result.Outputs["value"] != "chosen" {
		t.Fatalf("result = %#v", result)
	}
}

func TestInputSplitsValueByPattern(t *testing.T) {
	runner, err := New(map[string]any{
		"variable":  "reviewers",
		"message":   "Enter reviewers",
		"modifiers": map[string]any{"split": `\s*[,;]\s*`},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(t.Context(), step.Request{
		Vars: map[string]any{"reviewers": "alice, bob;carol"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []any{"alice", "bob", "carol"}
	output, outputOK := result.Outputs["value"].([]any)
	variable, variableOK := result.Variables["reviewers"].([]any)
	if !outputOK || !variableOK || !slices.Equal(output, want) || !slices.Equal(variable, want) {
		t.Fatalf("result = %#v, want %#v", result, want)
	}
}

func TestInputTrimsValueBeforeValidation(t *testing.T) {
	runner, err := New(map[string]any{
		"variable": "project",
		"message":  "Enter project",
		"validation": map[string]any{
			"pattern": `^[a-z]+$`,
		},
		"modifiers": map[string]any{"trim": true},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(t.Context(), step.Request{
		Vars: map[string]any{"project": " \t wuko \n"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outputs["value"] != "wuko" || result.Variables["project"] != "wuko" {
		t.Fatalf("result = %#v", result)
	}
}

func TestInputTrimCombinesWithSplit(t *testing.T) {
	runner, err := New(map[string]any{
		"variable":  "reviewers",
		"message":   "Enter reviewers",
		"modifiers": map[string]any{"trim": true, "split": ","},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(t.Context(), step.Request{
		Vars: map[string]any{"reviewers": " alice , bob , carol "},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []any{"alice", "bob", "carol"}
	got, ok := result.Outputs["value"].([]any)
	if !ok || !slices.Equal(got, want) {
		t.Fatalf("value = %#v, want %#v", result.Outputs["value"], want)
	}
}

func TestInputDecodesJSON(t *testing.T) {
	runner, err := New(map[string]any{
		"variable":  "metadata",
		"message":   "Enter metadata",
		"modifiers": map[string]any{"json": true},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(t.Context(), step.Request{
		Vars: map[string]any{"metadata": `{"enabled":true,"retries":3}`},
	})
	if err != nil {
		t.Fatal(err)
	}
	value, ok := result.Outputs["value"].(map[string]any)
	if !ok || value["enabled"] != true || value["retries"] != json.Number("3") {
		t.Fatalf("value = %#v", result.Outputs["value"])
	}
}

func TestInputRejectsInvalidJSON(t *testing.T) {
	runner, err := New(map[string]any{
		"variable":  "metadata",
		"message":   "Enter metadata",
		"modifiers": map[string]any{"json": true},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.Run(t.Context(), step.Request{Vars: map[string]any{"metadata": `{"broken":`}})
	if err == nil || !strings.Contains(err.Error(), "valid JSON") {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestInputRejectsMultipleJSONValues(t *testing.T) {
	runner, err := New(map[string]any{
		"variable":  "metadata",
		"message":   "Enter metadata",
		"modifiers": map[string]any{"json": true},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.Run(t.Context(), step.Request{Vars: map[string]any{"metadata": `{}` + `[]`}})
	if err == nil || !strings.Contains(err.Error(), "exactly one JSON value") {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestInputModifiersAcceptOptionalEmptyValue(t *testing.T) {
	optional := false
	tests := []struct {
		name      string
		modifiers map[string]any
		want      any
	}{
		{name: "split", modifiers: map[string]any{"split": ","}, want: []any{}},
		{name: "json", modifiers: map[string]any{"json": true}, want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner, err := New(map[string]any{
				"variable": "value", "message": "Enter value", "required": optional, "modifiers": tt.modifiers,
			})
			if err != nil {
				t.Fatal(err)
			}
			result, err := runner.Run(t.Context(), step.Request{Vars: map[string]any{"value": ""}})
			if err != nil {
				t.Fatal(err)
			}
			if tt.want == nil {
				if result.Outputs["value"] != nil {
					t.Fatalf("value = %#v, want nil", result.Outputs["value"])
				}
				return
			}
			got, ok := result.Outputs["value"].([]any)
			if !ok || len(got) != 0 {
				t.Fatalf("value = %#v, want empty list", result.Outputs["value"])
			}
		})
	}
}

func TestInputRejectsInvalidModifierConfiguration(t *testing.T) {
	tests := []map[string]any{
		{"split": "["},
		{"split": ",", "json": true},
	}
	for _, modifiers := range tests {
		_, err := New(map[string]any{
			"variable": "value", "message": "Enter value", "modifiers": modifiers,
		})
		if err == nil {
			t.Fatalf("modifiers %#v: expected error", modifiers)
		}
	}
}

func TestInputRejectsNonStringVariable(t *testing.T) {
	runner, err := New(map[string]any{"variable": "name", "message": "Name"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(t.Context(), step.Request{Vars: map[string]any{"name": 42}}); err == nil {
		t.Fatal("expected non-string variable error")
	}
}

func TestInputValidatesPreSuppliedVariable(t *testing.T) {
	runner, err := New(map[string]any{
		"variable": "name",
		"message":  "Name",
		"validation": map[string]any{
			"min_length": 3,
			"pattern":    `^[a-z]+$`,
			"message":    "Use at least three lowercase letters",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.Run(t.Context(), step.Request{Vars: map[string]any{"name": "AB"}})
	if err == nil || !strings.Contains(err.Error(), "Use at least three lowercase letters") {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestInputRejectsInvalidValidationConfiguration(t *testing.T) {
	_, err := New(map[string]any{
		"variable":   "name",
		"message":    "Name",
		"validation": map[string]any{"min_length": 4, "max_length": 2},
	})
	if err == nil {
		t.Fatal("expected invalid validation configuration error")
	}
}

func TestInputFailsNonInteractive(t *testing.T) {
	runner, err := New(map[string]any{"variable": "name", "message": "Name", "value": "suggested"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(t.Context(), step.Request{}); err == nil {
		t.Fatal("expected non-interactive input error")
	}
}
