package cmd

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/up2jj/wuko/process"
	"github.com/up2jj/wuko/step"
	runworkflowstep "github.com/up2jj/wuko/steps/runworkflow"
	"github.com/up2jj/wuko/steps/shell"
	"github.com/up2jj/wuko/workflow"
)

type recordingElevatedExecutor struct{ runs int }

func (executor *recordingElevatedExecutor) Run(context.Context, process.Options) (process.Result, error) {
	executor.runs++
	return process.Result{}, nil
}

// A workflow installed from a remote locator or a marketplace is not local code, so running it
// by name, or calling it through run_workflow, must not grant it sudo.
func TestInstalledWorkflowElevationFollowsProvenanceAtRunTime(t *testing.T) {
	t.Parallel()
	const elevated = "version: 1\nname: %s\nsteps:\n  - id: root\n    type: shell\n    with: {command: id, elevated: true}\n"
	const caller = "version: 1\nname: caller\nsteps:\n  - id: call\n    type: run_workflow\n    with: {workflow: %s}\n"
	tests := []struct {
		name      string
		marker    string
		packaged  bool
		viaCaller bool
		wantRun   bool
	}{
		{name: "authored in place", wantRun: true},
		{name: "installed from a local path", marker: `{"version":1,"name":"subject","source":"./subject.yaml","remote":false}`, wantRun: true},
		{name: "installed from a remote locator", marker: `{"version":1,"name":"subject","source":"https://example.com/subject.yaml","remote":true}`},
		{name: "remote install called through run_workflow", marker: `{"version":1,"name":"subject","source":"https://example.com/subject.yaml","remote":true}`, viaCaller: true},
		{name: "marketplace package", packaged: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			workflows := filepath.Join(root, ".wuko", "workflows")
			if err := os.MkdirAll(workflows, 0o755); err != nil {
				t.Fatal(err)
			}
			subject := filepath.Join(workflows, "subject.yaml")
			if test.packaged {
				packageDir := filepath.Join(workflows, "subject")
				if err := os.MkdirAll(packageDir, 0o755); err != nil {
					t.Fatal(err)
				}
				subject = filepath.Join(packageDir, "wuko.yaml")
				writeTestFile(t, filepath.Join(packageDir, workflow.WorkflowPackageMarkerName), `{"marketplace":"https://example.com/marketplace"}`)
			}
			writeTestFile(t, subject, strings.ReplaceAll(elevated, "%s", "subject"))
			if test.marker != "" {
				writeTestFile(t, workflowInstallMarkerPath(workflows, "subject"), test.marker)
			}
			name := "subject"
			if test.viaCaller {
				writeTestFile(t, filepath.Join(workflows, "caller.yaml"), strings.ReplaceAll(caller, "%s", "subject"))
				name = "caller"
			}
			registry := step.NewRegistry()
			for _, register := range []func(*step.Registry) error{shell.Register, runworkflowstep.Register} {
				if err := register(registry); err != nil {
					t.Fatal(err)
				}
			}
			executor := &recordingElevatedExecutor{}
			command := newRootCmd(dependencies{
				stdin: bytes.NewReader(nil), stdout: io.Discard, stderr: io.Discard,
				cwd: func() (string, error) { return root, nil }, homeDir: func() (string, error) { return t.TempDir(), nil },
				configDir: func() (string, error) { return t.TempDir(), nil }, registry: registry,
				elevatedExecutor: executor,
			})
			command.SetArgs([]string{"run", name, "--once"})
			err := command.ExecuteContext(t.Context())
			if test.wantRun {
				if err != nil || executor.runs != 1 {
					t.Fatalf("error = %v, elevated runs = %d, want one elevated run", err, executor.runs)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), "trusted local") || executor.runs != 0 {
				t.Fatalf("error = %v, elevated runs = %d, want elevation refused", err, executor.runs)
			}
		})
	}
}

func writeTestFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}
