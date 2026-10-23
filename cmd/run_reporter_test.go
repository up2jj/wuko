package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/up2jj/wuko/reporter"
	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/steps/shell"
)

func TestRunCommandComposesPlainAndGitHubReporters(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	workflowPath := filepath.Join(root, "check.yaml")
	writeWorkflowData(t, workflowPath, `version: 1
name: check
cron: "0 0 * * *"
steps:
  - id: build
    type: shell
    with: {script: "printf done"}
  - return:
      outputs:
        artifact: '"dist/app.tar.gz"'
        count: "3"
`)
	outputPath := filepath.Join(root, "github-output")
	summaryPath := filepath.Join(root, "github-summary")
	for _, path := range []string{outputPath, summaryPath} {
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	values := map[string]string{
		"GITHUB_OUTPUT": outputPath, "GITHUB_STEP_SUMMARY": summaryPath, "GITHUB_WORKSPACE": root,
	}
	registry := step.NewRegistry()
	if err := shell.Register(registry); err != nil {
		t.Fatal(err)
	}
	var terminal bytes.Buffer
	command := newRootCmd(dependencies{
		stdin: bytes.NewReader(nil), stdout: &terminal, stderr: &terminal,
		cwd: func() (string, error) { return root, nil }, homeDir: func() (string, error) { return root, nil },
		configDir: func() (string, error) { return root, nil }, registry: registry,
		getenv: func(name string) string { return values[name] },
		waitUntil: func(context.Context, time.Time) error {
			t.Fatal("--once unexpectedly waited for the workflow schedule")
			return nil
		},
	})
	command.SetArgs([]string{"run", "--file", workflowPath, "--once", "--reporter", "plain", "--reporter", "github"})
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(terminal.String(), "Workflow check succeeded") {
		t.Fatalf("terminal = %q, want plain reporter progress", terminal.String())
	}
	githubOutput, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"artifact<<WUKO_EOF\ndist/app.tar.gz\nWUKO_EOF",
		"count<<WUKO_EOF\n3\nWUKO_EOF",
		`wuko_outputs<<WUKO_EOF` + "\n" + `{"artifact":"dist/app.tar.gz","count":3}`,
	} {
		if !strings.Contains(string(githubOutput), want) {
			t.Errorf("GITHUB_OUTPUT = %q, want %q", githubOutput, want)
		}
	}
	summary, err := os.ReadFile(summaryPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(summary), "### Wuko: `check`") || !strings.Contains(string(summary), "| succeeded |") ||
		!strings.Contains(string(summary), "#### Steps") || !strings.Contains(string(summary), "| build | ✓ succeeded |") ||
		!strings.Contains(string(summary), "#### Execution statistics") {
		t.Fatalf("summary = %q", summary)
	}
}

