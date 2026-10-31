package cmd

import (
	"bytes"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/up2jj/wuko/internal/testfs"
	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/useroutput"
)

func runOutputWorkflow(t *testing.T, source string, args ...string) (string, string, error) {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, "workflow.yaml")
	testfs.Write(t, path, source)
	registry := step.NewRegistry()
	if err := registerBuiltinSteps(registry); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	command := newRootCmd(dependencies{stdin: bytes.NewReader(nil), stdout: &stdout, stderr: &stderr, registry: registry,
		cwd: func() (string, error) { return root, nil }, homeDir: func() (string, error) { return root, nil }, configDir: func() (string, error) { return root, nil }, getenv: func(string) string { return "" }})
	command.SetArgs(append([]string{"--env-loader", "none", "run", "--file", path, "--once"}, args...))
	err := command.ExecuteContext(t.Context())
	return stdout.String(), stderr.String(), err
}

func TestWorkflowReporterSelectionAndCLIOverride(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, config string
		args         []string
		progress     bool
	}{
		{"default", "", nil, true},
		{"yaml quiet", "reporter: quiet\n", nil, false},
		{"yaml mapping", "reporter: {type: quiet, hide_skipped: true}\n", nil, false},
		{"cli plain", "reporter: quiet\n", []string{"--reporter", "plain"}, true},
		{"cli quiet", "reporter: plain\n", []string{"--reporter", "quiet"}, false},
		{"auxiliary only", "reporter: quiet\n", []string{"--reporter", "multiplexer"}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			source := "version: 1\nname: output\n" + test.config + "vars: {person: reader}\nsteps:\n  - {id: note, type: print, with: {message: 'Hello {{ .vars.person }}', level: success}}\n  - {id: work, type: shell, with: {script: 'printf process-output'}}\n"
			stdout, stderr, err := runOutputWorkflow(t, source, test.args...)
			if err != nil {
				t.Fatal(err)
			}
			if stdout != "process-output" || strings.Count(stderr, "✓ Hello reader\n") != 1 {
				t.Fatalf("stdout=%q stderr=%q", stdout, stderr)
			}
			if strings.Contains(stderr, "Workflow output succeeded") != test.progress {
				t.Fatalf("progress=%q", stderr)
			}
			if strings.Contains(stderr, "note (print)") {
				t.Fatalf("print progress visible: %q", stderr)
			}
		})
	}
}

func TestQuietInheritanceOverridesAndJSONReport(t *testing.T) {
	t.Parallel()
	source := `version: 1
name: output
reporter: {type: plain, quiet: [lua], hide_skipped: true}
steps:
  - quiet: true
    steps:
      - {id: hidden, type: lua, with: {source: 'wuko.ui.info("inside block")'}}
      - {id: visible, type: lua, quiet: false, with: {source: 'wuko.ui.success("visible message")'}}
  - {id: sibling, type: shell, with: {script: 'true'}}
  - {id: skipped, type: shell, if: false, with: {script: 'false'}}
`
	stdout, stderr, err := runOutputWorkflow(t, source, "--reporter", "plain", "--report-json", "-")
	if err != nil {
		t.Fatal(err)
	}
	report := decodeStreamExecutionReport(t, []byte(stdout))
	if report.Stats.Steps.Total != 4 || report.Stats.Steps.Succeeded != 3 || report.Stats.Steps.Skipped != 1 {
		t.Fatalf("stats=%#v", report.Stats)
	}
	for _, want := range []string{"• inside block", "✓ visible message", "visible (lua)", "sibling (shell)", "1 skipped"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("missing %q in %q", want, stderr)
		}
	}
	for _, hidden := range []string{"hidden (lua)", "skipped skipped"} {
		if strings.Contains(stderr, hidden) {
			t.Errorf("unexpected %q in %q", hidden, stderr)
		}
	}
}

