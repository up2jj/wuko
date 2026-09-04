package githubactions

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/up2jj/wuko/diagnostic"
	"github.com/up2jj/wuko/engine"
	reporterpkg "github.com/up2jj/wuko/reporter"
)

func TestNewRequiresGitHubFiles(t *testing.T) {
	tests := []struct {
		name    string
		options Options
		want    string
	}{
		{name: "output", options: Options{SummaryPath: "summary"}, want: "GITHUB_OUTPUT is required"},
		{name: "summary", options: Options{OutputPath: "output"}, want: "GITHUB_STEP_SUMMARY is required"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := New(test.options)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("New() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestReporterAnnotatesDiagnosticsOnceAndWritesSummary(t *testing.T) {
	root := t.TempDir()
	reporter, output, summary, commands := newTestReporter(t, root)
	event := diagnostic.Event{
		Phase: diagnostic.PhaseValidation, Status: diagnostic.StatusFailed,
		Location: diagnostic.Location{Source: filepath.Join(root, ".wuko", "workflows", "check.yaml"), Line: 7, Column: 3},
		Message:  "invalid, workflow", Error: errors.New("bad\nvalue"),
	}
	reporter.Diagnostic(event)
	reporter.Diagnostic(event)
	reporter.Progress(engine.ProgressEvent{
		Kind: engine.WorkflowFinished, Status: engine.StatusFailed, WorkflowName: "check", Duration: 1500 * time.Millisecond,
		Stats: engine.RunStats{Total: 2, Succeeded: 1, Failed: 1},
	})
	if err := reporter.Finish(t.Context(), reporterpkg.Outcome{
		InvocationID: "invocation", WorkflowName: "check", Status: engine.StatusFailed, Duration: 2 * time.Second,
		Stats: engine.RunStats{Duration: 1500 * time.Millisecond, Total: 2, Succeeded: 1, Failed: 1},
		Err:   errors.New("bad value"),
	}); err != nil {
		t.Fatal(err)
	}

	annotation := commands.String()
	if strings.Count(annotation, "::error") != 1 {
		t.Fatalf("commands = %q, want one annotation", annotation)
	}
	for _, want := range []string{"file=.wuko/workflows/check.yaml", "line=7", "col=3", "invalid, workflow: bad value"} {
		if !strings.Contains(annotation, want) {
			t.Errorf("commands = %q, want %q", annotation, want)
		}
	}
	values := readGitHubOutputs(t, output)
	if values[statusOutput] != "failed" || values[executionIDOutput] != "invocation" || values[durationMSOutput] != "2000" {
		t.Fatalf("metadata outputs = %#v", values)
	}
	if _, exists := values[aggregateOutput]; exists {
		t.Fatalf("outputs = %#v, failed run must not export workflow outputs", values)
	}
	var executionReport reporterpkg.ExecutionReport
	if err := json.Unmarshal([]byte(values[reportOutput]), &executionReport); err != nil {
		t.Fatalf("decoding execution report %q: %v", values[reportOutput], err)
	}
	if executionReport.Status != engine.StatusFailed || executionReport.Outputs != nil || executionReport.Stats.Steps.Failed != 1 {
		t.Fatalf("execution report = %#v", executionReport)
	}
	summaryData, err := os.ReadFile(summary)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"### Wuko: `check`", "| failed | 1.5s | 2 | 1 | 1 | 0 |"} {
		if !strings.Contains(string(summaryData), want) {
			t.Errorf("summary = %q, want %q", summaryData, want)
		}
	}
}

func TestReporterWritesRichSummaryFromMatchingTerminalProgress(t *testing.T) {
	root := t.TempDir()
	reporter, _, summary, _ := newTestReporter(t, root)
	stats := engine.RunStats{
		Duration: 9 * time.Second, Total: 5, Succeeded: 1, Failed: 2, Skipped: 1, Canceled: 1,
		Attempts: 6, Retries: 3, RetryWait: 1500 * time.Millisecond,
		Polls: 4, PollWait: 2 * time.Second, TimedOut: 1,
		Steps: []engine.StepStats{
			{ID: "build|app", Status: engine.StatusSucceeded, Duration: 3 * time.Second, Attempts: []engine.AttemptStats{{Number: 1}}},
			{ID: "optional", Status: engine.StatusSkipped},
			{
				ID: "integration-test", Status: engine.StatusFailed, Duration: 2 * time.Second,
				Attempts: []engine.AttemptStats{{Number: 1}, {Number: 2}, {Number: 3}}, Polls: 4,
				Location: diagnostic.Location{Source: filepath.Join(root, ".wuko", "workflows", "check.yaml"), Line: 7},
				Error:    errors.New("bad <value>\nsecond & line"),
			},
			{
				ID: "wait-for-api", Status: engine.StatusTimedOut, Duration: 2500 * time.Millisecond,
				Attempts: []engine.AttemptStats{{Number: 1}, {Number: 2}},
				Location: diagnostic.Location{Source: "https://example.test/action.yaml", Line: 9},
				Error:    errors.New("deadline > exceeded"),
			},
			{
				ID: "cleanup", Status: engine.StatusCanceled, Duration: 10 * time.Millisecond,
				Location: diagnostic.Location{Source: filepath.Join(filepath.Dir(root), "outside.yaml"), Line: 3},
			},
		},
	}
	reporter.Progress(engine.ProgressEvent{
		RunID: "run-1", Kind: engine.WorkflowFinished, Status: engine.StatusFailed,
		WorkflowName: "check", Stats: stats,
	})
	stats.Steps[0].ID = "mutated-after-progress"
	if err := reporter.Finish(t.Context(), reporterpkg.Outcome{
		RunID: "run-1", WorkflowName: "check", Status: engine.StatusFailed,
		Stats: engine.RunStats{Total: 99}, Err: errors.New("failed"),
	}); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(summary)
	if err != nil {
		t.Fatal(err)
	}
	want := `### Wuko: ` + "`check`" + `

| Status | Duration | Steps | Succeeded | Failed | Skipped |
| --- | ---: | ---: | ---: | ---: | ---: |
| failed | 9s | 5 | 1 | 2 | 1 |

#### Steps

_Rows list the top-level steps the run recorded. The header counts every planned step, and the execution statistics below include the steps nested inside controls._

| Step | Status | Duration | Attempts | Retries | Polls |
| --- | --- | ---: | ---: | ---: | ---: |
| build\|app | ✓ succeeded | 3s | 1 | 0 | — |
| optional | ⊘ skipped | 0s | — | — | — |
| integration-test | ✗ failed | 2s | 3 | 2 | 4 |
| wait-for-api | ⏱ timed out | 2.5s | 2 | 1 | — |
| cleanup | ■ canceled | 10ms | — | — | — |

#### Failure details

##### <code>integration-test</code>

- Status: ✗ failed
- Duration: 2s
- Attempts: 3
- Retries: 2
- Source: <code>.wuko/workflows/check.yaml:7</code>

<pre>bad &lt;value&gt;
second &amp; line</pre>

##### <code>wait-for-api</code>

- Status: ⏱ timed out
- Duration: 2.5s
- Attempts: 2
- Retries: 1

<pre>deadline &gt; exceeded</pre>

##### <code>cleanup</code>

- Status: ■ canceled
- Duration: 10ms
- Attempts: —
- Retries: —

#### Execution statistics

- Attempts: 6
- Retries: 3
- Retry wait: 1.5s
- Polls: 4
- Poll wait: 2s
- Timeouts: 1
- Longest step: <code>build|app</code> (3s)

`
	if string(data) != want {
		t.Fatalf("summary:\n%s\nwant:\n%s", data, want)
	}
}

func TestReporterWithholdsStepErrorsWithoutMatchingTerminalProgress(t *testing.T) {
	reporter, _, summary, _ := newTestReporter(t, t.TempDir())
	outcome := reporterpkg.Outcome{
		WorkflowName: "check", Status: engine.StatusFailed, Err: errors.New("failed"),
		Stats: engine.RunStats{
			Duration: time.Second, Total: 1, Failed: 1,
			Steps: []engine.StepStats{{
				ID: "deploy", Status: engine.StatusFailed, Duration: time.Second,
				Error: errors.New("token sk-live-secret rejected"),
			}},
		},
	}
	if err := reporter.Finish(t.Context(), outcome); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(summary)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "sk-live-secret") {
		t.Fatalf("summary = %q, want no unredacted step error", data)
	}
	if !strings.Contains(string(data), "- Error: withheld, no redacted progress copy for this run") {
		t.Fatalf("summary = %q, want withheld error note", data)
	}
}

func TestReporterIgnoresUnrelatedTerminalProgress(t *testing.T) {
	tests := []struct {
		name    string
		event   engine.ProgressEvent
		outcome reporterpkg.Outcome
	}{
		{
			name: "run", event: engine.ProgressEvent{RunID: "other", WorkflowName: "check", Status: engine.StatusSucceeded},
			outcome: reporterpkg.Outcome{RunID: "run", WorkflowName: "check", Status: engine.StatusSucceeded},
		},
		{
			name: "workflow", event: engine.ProgressEvent{RunID: "run", WorkflowName: "dependency", Status: engine.StatusSucceeded},
			outcome: reporterpkg.Outcome{RunID: "run", WorkflowName: "check", Status: engine.StatusSucceeded},
		},
		{
			name: "status", event: engine.ProgressEvent{RunID: "run", WorkflowName: "check", Status: engine.StatusSucceeded},
			outcome: reporterpkg.Outcome{RunID: "run", WorkflowName: "check", Status: engine.StatusFailed, Err: errors.New("schedule failed")},
		},
		{
			name: "nested", event: engine.ProgressEvent{RunID: "run", WorkflowName: "check", Status: engine.StatusSucceeded, Depth: 1},
			outcome: reporterpkg.Outcome{RunID: "run", WorkflowName: "check", Status: engine.StatusSucceeded},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			reporter, _, summary, _ := newTestReporter(t, t.TempDir())
			test.event.Kind = engine.WorkflowFinished
			test.event.Stats = engine.RunStats{Total: 9, Succeeded: 9}
			test.outcome.Stats = engine.RunStats{Total: 2, Succeeded: 2}
			reporter.Progress(test.event)
			if err := reporter.Finish(t.Context(), test.outcome); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(summary)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(data), "| 2 | 2 | 0 | 0 |") || strings.Contains(string(data), "| 9 | 9 | 0 | 0 |") {
				t.Fatalf("summary = %q, want outcome statistics", data)
			}
		})
	}
}

