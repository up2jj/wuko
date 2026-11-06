package cmd

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/up2jj/wuko/engine"
	"github.com/up2jj/wuko/internal/testfs"
	"github.com/up2jj/wuko/step"
	assertstep "github.com/up2jj/wuko/steps/assert"
	checkstep "github.com/up2jj/wuko/steps/check"
)

func TestChecksCLIReportsFindingsAndContinues(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, severity string
		recheck        bool
	}{
		{"failure", "fail", false}, {"warning", "warn", false}, {"recheck", "fail", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			source := `version: 1
name: doctor
steps:
  - id: probes
    type: check
    with:
      checks:
        - {id: tool, expr: 'false', message: Tool unavailable, fix: 'install tool', severity: ` + test.severity + `}
        - {id: host, expr: 'true', message: Host unavailable}
  - {id: continued, type: print, with: {message: Continued after checks}}
`
			if test.recheck {
				source += `  - id: recheck
    type: check
    with:
      checks:
        - {id: tool, expr: 'true', message: Tool unavailable}
`
			}
			stdout, stderr, err := runOutputWorkflow(t, source, "--reporter", "plain", "--report-json", "-")
			failed := test.severity == "fail" && !test.recheck
			if (err != nil) != failed {
				t.Fatalf("error = %v, stderr = %s", err, stderr)
			}
			report := decodeStreamExecutionReport(t, []byte(stdout))
			if len(report.Checks) != 2 || !strings.Contains(stderr, "Continued after checks") {
				t.Fatalf("report = %#v, stderr = %s", report, stderr)
			}
			if failed && (report.Status != engine.StatusFailed || report.FailedStep != "" || report.Stats.Steps.Failed != 0) {
				t.Fatalf("report = %#v", report)
			}
			if test.recheck {
				if strings.Contains(stderr, "Failed checks:") || strings.Contains(stderr, "Fix:") || report.Checks[0].Status != "ok" {
					t.Fatalf("report = %#v, stderr = %s", report, stderr)
				}
			} else if strings.Count(stderr, "install tool") != 1 {
				t.Fatalf("stderr = %s", stderr)
			}
		})
	}
}

func TestChecksCLIActionContributesOneReport(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	testfs.Write(t, filepath.Join(root, "action", "action.yaml"), `version: 1
name: checks
steps:
  - id: inside
    type: check
    with:
      checks:
        - {id: action_tool, expr: 'false', message: Missing action tool, fix: 'install action-tool'}
`)
	testfs.Write(t, filepath.Join(root, "workflow.yaml"), `version: 1
name: doctor
steps:
  - {id: action, uses: ./action}
  - {id: continued, type: print, with: {message: Parent continued}}
`)
	var stdout, stderr bytes.Buffer
	registry := step.NewRegistry()
	if err := registerBuiltinSteps(registry); err != nil {
		t.Fatal(err)
	}
	command := newRootCmd(dependencies{stdin: bytes.NewReader(nil), stdout: &stdout, stderr: &stderr, registry: registry, cwd: func() (string, error) { return root, nil }, homeDir: func() (string, error) { return root, nil }, configDir: func() (string, error) { return root, nil }, getenv: func(string) string { return "" }})
	command.SetArgs([]string{"--env-loader", "none", "run", "--file", filepath.Join(root, "workflow.yaml"), "--once", "--reporter", "plain", "--report-json", "-"})
	err := command.ExecuteContext(t.Context())
	if err == nil || strings.Count(stderr.String(), "Failed checks:") != 1 || !strings.Contains(stderr.String(), "Parent continued") {
		t.Fatalf("error = %v, stderr = %s", err, stderr.String())
	}
	report := decodeStreamExecutionReport(t, stdout.Bytes())
	if len(report.Checks) != 1 || report.Checks[0].ID != "action_tool" {
		t.Fatalf("report = %#v", report)
	}
}

func TestChecksChildWorkflowHasIndependentFindingsAndStatus(t *testing.T) {
	t.Parallel()
	for _, failed := range []bool{false, true} {
		t.Run(map[bool]string{false: "warning", true: "failure"}[failed], func(t *testing.T) {
			t.Parallel()
			root, directory := runWorkflowFixture(t)
			severity := "warn"
			if failed {
				severity = "fail"
			}
			testfs.Write(t, filepath.Join(directory, "child.yaml"), `version: 1
name: child
steps:
  - {id: empty, type: assert, with: {expr: 'len(checks) == 0', message: Inherited parent findings}}
  - id: child_check
    type: check
    with:
      checks:
        - {id: child_only, expr: 'false', message: Child missing, severity: `+severity+`}
`)
			testfs.Write(t, filepath.Join(directory, "parent.yaml"), `version: 1
name: parent
steps:
  - id: parent_check
    type: check
    with:
      checks:
        - {id: parent_only, expr: 'false', message: Parent optional, severity: warn}
  - {id: call, type: run_workflow, with: {workflow: child}}
  - {id: independent, type: assert, with: {expr: 'len(checks) == 1 && checks[0].id == "parent_only"', message: Child findings escaped}}
`)
			command := runWorkflowCommand(t, root, nil, func(registry *step.Registry) {
				for _, register := range []func(*step.Registry) error{checkstep.Register, assertstep.Register} {
					if err := register(registry); err != nil {
						t.Fatal(err)
					}
				}
			})
			command.SetArgs([]string{"run", "parent", "--once"})
			err := command.ExecuteContext(t.Context())
			if (err != nil) != failed || failed && !strings.Contains(err.Error(), "unresolved check") {
				t.Fatalf("error = %v", err)
			}
		})
	}
}