func TestPlainAssertionRunsCleanupAndDoesNotRepeatMessages(t *testing.T) {
	t.Parallel()
	source := `version: 1
name: output
reporter: quiet
steps:
  - id: stop
    type: assert
    with: {expr: 'false', message: 'Complete setup.', plain: true, exit_code: 2}
finally:
  - {id: cleanup, type: print, with: {message: 'Cleanup complete.', level: success}}
`
	stdout, stderr, err := runOutputWorkflow(t, source, "--report-json", "-")
	if err == nil {
		t.Fatal("assert succeeded")
	}
	view := useroutput.Inspect(err)
	if len(view.Failures) != 1 || view.Failures[0].Code != 2 || view.Ordinary != nil {
		t.Fatalf("failure=%#v", view)
	}
	if stderr != "Complete setup.\n✓ Cleanup complete.\n" {
		t.Fatalf("stderr=%q", stderr)
	}
	var final bytes.Buffer
	if !WriteError(&final, err) || final.Len() != 0 {
		t.Fatalf("final output=%q", final.String())
	}
	report := decodeStreamExecutionReport(t, []byte(stdout))
	if report.Status != "failed" || report.Stats.Steps.Failed != 1 || report.Stats.Steps.Succeeded != 1 {
		t.Fatalf("report=%#v", report)
	}
}

func TestReporterSelectionRejectsConflictingOrUnknownNames(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		config string
		args   []string
		want   string
	}{
		{"", []string{"--reporter", "plain", "--reporter", "quiet"}, "mutually exclusive"},
		{"reporter: unknown\n", nil, "unknown reporter"},
		{"reporter: github\n", nil, "GITHUB_OUTPUT"},
	} {
		t.Run(fmt.Sprint(test), func(t *testing.T) {
			t.Parallel()
			_, _, err := runOutputWorkflow(t, "version: 1\nname: output\n"+test.config+"steps: [{id: note, type: print, with: {message: hi}}]\n", test.args...)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestValidateRejectsUnknownWorkflowReporter(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := filepath.Join(root, "workflow.yaml")
	testfs.Write(t, path, "version: 1\nname: output\nreporter: otel\nsteps: [{id: note, type: print, with: {message: hi}}]\n")
	registry := step.NewRegistry()
	if err := registerBuiltinSteps(registry); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	command := newRootCmd(dependencies{stdin: bytes.NewReader(nil), stdout: &stdout, stderr: &stderr, registry: registry,
		cwd: func() (string, error) { return root, nil }, homeDir: func() (string, error) { return root, nil }, configDir: func() (string, error) { return root, nil }, getenv: func(string) string { return "" }})
	command.SetArgs([]string{"--env-loader", "none", "validate", "--file", path})
	if err := command.ExecuteContext(t.Context()); err == nil || !strings.Contains(err.Error(), `unknown reporter "otel"`) {
		t.Fatalf("error=%v stderr=%q", err, stderr.String())
	}
}

func TestPlainAssertionsRemainRetryableAndCatchable(t *testing.T) {
	t.Parallel()
	source := `version: 1
name: output
reporter: quiet
steps:
  - id: recover
    try:
      steps:
        - id: retry
          attempt:
            max_attempts: 2
            initial_delay: 0s
            steps:
              - id: check
                type: assert
                with: {expr: 'false', message: 'Try setup again.', plain: true, exit_code: 7}
    catch:
      steps:
        - {id: recovery, type: print, with: {message: 'Recovered.', level: success}}
`
	stdout, stderr, err := runOutputWorkflow(t, source, "--report-json", "-")
	if err != nil {
		t.Fatal(err)
	}
	if stderr != "Try setup again.\nTry setup again.\n✓ Recovered.\n" {
		t.Fatalf("stderr=%q", stderr)
	}
	if report := decodeStreamExecutionReport(t, []byte(stdout)); report.Status != "succeeded" || report.Stats.Retries != 1 {
		t.Fatalf("report=%#v", report)
	}
}

func TestQuietFailureAndJoinedCleanupErrorRemainVisible(t *testing.T) {
	t.Parallel()
	source := `version: 1
name: output
reporter: quiet
steps:
  - {id: stop, type: assert, with: {expr: 'false', message: 'Fix setup.', plain: true, exit_code: 7}}
finally:
  - {id: cleanup, type: assert, with: {expr: 'false', message: 'Cleanup failed.'}}
`
	_, stderr, err := runOutputWorkflow(t, source)
	if err == nil || !strings.Contains(stderr, "assertion failed: Cleanup failed.") || strings.Count(stderr, "Fix setup.") != 1 {
		t.Fatalf("stderr=%q err=%v", stderr, err)
	}
	var final bytes.Buffer
	if !WriteError(&final, err) || !strings.Contains(final.String(), "Cleanup failed.") || strings.Contains(final.String(), "Fix setup.") {
		t.Fatalf("final=%q", final.String())
	}
}