func TestReporterExportsNamedAndAggregateOutputs(t *testing.T) {
	root := t.TempDir()
	reporter, output, _, _ := newTestReporter(t, root)
	outcome := reporterpkg.Outcome{
		InvocationID: "invocation", RunID: "run", WorkflowName: "check",
		Status: engine.StatusSucceeded, Duration: 2500 * time.Millisecond, Outputs: map[string]any{
			"artifact": "first\nWUKO_EOF\nlast",
			"count":    3,
			"metadata": map[string]any{"ok": true},
		}}
	if err := reporter.Finish(t.Context(), outcome); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{
		"artifact<<WUKO_EOF_\nfirst\nWUKO_EOF\nlast\nWUKO_EOF_\n",
		"count<<WUKO_EOF\n3\nWUKO_EOF\n",
		"metadata<<WUKO_EOF\n{\"ok\":true}\nWUKO_EOF\n",
		"wuko_outputs<<WUKO_EOF\n{\"artifact\":\"first\\nWUKO_EOF\\nlast\",\"count\":3,\"metadata\":{\"ok\":true}}\nWUKO_EOF\n",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("GITHUB_OUTPUT = %q, want %q", text, want)
		}
	}
	values := readGitHubOutputs(t, output)
	if values[statusOutput] != "succeeded" || values[executionIDOutput] != "invocation" || values[durationMSOutput] != "2500" || values[failedStepOutput] != "" {
		t.Fatalf("metadata outputs = %#v", values)
	}
	var executionReport reporterpkg.ExecutionReport
	if err := json.Unmarshal([]byte(values[reportOutput]), &executionReport); err != nil {
		t.Fatal(err)
	}
	if executionReport.RunID != "run" || executionReport.Outputs == nil || (*executionReport.Outputs)["count"] != float64(3) {
		t.Fatalf("execution report = %#v", executionReport)
	}
}

