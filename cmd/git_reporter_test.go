package cmd

import (
	"bytes"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
	"github.com/google/go-cmp/cmp"
	"github.com/spf13/cobra"
	"github.com/up2jj/wuko/engine"
	"github.com/up2jj/wuko/internal/gittest"
	"github.com/up2jj/wuko/internal/testfs"
)

func TestGitHookReporterPrintsCheckOutcomes(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		status engine.ExecutionStatus
		want   string
	}{
		{engine.StatusSucceeded, "Passed"},
		{engine.StatusFailed, "Failed"},
		{engine.StatusSkipped, "Skipped"},
		{engine.StatusTimedOut, "Timed out"},
		{engine.StatusCanceled, "Canceled"},
	} {
		t.Run(string(test.status), func(t *testing.T) {
			t.Parallel()
			var output bytes.Buffer
			command := &cobra.Command{}
			command.SetErr(&output)
			reporter := newGitHookReporter(command, dependencies{}, "/repo", "pre-commit", "checks")
			reporter.Progress(engine.ProgressEvent{Kind: engine.StepFinished, StepID: "vet", Status: test.status})
			line := strings.TrimSuffix(output.String(), "\n")
			if !strings.HasPrefix(line, "checks / vet...") || !strings.HasSuffix(line, test.want) {
				t.Fatalf("output = %q, want a labeled %s result", output.String(), test.want)
			}
			if utf8.RuneCountInString(line) != 72 || strings.Count(output.String(), "\n") != 1 {
				t.Fatalf("output = %q, want one aligned result line", output.String())
			}
		})
	}
}

func TestGitHookReporterLabelsChecksByDeclaredWorkflowName(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	command := &cobra.Command{}
	command.SetErr(&output)
	reporter := newGitHookReporter(command, dependencies{}, "/repo", "pre-commit", "./ci/checks.yaml")
	reporter.Progress(engine.ProgressEvent{Kind: engine.StepFinished, WorkflowName: "checks", StepID: "vet", Status: engine.StatusSucceeded})
	if !strings.HasPrefix(output.String(), "checks / vet...") {
		t.Fatalf("output = %q, want the declared workflow name as the label", output.String())
	}
}

func TestGitHookReporterAlignsWideAndLongLabels(t *testing.T) {
	t.Parallel()
	for name, stepID := range map[string]string{
		"wide":  "检查格式",
		"long":  strings.Repeat("x", 80),
		"mixed": strings.Repeat("检", 40),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var output bytes.Buffer
			command := &cobra.Command{}
			command.SetErr(&output)
			reporter := newGitHookReporter(command, dependencies{}, "/repo", "pre-commit", "checks")
			reporter.Progress(engine.ProgressEvent{Kind: engine.StepFinished, StepID: stepID, Status: engine.StatusSucceeded})
			line := strings.TrimSuffix(output.String(), "\n")
			if ansi.StringWidth(line) != 72 || !strings.HasSuffix(line, "...Passed") {
				t.Fatalf("output = %q (width %d), want a 72-column line ending in a status", line, ansi.StringWidth(line))
			}
		})
	}
}

func TestGitHookReporterSuppressesIntermediateResults(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	command := &cobra.Command{}
	command.SetErr(&output)
	reporter := newGitHookReporter(command, dependencies{}, "/repo", "pre-push", "checks")
	for _, event := range []engine.ProgressEvent{
		{Kind: engine.StepStarted, StepID: "vet", Status: engine.StatusRunning},
		{Kind: engine.AttemptFinished, StepID: "vet", Status: engine.StatusFailed},
		{Kind: engine.RetryScheduled, StepID: "vet", Status: engine.StatusRunning},
		{Kind: engine.PollFinished, StepID: "vet", Status: engine.StatusSucceeded},
		{Kind: engine.StepFinished, StepID: "inner", Depth: 2, Status: engine.StatusSucceeded},
		{Kind: engine.StepFinished, StepID: "inner", Depth: 1, ParentRunID: "parent", Status: engine.StatusFailed},
	} {
		reporter.Progress(event)
	}
	if output.Len() != 0 {
		t.Fatalf("output = %q, want no intermediate results", output.String())
	}
	reporter.Progress(engine.ProgressEvent{Kind: engine.StepFinished, StepID: "vet", Status: engine.StatusSucceeded})
	if strings.Count(output.String(), "\n") != 1 || !strings.HasSuffix(output.String(), "Passed\n") {
		t.Fatalf("output = %q, want only the final result", output.String())
	}
}

func TestGitHookRunReportsChecksForCommitAndPush(t *testing.T) {
	t.Parallel()
	for _, hook := range []string{"pre-commit", "pre-push"} {
		t.Run(hook, func(t *testing.T) {
			t.Parallel()
			root := gittest.Init(t)
			testfs.Write(t, filepath.Join(root, ".wuko", "workflows", "checks.yaml"), `version: 1
name: checks
steps:
  - id: whitespace
    type: shell
    with: {script: "true"}
  - id: tidy
    if: "false"
    type: shell
    with: {script: "exit 9"}
  - id: retry
    attempt:
      max_attempts: 2
      initial_delay: 0s
      steps:
        - id: probe
          type: shell
          with:
            script: "if [ -f retried ]; then exit 0; fi; touch retried; exit 1"
  - id: files
    foreach:
      items: '[1, 2]'
      max_concurrency: 1
      steps:
        - id: inner
          type: shell
          with: {script: "true"}
  - concurrent:
      max_concurrency: 2
      steps:
        - id: lint
          type: shell
          with: {script: "true"}
        - id: test
          type: shell
          with: {script: "true"}
  - id: vet
    type: shell
    with: {script: "printf 'vet detail\n' >&2; exit 7"}
  - id: after
    type: shell
    with: {script: "true"}
`)
			writeGitHookManifest(t, root, hook+":\n    - workflow: checks")
			var output bytes.Buffer
			command := newRootCmd(gitHookTestDependencies(t, root, io.Discard, &output, func(string) string { return "" }))
			args := []string{"git", "hook", "run", hook}
			if hook == "pre-push" {
				args = append(args, "--", "origin", "https://example.com/repo")
			}
			command.SetArgs(args)
			err := command.ExecuteContext(t.Context())
			if err == nil || !strings.Contains(err.Error(), hook+`: workflow "checks" failed at step "vet"`) {
				t.Fatalf("error = %v, want the failed check identified", err)
			}
			lines := strings.Split(strings.TrimSuffix(output.String(), "\n"), "\n")
			got := make(map[string]string)
			for _, line := range lines {
				if line == "vet detail" {
					continue
				}
				label, status, found := strings.Cut(line, ".")
				if !found {
					t.Fatalf("unexpected output line %q", line)
				}
				if _, duplicate := got[label]; duplicate {
					t.Fatalf("duplicate check result %q", line)
				}
				got[label] = strings.TrimLeft(status, ".")
			}
			want := map[string]string{
				"checks / whitespace": "Passed", "checks / tidy": "Skipped", "checks / files": "Passed",
				"checks / retry": "Passed", "checks / lint": "Passed", "checks / test": "Passed", "checks / vet": "Failed",
			}
			if diff := cmp.Diff(want, got); diff != "" {
				t.Fatalf("check results mismatch (-want +got):\n%s", diff)
			}
			if !strings.Contains(output.String(), "vet detail\nchecks / vet.") {
				t.Fatalf("output = %q, want command diagnostics before the failed result", output.String())
			}
		})
	}
}
