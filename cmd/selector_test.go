package cmd

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/up2jj/wuko/internal/testfs"
)

func TestWorkflowCommandsExplainExtraFileSelectors(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, command string
		flags         []string
		streamReport  bool
	}{
		{name: "run", command: "run"},
		{name: "run JSON", command: "run", flags: []string{"--report-json", "-"}, streamReport: true},
		{name: "tree", command: "tree"},
		{name: "validate", command: "validate"},
		{name: "validate JSON", command: "validate", flags: []string{"--format", "json"}},
		{name: "ui", command: "ui"},
	} {
		for _, filename := range []string{"missing.yaml", "-"} {
			t.Run(test.name+"/"+filename, func(t *testing.T) {
				t.Parallel()
				var output bytes.Buffer
				input := strings.NewReader("unread workflow")
				deps := listTestDependencies(t.TempDir(), &output)
				deps.stdin = input
				command := newRootCmd(deps)
				command.SetArgs(append([]string{test.command, "--file", filename, "deploy", "staging"}, test.flags...))
				err := command.ExecuteContext(t.Context())
				want := "with --file, pass at most one positional TARGET; usage: wuko " + test.command + " --file PATH [TARGET]"
				if err == nil || err.Error() != want {
					t.Fatalf("ExecuteContext() error = %v, want %q", err, want)
				}
				if input.Len() != len("unread workflow") {
					t.Fatal("invalid arguments consumed stdin")
				}
				if test.streamReport {
					report := decodeStreamExecutionReport(t, output.Bytes())
					if report.Status != "failed" || report.InvocationID == "" {
						t.Fatalf("report = %#v, want failed report with invocation ID", report)
					}
				} else if output.Len() != 0 {
					t.Fatalf("invalid arguments wrote to stdout: %q", output.String())
				}
			})
		}
	}
}

func TestWorkflowCommandsSuggestListingWhenSelectorMissing(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"run", "tree", "ui"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var output bytes.Buffer
			input := strings.NewReader("unread workflow")
			deps := listTestDependencies(t.TempDir(), &output)
			deps.stdin = input
			command := newRootCmd(deps)
			command.SetArgs([]string{name})
			err := command.ExecuteContext(t.Context())
			want := "workflow name or --file is required; use wuko list to find workflows"
			if err == nil || err.Error() != want {
				t.Fatalf("ExecuteContext() error = %v, want %q", err, want)
			}
			if input.Len() != len("unread workflow") || output.Len() != 0 {
				t.Fatalf("unread bytes = %d, stdout = %q", input.Len(), output.String())
			}
		})
	}
}

func TestWorkflowCommandsAcceptSingleFileTarget(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"run", "tree", "validate", "ui"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			path := filepath.Join(root, "deploy.yaml")
			testfs.Write(t, path, `version: 1
name: deploy
targets:
  staging:
    steps:
      - return: {outputs: {}}
`)
			var output bytes.Buffer
			command := newRootCmd(listTestDependencies(root, &output))
			command.SetArgs([]string{name, "--file", path, "staging"})
			err := command.ExecuteContext(t.Context())
			if name == "ui" {
				// Reaching the form check proves the target was selected, without opening a server.
				if err == nil || err.Error() != `workflow "deploy" does not declare a form` {
					t.Fatalf("ExecuteContext() error = %v, want missing form after target selection", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}
