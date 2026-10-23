package cmd

import (
	"bytes"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/up2jj/wuko/internal/testfs"
)

func TestWorkflowFileCompletionUsesSelectedFile(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	data := `version: 1
name: draft
description: Deploy
form: {title: Deploy}
plugins:
  unavailable: {command: /does/not/exist}
targets:
  staging:
    description: Stage release
    steps: [{uses: ./missing-action}]
  production:
    steps: [{uses: ./missing-action}]
`
	testfs.WriteFile(t, filepath.Join(root, "draft.yaml"), []byte(data), 0o644)
	broken := filepath.Join(root, ".wuko", "workflows", "broken.yaml")
	testfs.Write(t, broken, "version: [")
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
	t.Parallel()
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
	t.Parallel()
	root := t.TempDir()
	for filename, data := range map[string]string{
		"dependency.yaml": "invokable: false\ntargets: {staging: {description: Stage}}\n",
		"legacy.yaml":     "name: legacy\n",
		"invalid.yaml":    "targets: [",
	} {
		testfs.WriteFile(t, filepath.Join(root, filename), []byte(data), 0o644)
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
	t.Parallel()
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

func TestWorkflowOverrideCompletionDisablesFilenames(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"run", "tree", "validate", "ui", "install", "uninstall"} {
		for _, flag := range []string{"--var", "--env"} {
			for _, value := range []string{"", "CO", "CODE=", "CODE=123"} {
				t.Run(name+"/"+flag+"/"+value, func(t *testing.T) {
					t.Parallel()
					if got := executeCompletion(t, t.TempDir(), []string{name, flag, value}); got != ":4\n" {
						t.Fatalf("completion = %q, want no candidates and no filename completion", got)
					}
				})
			}
		}
	}
}

func TestValidateFlagCompletionSuggestsSupportedValues(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		flag string
		want string
	}{
		{flag: "--format", want: "text\njson\n:4\n"},
		{flag: "--reporter", want: "plain\ngithub\n:4\n"},
	} {
		t.Run(test.flag, func(t *testing.T) {
			if got := executeCompletion(t, t.TempDir(), []string{"validate", test.flag, ""}); got != test.want {
				t.Fatalf("completion = %q, want %q", got, test.want)
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

func TestNamedWorkflowCompletionIgnoresBrokenDraftsAndUnpreparedSteps(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dir := filepath.Join(root, ".wuko", "workflows")
	testfs.MkdirAll(t, dir, 0o755)
	for name, data := range map[string]string{
		"broken": "version: [",
		"deploy": `name: deploy
description: Deploy
form: {title: Deploy}
plugins:
  unavailable: {command: /does/not/exist}
targets:
  staging:
    description: Stage release
    steps: [{uses: ./missing-action}]
  production:
    steps: [{type: nonexistent_step}]
`,
		"dependency": "invokable: false\ndescription: Internal\n",
	} {
		testfs.WriteFile(t, filepath.Join(dir, name+".yaml"), []byte(data), 0o644)
	}
	for _, name := range []string{"run", "ui", "tree", "validate"} {
		t.Run(name, func(t *testing.T) {
			want := "deploy\tDeploy\n:4\n"
			if name == "tree" || name == "validate" {
				want = "dependency\tInternal\n" + want
			}
			if got := executeCompletion(t, root, []string{name, ""}); got != want {
				t.Fatalf("names = %q, want %q", got, want)
			}
			want = "production\tDeploy\nstaging\tStage release\n:4\n"
			if got := executeCompletion(t, root, []string{name, "deploy", ""}); got != want {
				t.Fatalf("targets = %q, want %q", got, want)
			}
		})
	}
	var output bytes.Buffer
	command := newRootCmd(listTestDependencies(root, &output))
	command.SetArgs([]string{"validate"})
	if err := command.ExecuteContext(t.Context()); err == nil {
		t.Fatal("validation must still reject malformed workflows")
	}
}

func TestUICompletionFiltersEffectiveBrowserForms(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dir := filepath.Join(root, ".wuko", "workflows")
	testfs.MkdirAll(t, dir, 0o755)
	for name, data := range map[string]string{
		"plain":      "description: Plain\nform: null\n",
		"browser":    "description: Browser\nform: {}\nplugins: {missing: {command: /does/not/exist}}\n",
		"dependency": "invokable: false\nform: {}\ntargets: {web: {form: {}}}\n",
		"shared":     "description: Shared\nform: {}\ntargets:\n  inherited: {}\n  fallback: {form: null}\n  override: {form: {title: Override}}\n",
		"mixed":      "description: Mixed\ntargets:\n  cli: {form: null}\n  web: {description: Web, form: {}}\n",
	} {
		testfs.WriteFile(t, filepath.Join(dir, name+".yaml"), []byte(data), 0o644)
	}
	for _, test := range []struct {
		args []string
		want string
	}{
		{[]string{"ui", ""}, "browser\tBrowser\nmixed\tWeb\nshared\tShared\n:4\n"},
		{[]string{"ui", "mixed", ""}, "web\tWeb\n:4\n"},
		{[]string{"ui", "shared", ""}, "fallback\tShared\ninherited\tShared\noverride\tShared\n:4\n"},
		{[]string{"ui", "dependency", ""}, ":4\n"},
		{[]string{"run", "mixed", ""}, "cli\tMixed\nweb\tWeb\n:4\n"},
		{[]string{"tree", "dependency", ""}, "web\n:4\n"},
	} {
		t.Run(strings.Join(test.args, " "), func(t *testing.T) {
			if got := executeCompletion(t, root, test.args); got != test.want {
				t.Fatalf("completion = %q, want %q", got, test.want)
			}
		})
	}
	for _, name := range []string{"mixed", "shared", "dependency"} {
		// File completion must apply the same form inheritance and invocation policy.
		args := []string{"ui", "--file", filepath.Join(dir, name+".yaml"), ""}
		want := executeCompletion(t, root, []string{"ui", name, ""})
		if got := executeCompletion(t, root, args); got != want {
			t.Fatalf("file completion for %s = %q, want %q", name, got, want)
		}
	}
}
