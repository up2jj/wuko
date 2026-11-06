package githubactions

import (
	"os"
	"strings"
	"testing"

	"github.com/up2jj/wuko/engine"
	reporterpkg "github.com/up2jj/wuko/reporter"
	"github.com/up2jj/wuko/step"
)

func TestReporterIncludesRedactedChecksInSummaryAndJSON(t *testing.T) {
	t.Parallel()
	reporter, output, summary, _ := newTestReporter(t, t.TempDir())
	reporter.Progress(engine.ProgressEvent{Kind: engine.WorkflowFinished, RunID: "checks", WorkflowName: "doctor", Status: engine.StatusFailed, Checks: []step.CheckFinding{
		{ID: "ok", Status: "ok", Message: "hidden", Fix: "hidden"},
		{ID: "missing", Status: "fail", Message: "[REDACTED]", Fix: "install | tool\nconfigure <tool>"},
	}})
	if err := reporter.Finish(t.Context(), reporterpkg.Outcome{RunID: "checks", WorkflowName: "doctor", Status: engine.StatusFailed}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(summary)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{"#### Failed checks", "[REDACTED]", "install &#124; tool<br>configure &lt;tool&gt;", "#### Passed checks"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in summary: %s", want, text)
		}
	}
	if strings.Contains(text, "hidden") || strings.Index(text, "Failed checks") > strings.Index(text, "Passed checks") {
		t.Fatalf("summary = %s", text)
	}
	data, err = os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"checks":[`) {
		t.Fatalf("outputs = %s", data)
	}
}
