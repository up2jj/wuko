package cmd

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/creack/pty"
	"github.com/up2jj/wuko/internal/testfs"
	"github.com/up2jj/wuko/process"
	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/steps/shell"
)

const replacingWorkflow = `version: 1
name: handoff
steps:
  - id: prepare
    type: shell
    with:
      command: "true"
  - id: connect
    type: shell
    with:
      argv:
        expr: '["sh", "-c", "exit 0"]'
      tty: true
      replace: true
`

func TestReplaceAfterReplacesOnlyAfterSuccess(t *testing.T) {
	t.Parallel()
	program := process.Replacement{Path: "/bin/sh", Argv: []string{"sh"}}
	failure := errors.New("run failed")
	tests := []struct {
		name    string
		request bool
		err     error
		want    int
	}{
		{name: "pending", request: true, want: 1},
		{name: "failed run", request: true, err: failure},
		{name: "nothing pending"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			replaced := 0
			err := replaceAfter(func(ctx context.Context) error {
				if test.request {
					if err := processReplacementFrom(ctx).request(program); err != nil {
						return err
					}
				}
				return test.err
			}, func(got process.Replacement) error {
				if got.Path != program.Path {
					t.Errorf("replacement = %#v", got)
				}
				replaced++
				return nil
			})(t.Context())
			if !errors.Is(err, test.err) || replaced != test.want {
				t.Fatalf("error = %v replaced = %d, want error %v replaced %d", err, replaced, test.err, test.want)
			}
		})
	}
}

func replacementCommandDeps(t *testing.T, root string, output *bytes.Buffer) (dependencies, func()) {
	t.Helper()
	registry := step.NewRegistry()
	if err := shell.Register(registry); err != nil {
		t.Fatal(err)
	}
	terminal, tty, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	deps := dependencies{
		stdin: tty, stdout: output, stderr: output,
		cwd: func() (string, error) { return root, nil }, homeDir: func() (string, error) { return filepath.Join(root, "home"), nil },
		configDir: func() (string, error) { return filepath.Join(root, "config"), nil }, registry: registry,
	}
	return deps, func() {
		_ = tty.Close()
		_ = terminal.Close()
	}
}

func TestRunCommandRecordsProcessReplacementWithoutSummary(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	testfs.Write(t, filepath.Join(root, ".wuko", "workflows", "handoff.yaml"), replacingWorkflow)
	var output bytes.Buffer
	deps, closeTerminal := replacementCommandDeps(t, root, &output)
	defer closeTerminal()
	replacement := &processReplacement{}
	command := newRootCmd(deps)
	command.SetArgs([]string{"run", "handoff"})
	if err := command.ExecuteContext(withProcessReplacement(t.Context(), replacement)); err != nil {
		t.Fatalf("run error = %v; output:\n%s", err, output.String())
	}
	program := replacement.take()
	if program == nil {
		t.Fatalf("no replacement recorded; output:\n%s", output.String())
	}
	if got := strings.Join(program.Argv, " "); got != "sh -c exit 0" || program.Dir != root {
		t.Fatalf("replacement = %#v", program)
	}
	if strings.Contains(output.String(), "succeeded in") {
		t.Fatalf("output contains a completion summary:\n%s", output.String())
	}
}

func TestRunCommandRejectsProcessReplacementOutsideSelectedWorkflow(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		files map[string]string
		args  []string
	}{
		{
			name: "dependency",
			files: map[string]string{
				"handoff.yaml": strings.Replace(replacingWorkflow, "name: handoff", "name: handoff\ninvokable: false", 1),
				"release.yaml": "version: 1\nname: release\ndepends_on: {handoff: handoff}\nsteps:\n  - id: done\n    type: shell\n    with:\n      command: \"true\"\n",
			},
			args: []string{"run", "release"},
		},
		{
			name:  "schedule",
			files: map[string]string{"handoff.yaml": strings.Replace(replacingWorkflow, "name: handoff", "name: handoff\ncron: \"* * * * *\"", 1)},
			args:  []string{"run", "handoff"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			testfs.WriteTree(t, filepath.Join(root, ".wuko", "workflows"), test.files)
			var output bytes.Buffer
			deps, closeTerminal := replacementCommandDeps(t, root, &output)
			defer closeTerminal()
			replacement := &processReplacement{}
			command := newRootCmd(deps)
			command.SetArgs(test.args)
			err := command.ExecuteContext(withProcessReplacement(t.Context(), replacement))
			if err == nil || !strings.Contains(err.Error(), "replace is available only to the final step") {
				t.Fatalf("run error = %v; output:\n%s", err, output.String())
			}
			if replacement.requested() {
				t.Fatal("replacement recorded")
			}
		})
	}
}

func TestValidateCommandAcceptsProcessReplacement(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	testfs.Write(t, filepath.Join(root, ".wuko", "workflows", "handoff.yaml"), replacingWorkflow)
	var output bytes.Buffer
	deps, closeTerminal := replacementCommandDeps(t, root, &output)
	defer closeTerminal()
	command := newRootCmd(deps)
	command.SetArgs([]string{"validate", "handoff"})
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatalf("validate error = %v; output:\n%s", err, output.String())
	}
}
