package tui

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/up2jj/wuko/validation"
)

func TestWriteValidationRendersCompilerStyleIssues(t *testing.T) {
	root := t.TempDir()
	issues := &validation.Error{Issues: []validation.Issue{
		{Code: validation.CodeUnknownStepOutput, Message: `step "fetch" has no output "sttaus"`, Hint: `did you mean "status"?`, Path: "steps[1].with.value", Span: validation.Span{Source: filepath.Join(root, "workflow.yaml"), Line: 9, Column: 18, EndLine: 9, EndColumn: 23}, SourceLine: `value: "{{ .steps.fetch.sttaus }}"`},
		{Code: validation.CodeMissingField, Message: `missing required field "type"`},
	}}
	var output bytes.Buffer
	if !WriteValidation(&output, issues, root) {
		t.Fatal("issue was not rendered")
	}
	for _, want := range []string{"workflow.yaml:9:18: error[unknown_step_output]", "path: steps[1].with.value", "^^^^^^", `help: did you mean "status"?`, "2 validation issues"} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("output = %q, want %q", output.String(), want)
		}
	}
}

func TestWriteValidationColorStylesDiagnosticsOnlyWhenEnabled(t *testing.T) {
	issues := &validation.Error{Issues: []validation.Issue{
		{Code: validation.CodeMissingField, Message: `missing required field "type"`, Hint: "add a step type", Span: validation.Span{Source: "workflow.yaml", Line: 3, Column: 5}, SourceLine: "  - id: build"},
	}}
	var plain, colored bytes.Buffer
	WriteValidationColor(&plain, issues, "", false)
	WriteValidationColor(&colored, issues, "", true)
	if strings.Contains(plain.String(), "\x1b[") {
		t.Fatalf("plain output contains ANSI escapes: %q", plain.String())
	}
	if got := stripANSI(colored.String()); got != plain.String() {
		t.Fatalf("colored text = %q, want %q", got, plain.String())
	}
	for _, want := range []string{"\x1b[36mworkflow.yaml:3:5\x1b[0m", "\x1b[1;31merror[missing_field]:\x1b[0m", "\x1b[1;32mhelp:\x1b[0m"} {
		if !strings.Contains(colored.String(), want) {
			t.Fatalf("colored output = %q, want %q", colored.String(), want)
		}
	}
}

func stripANSI(value string) string {
	var builder strings.Builder
	for index := 0; index < len(value); index++ {
		if value[index] == '\x1b' {
			for index < len(value) && value[index] != 'm' {
				index++
			}
			continue
		}
		builder.WriteByte(value[index])
	}
	return builder.String()
}
