package githubactions

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/up2jj/wuko/diagnostic"
	"github.com/up2jj/wuko/engine"
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
	if err := reporter.Finish("check", nil, errors.New("bad value"), false); err != nil {
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
	if data, err := os.ReadFile(output); err != nil || len(data) != 0 {
		t.Fatalf("output = %q, err = %v; failed run must not export outputs", data, err)
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

func TestReporterExportsNamedAndAggregateOutputs(t *testing.T) {
	root := t.TempDir()
	reporter, output, _, _ := newTestReporter(t, root)
	state := &engine.State{Outputs: map[string]any{
		"artifact": "first\nWUKO_EOF\nlast",
		"count":    3,
		"metadata": map[string]any{"ok": true},
	}}
	if err := reporter.Finish("check", state, nil, false); err != nil {
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
}

func TestReporterRejectsReservedAggregateOutputWithoutWriting(t *testing.T) {
	reporter, output, _, _ := newTestReporter(t, t.TempDir())
	err := reporter.Finish("check", &engine.State{Outputs: map[string]any{
		"artifact":     "dist/app.tar.gz",
		"wuko_outputs": "workflow value",
	}}, nil, false)
	if err == nil || !strings.Contains(err.Error(), `workflow output "wuko_outputs" is reserved`) {
		t.Fatalf("Finish() error = %v, want reserved output error", err)
	}
	data, readErr := os.ReadFile(output)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(data) != 0 {
		t.Fatalf("GITHUB_OUTPUT = %q, reserved output error must not write partial data", data)
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
	reporter, _, _, commands := newTestReporter(t, root)
	reporter.Diagnostic(diagnostic.Event{
		Phase: diagnostic.PhaseLoad, Status: diagnostic.StatusFailed,
		Location: diagnostic.Location{Source: filepath.Join(filepath.Dir(root), "outside.yaml"), Line: 2},
		Error:    errors.New("broken"),
	})
	if err := reporter.Finish("", nil, errors.New("broken"), false); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(commands.String(), "file=") {
		t.Fatalf("commands = %q, outside path must not be annotated as a file", commands.String())
	}
}

func TestReporterDryRunWritesSummaryWithoutOutputs(t *testing.T) {
	reporter, output, summary, _ := newTestReporter(t, t.TempDir())
	state := &engine.State{Outputs: map[string]any{"result": "hidden"}}
	if err := reporter.Finish("check", state, nil, true); err != nil {
		t.Fatal(err)
	}
	outputData, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if len(outputData) != 0 {
		t.Fatalf("GITHUB_OUTPUT = %q, dry run must not export outputs", outputData)
	}
	summaryData, err := os.ReadFile(summary)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(summaryData), "| validated |") {
		t.Fatalf("summary = %q, want validated status", summaryData)
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