func TestReporterRejectsReservedOutputsAfterWritingMetadata(t *testing.T) {
	for _, reserved := range reservedOutputs {
		t.Run(reserved, func(t *testing.T) {
			reporter, output, _, _ := newTestReporter(t, t.TempDir())
			err := reporter.Finish(t.Context(), reporterpkg.Outcome{
				InvocationID: "invocation", WorkflowName: "check", Status: engine.StatusSucceeded,
				Outputs: map[string]any{"artifact": "dist/app.tar.gz", reserved: "workflow value"},
			})
			if err == nil || !strings.Contains(err.Error(), fmt.Sprintf(`workflow output %q is reserved`, reserved)) {
				t.Fatalf("Finish() error = %v, want reserved output error", err)
			}
			values := readGitHubOutputs(t, output)
			if values[statusOutput] != "succeeded" || values[executionIDOutput] != "invocation" || values[reportOutput] == "" {
				t.Fatalf("metadata outputs = %#v", values)
			}
			if _, exists := values["artifact"]; exists {
				t.Fatalf("outputs = %#v, reserved output error must omit all workflow outputs", values)
			}
			if _, exists := values[aggregateOutput]; exists {
				t.Fatalf("outputs = %#v, reserved output error must omit aggregate workflow output", values)
			}
		})
	}
}

func TestWriteEnvironmentValueAvoidsCRLFDelimiterCollision(t *testing.T) {
	var output bytes.Buffer
	if err := writeEnvironmentValue(&output, "artifact", "first\r\nWUKO_EOF\r\nlast"); err != nil {
		t.Fatal(err)
	}
	want := "artifact<<WUKO_EOF_\nfirst\r\nWUKO_EOF\r\nlast\nWUKO_EOF_\n"
	if output.String() != want {
		t.Fatalf("output = %q, want %q", output.String(), want)
	}
}

