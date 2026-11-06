package tui

import (
	"bytes"
	"testing"

	"github.com/up2jj/wuko/engine"
	"github.com/up2jj/wuko/step"
)

func TestProgressGroupsChecksAndFormatsMultilineFixes(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	NewProgress(&output, false).Report(engine.ProgressEvent{Kind: engine.WorkflowFinished, Status: engine.StatusFailed, WorkflowName: "doctor", Checks: []step.CheckFinding{
		{ID: "ready", Status: "ok", Message: "hidden success message", Fix: "hidden success fix"},
		{ID: "optional", Status: "warn", Message: "Optional tool missing", Fix: "install optional"},
		{ID: "required", Status: "fail", Message: "Required tool missing", Fix: "install required\nconfigure required\n"},
	}})
	want := "Failed checks:\n  ✗ required: Required tool missing\n    Fix:\n      install required\n      configure required\nWarnings:\n  ! optional: Optional tool missing\n    Fix:\n      install optional\nPassed checks:\n  ✓ ready\n✗ Workflow doctor failed in 0s\n"
	if output.String() != want {
		t.Fatalf("output = %q, want %q", output.String(), want)
	}
}
