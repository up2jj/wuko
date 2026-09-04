package cmd

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/steps/shell"
)

func TestRunCommandComposesPlainAndGitHubReporters(t *testing.T) {
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

func TestRunCommandDiscardsEarlierAttemptStateWhenSchedulingFails(t *testing.T) {
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
