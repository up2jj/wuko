package cmd

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/up2jj/wuko/internal/testfs"
	"github.com/up2jj/wuko/shim"
	"github.com/up2jj/wuko/step"
)

func TestInlineShimRunsFromCallerDirectoryWithoutSource(t *testing.T) {
	t.Parallel()
	for _, destination := range []string{"local", "global"} {
		t.Run(destination, func(t *testing.T) {
			t.Parallel()
			project, home, caller := t.TempDir(), t.TempDir(), t.TempDir()
			source := filepath.Join(project, ".wuko", "workflows", "check.yaml")
			content := "version: 1\nname: check\ntargets:\n  ci:\n    steps:\n      - id: inspect\n        type: capture_stdin\n"
			testfs.Write(t, source, content)
			dir := project
			if destination == "global" {
				dir = filepath.Join(home, ".wuko", "bin")
			}
			path := filepath.Join(dir, "check-ci")
			args := []string{"shim", "create", "check", "ci", "--inline", "--output", path, "--", "--no-config"}
			if _, _, err := runShimCommand(t, project, home, "", args...); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			spec, err := shim.Parse(data)
			if err != nil || !spec.Inline || spec.Content != content {
				t.Fatalf("inline spec = %+v, error = %v", spec, err)
			}
			if diff := cmp.Diff([]string{"--no-config"}, spec.Args); diff != "" {
				t.Fatalf("baked flags (-want +got):\n%s", diff)
			}
			// The YAML is a snapshot: changing and then removing the original cannot
			// change what the globally placed or locally created command runs.
			testfs.Write(t, source, "invalid workflow")
			if err := os.Remove(source); err != nil {
				t.Fatal(err)
			}
			var captured step.Request
			registry := step.NewRegistry()
			if err := registry.Register("capture_stdin", func(map[string]any) (step.Runner, error) {
				return stdinCaptureRunner{request: &captured}, nil
			}); err != nil {
				t.Fatal(err)
			}
			input := strings.NewReader("runtime stdin")
			command := newRootCmd(dependencies{
				stdin: input, stdout: io.Discard, stderr: io.Discard,
				cwd:       func() (string, error) { return caller, nil },
				homeDir:   func() (string, error) { return home, nil },
				configDir: func() (string, error) { return filepath.Join(home, "config"), nil },
				registry:  registry,
			})
			command.SetArgs([]string{"--no-config", "run", "--file", path, "ci"})
			if err := command.ExecuteContext(t.Context()); err != nil {
				t.Fatal(err)
			}
			if captured.RunDir != caller || captured.WorkflowDir != dir || captured.Stdin != input {
				t.Fatalf("run dir = %q, workflow dir = %q, stdin = %#v", captured.RunDir, captured.WorkflowDir, captured.Stdin)
			}
			stdout, _, err := runShimCommand(t, caller, home, "", "shim", "list", "--dir", dir)
			if err != nil || !strings.Contains(stdout, "inline ") || strings.Contains(stdout, "missing") {
				t.Fatalf("list = %q, %v", stdout, err)
			}
			if _, _, err := runShimCommand(t, caller, home, "", "shim", "uninstall", "check-ci", "--dir", dir); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestInlineShimSupportsStdoutAndRejectsUnsupportedSelectors(t *testing.T) {
	t.Parallel()
	project, home := t.TempDir(), t.TempDir()
	source := filepath.Join(project, "check.yaml")
	testfs.Write(t, source, shimTestWorkflow)
	stdout, _, err := runShimCommand(t, project, home, "", "shim", "create", "--file", source, "--inline", "-o", "-")
	if err != nil {
		t.Fatal(err)
	}
	spec, err := shim.Parse([]byte(stdout))
	if err != nil || !spec.Inline || spec.Content != shimTestWorkflow {
		t.Fatalf("stdout inline spec = %+v, %v", spec, err)
	}
	for _, selector := range [][]string{
		{"--project", "."}, {"check", "--by-name"}, {"https://example.test/check.yaml", "--as", "remote"},
	} {
		args := append([]string{"shim", "create", "--inline"}, selector...)
		if _, _, err := runShimCommand(t, project, home, "", args...); err == nil || !strings.Contains(err.Error(), "inline") {
			t.Fatalf("%v error = %v, want inline selector error", args, err)
		}
	}
}

func TestShimInstallRejectsInline(t *testing.T) {
	t.Parallel()
	_, _, err := runShimCommand(t, t.TempDir(), t.TempDir(), "", "shim", "install", "check", "--inline")
	if err == nil || !strings.Contains(err.Error(), "unknown flag: --inline") {
		t.Fatalf("install error = %v, want unknown inline flag", err)
	}
}

func TestInlineShimRejectsWorkflowsThatAreNotStandalone(t *testing.T) {
	t.Parallel()
	for _, test := range []struct{ name, content, want string }{
		{name: "invalid", content: "version: 1\nname: broken\nsteps: {not: a list}\n", want: "cannot inline"},
		{name: "required file", content: "version: 1\nname: broken\nsteps:\n  - require: steps.yaml\n", want: "required step files"},
		{name: "template file", content: "version: 1\nname: broken\ntemplates:\n  greeting:\n    file: message.tmpl\nsteps:\n  - id: hello\n    type: print\n    with: {message: hello}\n", want: `file-backed template "greeting"`},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			project, home := t.TempDir(), t.TempDir()
			// The referenced files exist beside the source but would not travel with the shim.
			testfs.Write(t, filepath.Join(project, "steps.yaml"), "steps:\n  - id: hello\n    type: print\n    with: {message: hello}\n")
			testfs.Write(t, filepath.Join(project, "message.tmpl"), "hello")
			testfs.Write(t, filepath.Join(project, "broken.yaml"), test.content)
			_, _, err := runShimCommand(t, project, home, "", "shim", "create", "--file", "broken.yaml", "--inline")
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("create = %v, want %q", err, test.want)
			}
			if _, err := os.Stat(filepath.Join(project, "broken")); !os.IsNotExist(err) {
				t.Fatalf("broken shim was written: %v", err)
			}
		})
	}
}