func TestReporterOmitsOutsideWorkspaceLocation(t *testing.T) {
	root := t.TempDir()
	reporter, _, summary, commands := newTestReporter(t, root)
	reporter.Diagnostic(diagnostic.Event{
		Phase: diagnostic.PhaseLoad, Status: diagnostic.StatusFailed,
		Location: diagnostic.Location{Source: filepath.Join(filepath.Dir(root), "outside.yaml"), Line: 2},
		Error:    errors.New("broken"),
	})
	if err := reporter.Finish(t.Context(), reporterpkg.Outcome{Status: engine.StatusFailed, Err: errors.New("broken")}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(commands.String(), "file=") {
		t.Fatalf("commands = %q, outside path must not be annotated as a file", commands.String())
	}
	summaryData, err := os.ReadFile(summary)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(summaryData), "#### Steps") || strings.Contains(string(summaryData), "#### Execution statistics") {
		t.Fatalf("summary = %q, pre-execution failure must omit step details", summaryData)
	}
}

func TestReporterWithholdsOutputsFromAnUnsuccessfulOutcomeWithoutError(t *testing.T) {
	for _, status := range []engine.ExecutionStatus{engine.StatusCanceled, engine.StatusTimedOut, engine.StatusFailed} {
		t.Run(string(status), func(t *testing.T) {
			reporter, output, _, _ := newTestReporter(t, t.TempDir())
			if err := reporter.Finish(t.Context(), reporterpkg.Outcome{
				InvocationID: "invocation", WorkflowName: "check", Status: status,
				Outputs: map[string]any{"artifact": "dist/app.tar.gz"},
			}); err != nil {
				t.Fatal(err)
			}
			values := readGitHubOutputs(t, output)
			if values[statusOutput] != string(status) {
				t.Fatalf("metadata outputs = %#v", values)
			}
			if _, exists := values[aggregateOutput]; exists {
				t.Fatalf("outputs = %#v, %q must not export workflow outputs", values, status)
			}
			if _, exists := values["artifact"]; exists {
				t.Fatalf("outputs = %#v, %q must not export workflow outputs", values, status)
			}
		})
	}
}

func TestReporterDryRunWritesSummaryWithoutOutputs(t *testing.T) {
	reporter, output, summary, _ := newTestReporter(t, t.TempDir())
	outcome := reporterpkg.Outcome{
		WorkflowName: "check", Status: engine.StatusSucceeded,
		Outputs: map[string]any{"result": "hidden"}, DryRun: true,
	}
	if err := reporter.Finish(t.Context(), outcome); err != nil {
		t.Fatal(err)
	}
	values := readGitHubOutputs(t, output)
	if values[statusOutput] != "succeeded" || values[reportOutput] == "" {
		t.Fatalf("metadata outputs = %#v", values)
	}
	if _, exists := values[aggregateOutput]; exists {
		t.Fatalf("outputs = %#v, dry run must not export workflow outputs", values)
	}
	var executionReport reporterpkg.ExecutionReport
	if err := json.Unmarshal([]byte(values[reportOutput]), &executionReport); err != nil {
		t.Fatal(err)
	}
	if !executionReport.DryRun || executionReport.Outputs != nil {
		t.Fatalf("execution report = %#v, want dry run without outputs", executionReport)
	}
	summaryData, err := os.ReadFile(summary)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(summaryData), "| validated |") {
		t.Fatalf("summary = %q, want validated status", summaryData)
	}
	if strings.Contains(string(summaryData), "#### Steps") || strings.Contains(string(summaryData), "#### Execution statistics") {
		t.Fatalf("summary = %q, dry run must omit step details", summaryData)
	}
}

func newTestReporter(t *testing.T, workspace string) (*Reporter, string, string, *bytes.Buffer) {
	t.Helper()
	output := filepath.Join(t.TempDir(), "output")
	summary := filepath.Join(t.TempDir(), "summary")
	for _, path := range []string{output, summary} {
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	commands := new(bytes.Buffer)
	reporter, err := New(Options{OutputPath: output, SummaryPath: summary, Workspace: workspace, Commands: commands})
	if err != nil {
		t.Fatal(err)
	}
	return reporter, output, summary, commands
}

func readGitHubOutputs(t *testing.T, path string) map[string]string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(data), "\n")
	values := make(map[string]string)
	for index := 0; index < len(lines); {
		name, delimiter, found := strings.Cut(lines[index], "<<")
		if !found {
			if lines[index] != "" {
				t.Fatalf("invalid GITHUB_OUTPUT line %q", lines[index])
			}
			index++
			continue
		}
		index++
		start := index
		for index < len(lines) && lines[index] != delimiter {
			index++
		}
		if index == len(lines) {
			t.Fatalf("unterminated GITHUB_OUTPUT value %q", name)
		}
		values[name] = strings.Join(lines[start:index], "\n")
		index++
	}
	return values
}