func TestRunCommandDefaultReporterDoesNotReadGitHubFiles(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	workflowPath := filepath.Join(root, "check.yaml")
	writeWorkflowData(t, workflowPath, "version: 1\nname: check\nsteps:\n  - return: {outputs: {ok: 'true'}}\n")
	command := newRootCmd(dependencies{
		stdin: bytes.NewReader(nil), stdout: new(bytes.Buffer), stderr: new(bytes.Buffer),
		cwd: func() (string, error) { return root, nil }, homeDir: func() (string, error) { return root, nil },
		configDir: func() (string, error) { return root, nil }, registry: step.NewRegistry(),
		getenv: func(string) string { return "" },
	})
	command.SetArgs([]string{"run", "--file", workflowPath})
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestRunCommandGitHubReporterRequiresEnvironmentFiles(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	workflowPath := filepath.Join(root, "check.yaml")
	writeWorkflowData(t, workflowPath, "version: 1\nname: check\nsteps:\n  - return: {outputs: {ok: 'true'}}\n")
	command := newRootCmd(dependencies{
		stdin: bytes.NewReader(nil), stdout: new(bytes.Buffer), stderr: new(bytes.Buffer),
		cwd: func() (string, error) { return root, nil }, homeDir: func() (string, error) { return root, nil },
		configDir: func() (string, error) { return root, nil }, registry: step.NewRegistry(),
		getenv: func(string) string { return "" },
	})
	command.SetArgs([]string{"run", "--file", workflowPath, "--reporter", "github"})
	err := command.ExecuteContext(t.Context())
	if err == nil || !strings.Contains(err.Error(), "GITHUB_OUTPUT is required") {
		t.Fatalf("error = %v, want missing GITHUB_OUTPUT", err)
	}
}

func TestRunCommandWritesRelativeExecutionReportOnSuccess(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	workflowPath := filepath.Join(root, "check.yaml")
	writeWorkflowData(t, workflowPath, `version: 1
name: check
steps:
  - return:
      outputs:
        artifact: '"dist/app.tar.gz"'
`)
	reportDirectory := filepath.Join(root, "reports")
	if err := os.Mkdir(reportDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	now := sequenceClock(
		time.Date(2026, time.September, 4, 10, 0, 0, 0, time.UTC),
		time.Date(2026, time.September, 4, 10, 0, 2, 0, time.UTC),
	)
	command := newRootCmd(dependencies{
		stdin: bytes.NewReader(nil), stdout: new(bytes.Buffer), stderr: new(bytes.Buffer),
		cwd: func() (string, error) { return root, nil }, homeDir: func() (string, error) { return root, nil },
		configDir: func() (string, error) { return root, nil }, registry: step.NewRegistry(), now: now,
	})
	command.SetArgs([]string{"run", "--file", workflowPath, "--report-json", filepath.Join("reports", "execution.json")})
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	report := readExecutionReport(t, filepath.Join(reportDirectory, "execution.json"))
	if report.InvocationID == "" || report.RunID == "" || report.Workflow != "check" || report.Status != "succeeded" || report.DurationMS != 2000 {
		t.Fatalf("report = %#v", report)
	}
	if report.Outputs == nil || (*report.Outputs)["artifact"] != "dist/app.tar.gz" {
		t.Fatalf("report outputs = %#v", report.Outputs)
	}
}

func TestRunCommandStreamsExecutionReportAndRoutesWorkflowOutputToStderr(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	workflowPath := filepath.Join(root, "check.yaml")
	writeWorkflowData(t, workflowPath, `version: 1
name: check
steps:
  - id: announce
    type: shell
    with: {script: "printf step-output"}
  - return:
      outputs:
        answer: "42"
`)
	registry := step.NewRegistry()
	if err := shell.Register(registry); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	command := newRootCmd(dependencies{
		stdin: bytes.NewReader(nil), stdout: &stdout, stderr: &stderr,
		cwd: func() (string, error) { return root, nil }, homeDir: func() (string, error) { return root, nil },
		configDir: func() (string, error) { return root, nil }, registry: registry,
	})
	command.SetArgs([]string{"run", "--file", workflowPath, "--report-json", "-"})
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	report := decodeStreamExecutionReport(t, stdout.Bytes())
	if report.Status != "succeeded" || report.Outputs == nil || (*report.Outputs)["answer"] != float64(42) {
		t.Fatalf("report = %#v", report)
	}
	if strings.Contains(stdout.String(), "step-output") || strings.Contains(stdout.String(), "Workflow check") {
		t.Fatalf("stdout contains terminal output: %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "step-output") || !strings.Contains(stderr.String(), "Workflow check succeeded") {
		t.Fatalf("stderr = %q, want step output and progress", stderr.String())
	}
}

func TestRunCommandStreamsReportsForFailurePreflightAndDryRun(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	registry := step.NewRegistry()
	if err := shell.Register(registry); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name       string
		workflow   string
		extraArgs  []string
		wantStatus string
		wantStep   string
		wantDryRun bool
		wantError  bool
		wantEmpty  bool
	}{
		{
			name: "failure", wantStatus: "failed", wantStep: "explode", wantError: true,
			workflow: "version: 1\nname: check\nsteps:\n  - id: explode\n    type: shell\n    with: {script: 'printf failure-output >&2; exit 7'}\n",
		},
		{
			name: "preflight", wantStatus: "failed", wantError: true, extraArgs: []string{"--var", "invalid"},
			workflow: "version: 1\nname: check\nsteps:\n  - return: {outputs: {}}\n",
		},
		{
			name: "dry run", wantStatus: "succeeded", wantDryRun: true, extraArgs: []string{"--dry-run"},
			workflow: "version: 1\nname: check\nsteps:\n  - return: {outputs: {}}\n",
		},
		{
			name: "empty outputs", wantStatus: "succeeded", wantEmpty: true,
			workflow: "version: 1\nname: check\nsteps:\n  - return: {outputs: {}}\n",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			workflowPath := filepath.Join(root, strings.ReplaceAll(test.name, " ", "-")+".yaml")
			writeWorkflowData(t, workflowPath, test.workflow)
			var stdout, stderr bytes.Buffer
			command := newRootCmd(dependencies{
				stdin: bytes.NewReader(nil), stdout: &stdout, stderr: &stderr,
				cwd: func() (string, error) { return root, nil }, homeDir: func() (string, error) { return root, nil },
				configDir: func() (string, error) { return root, nil }, registry: registry,
			})
			args := []string{"run", "--file", workflowPath, "--report-json", "-"}
			args = append(args, test.extraArgs...)
			command.SetArgs(args)
			err := command.ExecuteContext(t.Context())
			if (err != nil) != test.wantError {
				t.Fatalf("Execute() error = %v, wantError %v", err, test.wantError)
			}
			report := decodeStreamExecutionReport(t, stdout.Bytes())
			if string(report.Status) != test.wantStatus || report.FailedStep != test.wantStep || report.DryRun != test.wantDryRun {
				t.Fatalf("report = %#v", report)
			}
			if test.wantDryRun && report.Outputs != nil {
				t.Fatalf("dry-run outputs = %#v, want omitted", report.Outputs)
			}
			if test.wantEmpty && (report.Outputs == nil || len(*report.Outputs) != 0) {
				t.Fatalf("outputs = %#v, want empty object", report.Outputs)
			}
		})
	}
}

func TestRunCommandReportsStreamWriteFailure(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	workflowPath := filepath.Join(root, "check.yaml")
	writeWorkflowData(t, workflowPath, "version: 1\nname: check\nsteps:\n  - return: {outputs: {}}\n")
	command := newRootCmd(dependencies{
		stdin: bytes.NewReader(nil), stdout: failingCommandWriter{}, stderr: io.Discard,
		cwd: func() (string, error) { return root, nil }, homeDir: func() (string, error) { return root, nil },
		configDir: func() (string, error) { return root, nil }, registry: step.NewRegistry(),
	})
	command.SetArgs([]string{"run", "--file", workflowPath, "--report-json", "-"})
	err := command.ExecuteContext(t.Context())
	if err == nil || !strings.Contains(err.Error(), "writing execution report: unavailable") {
		t.Fatalf("Execute() error = %v, want report write failure", err)
	}
}

func TestRunCommandWritesExecutionReportBeforeEngineStarts(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	workflowPath := filepath.Join(root, "check.yaml")
	writeWorkflowData(t, workflowPath, "version: 1\nname: check\nsteps: []\n")
	reportPath := filepath.Join(root, "report.json")
	command := newRootCmd(dependencies{
		stdin: bytes.NewReader(nil), stdout: new(bytes.Buffer), stderr: new(bytes.Buffer),
		cwd: func() (string, error) { return root, nil }, homeDir: func() (string, error) { return root, nil },
		configDir: func() (string, error) { return root, nil }, registry: step.NewRegistry(),
	})
	command.SetArgs([]string{"run", "--file", workflowPath, "--var", "invalid", "--report-json", reportPath})
	err := command.ExecuteContext(t.Context())
	if err == nil || !strings.Contains(err.Error(), "expected key=value") {
		t.Fatalf("run error = %v, want variable parse failure", err)
	}
	report := readExecutionReport(t, reportPath)
	if report.InvocationID == "" || report.RunID != "" || report.Workflow != "" || report.Status != "failed" || report.Outputs != nil {
		t.Fatalf("report = %#v, want pre-engine failure", report)
	}
}

func TestRunCommandReportsFirstFailedStep(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	workflowPath := filepath.Join(root, "check.yaml")
	writeWorkflowData(t, workflowPath, `version: 1
name: check
steps:
  - id: explode
    type: shell
    with: {script: "exit 7"}
`)
	reportPath := filepath.Join(root, "report.json")
	registry := step.NewRegistry()
	if err := shell.Register(registry); err != nil {
		t.Fatal(err)
	}
	command := newRootCmd(dependencies{
		stdin: bytes.NewReader(nil), stdout: new(bytes.Buffer), stderr: new(bytes.Buffer),
		cwd: func() (string, error) { return root, nil }, homeDir: func() (string, error) { return root, nil },
		configDir: func() (string, error) { return root, nil }, registry: registry,
	})
	command.SetArgs([]string{"run", "--file", workflowPath, "--report-json", reportPath})
	if err := command.ExecuteContext(t.Context()); err == nil {
		t.Fatal("run succeeded, want failed workflow")
	}
	report := readExecutionReport(t, reportPath)
	if report.RunID == "" || report.Status != "failed" || report.FailedStep != "explode" || report.Stats.Steps.Failed != 1 {
		t.Fatalf("report = %#v, want failed explode step", report)
	}
}

func TestRunCommandJoinsWorkflowAndReportDeliveryFailures(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	workflowPath := filepath.Join(root, "check.yaml")
	writeWorkflowData(t, workflowPath, `version: 1
name: check
steps:
  - id: explode
    type: shell
    with: {script: "exit 7"}
`)
	reportPath := filepath.Join(root, "report.json")
	if err := os.Mkdir(reportPath, 0o700); err != nil {
		t.Fatal(err)
	}
	registry := step.NewRegistry()
	if err := shell.Register(registry); err != nil {
		t.Fatal(err)
	}
	command := newRootCmd(dependencies{
		stdin: bytes.NewReader(nil), stdout: new(bytes.Buffer), stderr: new(bytes.Buffer),
		cwd: func() (string, error) { return root, nil }, homeDir: func() (string, error) { return root, nil },
		configDir: func() (string, error) { return root, nil }, registry: registry,
	})
	command.SetArgs([]string{"run", "--file", workflowPath, "--report-json", reportPath})
	err := command.ExecuteContext(t.Context())
	if err == nil || !strings.Contains(err.Error(), `step "explode"`) || !strings.Contains(err.Error(), "replacing execution report") {
		t.Fatalf("run error = %v, want workflow and report failures", err)
	}
}

func TestRunCommandDiscardsEarlierAttemptStateWhenSchedulingFails(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	workflowPath := filepath.Join(root, "check.yaml")
	writeWorkflowData(t, workflowPath, `version: 1
name: check
cron: "* * * * * *"
steps:
  - id: echo
    type: shell
    with: {script: "printf done"}
`)
	registry := step.NewRegistry()
	if err := shell.Register(registry); err != nil {
		t.Fatal(err)
	}
	outputPath := filepath.Join(root, "github-output")
	summaryPath := filepath.Join(root, "github-summary")
	for _, path := range []string{outputPath, summaryPath} {
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	values := map[string]string{
		"GITHUB_OUTPUT": outputPath, "GITHUB_STEP_SUMMARY": summaryPath, "GITHUB_WORKSPACE": root,
	}
	waits := 0
	now := time.Date(2026, time.August, 27, 10, 0, 0, 0, time.UTC)
	var terminal bytes.Buffer
	command := newRootCmd(dependencies{
		stdin: bytes.NewReader(nil), stdout: &terminal, stderr: &terminal,
		cwd: func() (string, error) { return root, nil }, homeDir: func() (string, error) { return root, nil },
		configDir: func() (string, error) { return root, nil }, registry: registry,
		getenv: func(name string) string { return values[name] },
		now:    func() time.Time { return now },
		waitUntil: func(_ context.Context, instant time.Time) error {
			waits++
			if waits == 1 {
				now = instant
				return nil
			}
			return errors.New("timer unavailable")
		},
	})
	command.SetArgs([]string{"run", "--file", workflowPath, "--reporter", "github"})
	err := command.ExecuteContext(t.Context())
	if err == nil || !strings.Contains(err.Error(), "timer unavailable") {
		t.Fatalf("run error = %v, want the scheduling failure", err)
	}
	if waits != 2 {
		t.Fatalf("waits = %d, want one occurrence followed by a failed wait", waits)
	}
	summary, err := os.ReadFile(summaryPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(summary), "| failed |") {
		t.Fatalf("summary = %q, want the failed invocation", summary)
	}
	if !strings.Contains(string(summary), "| 0 | 0 | 0 | 0 |") {
		t.Fatalf("summary = %q, want no step counts borrowed from the successful occurrence", summary)
	}
}

func writeWorkflowData(t *testing.T, path, data string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readExecutionReport(t *testing.T, path string) reporter.ExecutionReport {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var report reporter.ExecutionReport
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatalf("decoding execution report %s: %v", path, err)
	}
	return report
}

func decodeStreamExecutionReport(t *testing.T, data []byte) reporter.ExecutionReport {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(data))
	var report reporter.ExecutionReport
	if err := decoder.Decode(&report); err != nil {
		t.Fatalf("decoding execution report %q: %v", data, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		t.Fatalf("execution report has trailing data %q: %v", data, err)
	}
	return report
}

type failingCommandWriter struct{}

func (failingCommandWriter) Write([]byte) (int, error) { return 0, errors.New("unavailable") }

func sequenceClock(instants ...time.Time) func() time.Time {
	index := 0
	return func() time.Time {
		instant := instants[min(index, len(instants)-1)]
		index++
		return instant
	}
}

func TestRunCommandStreamsReportForSetupFailures(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	workflowPath := filepath.Join(root, "check.yaml")
	writeWorkflowData(t, workflowPath, "version: 1\nname: check\nsteps:\n  - return: {outputs: {}}\n")
	tests := []struct {
		name string
		cwd  func() (string, error)
		args []string
		want string
	}{
		{name: "directories", cwd: func() (string, error) { return "", errors.New("no cwd") }, want: "no cwd"},
		{name: "unknown reporter", cwd: func() (string, error) { return root, nil }, args: []string{"--reporter", "bogus"}, want: "bogus"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stdout bytes.Buffer
			command := newRootCmd(dependencies{
				stdin: bytes.NewReader(nil), stdout: &stdout, stderr: io.Discard,
				cwd: test.cwd, homeDir: func() (string, error) { return root, nil },
				configDir: func() (string, error) { return root, nil }, registry: step.NewRegistry(),
			})
			command.SetArgs(append([]string{"run", "--file", workflowPath, "--report-json", "-"}, test.args...))
			err := command.ExecuteContext(t.Context())
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Execute() error = %v, want %q", err, test.want)
			}
			report := decodeStreamExecutionReport(t, stdout.Bytes())
			if report.Status != "failed" || report.InvocationID == "" {
				t.Fatalf("report = %#v, want failed report with invocation ID", report)
			}
		})
	}
}

func TestRunCommandRejectsPickWithStreamedReport(t *testing.T) {
	t.Parallel()
	var stdout bytes.Buffer
	root := t.TempDir()
	command := newRootCmd(dependencies{
		stdin: bytes.NewReader(nil), stdout: &stdout, stderr: io.Discard,
		cwd: func() (string, error) { return root, nil }, homeDir: func() (string, error) { return root, nil },
		configDir: func() (string, error) { return root, nil }, registry: step.NewRegistry(),
	})
	command.SetArgs([]string{"run", "--pick", "github:owner/repo", "--report-json", "-"})
	err := command.ExecuteContext(t.Context())
	if err == nil || !strings.Contains(err.Error(), "--pick cannot be used with --report-json -") {
		t.Fatalf("Execute() error = %v, want --pick rejection", err)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
}
