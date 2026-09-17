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
