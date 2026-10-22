package cmd

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestWorkflowFileCompletionUsesSelectedFile(t *testing.T) {
	root := t.TempDir()
	data := `version: 1
name: draft
description: Deploy
plugins:
  unavailable: {command: /does/not/exist}
targets:
  staging:
    description: Stage release
    steps: [{uses: ./missing-action}]
  production:
    steps: [{uses: ./missing-action}]
`
	if err := os.WriteFile(filepath.Join(root, "draft.yaml"), []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	broken := filepath.Join(root, ".wuko", "workflows", "broken.yaml")
	if err := os.MkdirAll(filepath.Dir(broken), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(broken, []byte("version: ["), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"run", "tree", "ui", "validate"} {
		t.Run(name, func(t *testing.T) {
			output := executeCompletion(t, root, []string{name, "--file", "draft.yaml", ""})
			want := "production\tDeploy\nstaging\tStage release\n:4\n"
			if output != want {
				t.Fatalf("completion = %q, want %q", output, want)
			}
		})
	}
}

func TestWorkflowCompletionStopsAfterTargetAndDoesNotReadStdin(t *testing.T) {
	for _, args := range [][]string{
		{"run", "--file", "-", ""},
		{"run", "--file", "missing.yaml", "staging", ""},
		{"run", "deploy", "staging", ""},
		{"run", "https://example.com/workflow.yaml", ""},
		{"run", "--pick", ""},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			var output bytes.Buffer
			input := strings.NewReader("do not consume")
			deps := listTestDependencies(t.TempDir(), &output)
			deps.stdin = input
			command := newRootCmd(deps)
			command.SetArgs(append([]string{"__complete"}, args...))
			if err := command.ExecuteContext(t.Context()); err != nil {
				t.Fatal(err)
			}
			if output.String() != ":4\n" || input.Len() != len("do not consume") {
				t.Fatalf("completion = %q, unread bytes = %d", output.String(), input.Len())
			}
		})
	}
}

func TestWorkflowFileCompletionHonorsInvocationPolicyAndErrors(t *testing.T) {
	root := t.TempDir()
	for filename, data := range map[string]string{
		"dependency.yaml": "invokable: false\ntargets: {staging: {description: Stage}}\n",
		"legacy.yaml":     "name: legacy\n",
		"invalid.yaml":    "targets: [",
	} {
		if err := os.WriteFile(filepath.Join(root, filename), []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, test := range []struct {
		command, file, want string
	}{
		{"run", "dependency.yaml", ":4\n"},
		{"tree", "dependency.yaml", "staging\tStage\n:4\n"},
		{"run", "legacy.yaml", ":4\n"},
		{"run", "missing.yaml", ":1\n"},
		{"run", "invalid.yaml", ":1\n"},
	} {
		t.Run(test.command+"/"+test.file, func(t *testing.T) {
			if got := executeCompletion(t, root, []string{test.command, "--file", test.file, ""}); got != test.want {
				t.Fatalf("completion = %q, want %q", got, test.want)
			}
		})
	}
}

func TestWorkflowFlagCompletionFiltersFileExtensions(t *testing.T) {
	for _, test := range []struct {
		command, flag string
		extensions    []string
	}{
		{"run", "--file", []string{"yaml", "yml"}},
		{"tree", "--file", []string{"yaml", "yml"}},
		{"ui", "--file", []string{"yaml", "yml"}},
		{"validate", "--file", []string{"yaml", "yml"}},
		{"run", "--var-file", []string{"json", "toml"}},
		{"validate", "--var-file", []string{"json", "toml"}},
		{"install", "--var-file", []string{"json", "toml"}},
	} {
		t.Run(test.command+test.flag, func(t *testing.T) {
			got := executeCompletion(t, t.TempDir(), []string{test.command, test.flag, ""})
			want := strings.Join(test.extensions, "\n") + fmt.Sprintf("\n:%d\n", cobra.ShellCompDirectiveFilterFileExt)
			if got != want {
				t.Fatalf("completion = %q, want %q", got, want)
			}
		})
	}
}

func executeCompletion(t *testing.T, root string, args []string) string {
	t.Helper()
	var output bytes.Buffer
	command := newRootCmd(listTestDependencies(root, &output))
	command.SetArgs(append([]string{"__complete"}, args...))
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	return output.String()
}
