package cmd

import (
	"bytes"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/up2jj/wuko/internal/testfs"
	"github.com/up2jj/wuko/process/processtest"
	"github.com/up2jj/wuko/step"
	runworkflowstep "github.com/up2jj/wuko/steps/runworkflow"
	"github.com/up2jj/wuko/steps/shell"
	"github.com/up2jj/wuko/workflow"
)

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
			testfs.MkdirAll(t, workflows, 0o755)
			subject := filepath.Join(workflows, "subject.yaml")
			if test.packaged {
				packageDir := filepath.Join(workflows, "subject")
				testfs.MkdirAll(t, packageDir, 0o755)
				subject = filepath.Join(packageDir, "wuko.yaml")
				testfs.Write(t, filepath.Join(packageDir, workflow.WorkflowPackageMarkerName), `{"marketplace":"https://example.com/marketplace"}`)
			}
			testfs.Write(t, subject, strings.ReplaceAll(elevated, "%s", "subject"))
			if test.marker != "" {
				testfs.Write(t, workflowInstallMarkerPath(workflows, "subject"), test.marker)
			}
			name := "subject"
			if test.viaCaller {
				testfs.Write(t, filepath.Join(workflows, "caller.yaml"), strings.ReplaceAll(caller, "%s", "subject"))
				name = "caller"
			}
			registry := step.NewRegistry()
			for _, register := range []func(*step.Registry) error{shell.Register, runworkflowstep.Register} {
				if err := register(registry); err != nil {
					t.Fatal(err)
				}
			}
			executor := &processtest.Executor{}
			command := newRootCmd(dependencies{
				stdin: bytes.NewReader(nil), stdout: io.Discard, stderr: io.Discard,
				cwd: func() (string, error) { return root, nil }, homeDir: func() (string, error) { return t.TempDir(), nil },
				configDir: func() (string, error) { return t.TempDir(), nil }, registry: registry,
				elevatedExecutor: executor,
			})
			command.SetArgs([]string{"run", name, "--once"})
			err := command.ExecuteContext(t.Context())
			if test.wantRun {
				if err != nil || len(executor.Calls()) != 1 {
					t.Fatalf("error = %v, elevated runs = %d, want one elevated run", err, len(executor.Calls()))
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), "trusted local") || len(executor.Calls()) != 0 {
				t.Fatalf("error = %v, elevated runs = %d, want elevation refused", err, len(executor.Calls()))
			}
		})
	}
}
