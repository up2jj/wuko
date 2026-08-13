package cmd

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/up2jj/wuko/step"
	keyvaluestep "github.com/up2jj/wuko/steps/key_value"
	"github.com/up2jj/wuko/steps/shell"
)

func TestRunCommandInMemory(t *testing.T) {
	root := t.TempDir()
	workflowDir := filepath.Join(root, ".wuko", "workflows")
	if err := os.MkdirAll(workflowDir, 0o755); err != nil {
		t.Fatal(err)
	}
	data := `version: 1
name: hello
steps:
  - id: hello
    type: shell
    with:
      script: "printf '%s' \"$1\""
      args: ["{{ .vars.name }}"]
`
	if err := os.WriteFile(filepath.Join(workflowDir, "hello.yaml"), []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	registry := step.NewRegistry()
	if err := shell.Register(registry); err != nil {
		t.Fatal(err)
	}
	var output, diagnostics bytes.Buffer
	command := newRootCmd(dependencies{
		stdin: bytes.NewReader(nil), stdout: &output, stderr: &diagnostics,
		cwd:       func() (string, error) { return root, nil },
		homeDir:   func() (string, error) { return filepath.Join(root, "home"), nil },
		configDir: func() (string, error) { return filepath.Join(root, "config"), nil },
		registry:  registry,
	})
	command.SetArgs([]string{"run", "hello", "--var", "name=world"})
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if output.String() != "world" {
		t.Fatalf("stdout = %q", output.String())
	}
	if !strings.Contains(diagnostics.String(), "◆ Workflow hello · 1 step") || !strings.Contains(diagnostics.String(), "✓ Workflow hello succeeded") {
		t.Fatalf("progress = %q", diagnostics.String())
	}
}

func TestRunCommandRoutesLocalAndGlobalValueStores(t *testing.T) {
	root := t.TempDir()
	configDir := filepath.Join(root, "config")
	workflowDir := filepath.Join(root, ".wuko", "workflows")
	if err := os.MkdirAll(workflowDir, 0o755); err != nil {
		t.Fatal(err)
	}
	data := `version: 1
name: values
steps:
  - id: local
    type: key_value
    with: {operation: set, scope: local, store: prefs, key: theme, value: dark}
  - id: global
    type: key_value
    with: {operation: set, scope: global, store: prefs, key: language, value: en}
`
	if err := os.WriteFile(filepath.Join(workflowDir, "values.yaml"), []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	registry := step.NewRegistry()
	if err := keyvaluestep.Register(registry); err != nil {
		t.Fatal(err)
	}
	command := newRootCmd(dependencies{
		stdin: bytes.NewReader(nil), stdout: io.Discard, stderr: io.Discard,
		cwd: func() (string, error) { return root, nil }, homeDir: func() (string, error) { return "", nil },
		configDir: func() (string, error) { return configDir, nil }, registry: registry,
	})
	command.SetArgs([]string{"run", "values"})
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		filepath.Join(workflowDir, ".wuko", "values", "prefs.json"),
		filepath.Join(configDir, "wuko", "values", "prefs.json"),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("store %s: %v", path, err)
		}
	}
}

func TestRunCommandUsesInvocationEnvironment(t *testing.T) {
	root := t.TempDir()
	workflowDir := filepath.Join(root, ".wuko", "workflows")
	if err := os.MkdirAll(workflowDir, 0o755); err != nil {
		t.Fatal(err)
	}
	data := `version: 1
name: environment
env:
  DERIVED: "{{ .env.FROM_DIRENV }}"
  PRIORITY: workflow
steps:
  - id: environment
    type: shell
    with:
      script: "printf '%s:%s' \"$DERIVED\" \"$PRIORITY\""
`
	if err := os.WriteFile(filepath.Join(workflowDir, "environment.yaml"), []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	registry := step.NewRegistry()
	if err := shell.Register(registry); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	command := newRootCmd(dependencies{
		stdin: bytes.NewReader(nil), stdout: &output, stderr: &output,
		cwd: func() (string, error) { return root, nil },
		environment: func(_ context.Context, dir string) (map[string]string, error) {
			if dir != root {
				t.Fatalf("dir = %q, want %q", dir, root)
			}
			return map[string]string{"FROM_DIRENV": "loaded", "PRIORITY": "direnv"}, nil
		},
		homeDir:   func() (string, error) { return filepath.Join(root, "home"), nil },
		configDir: func() (string, error) { return filepath.Join(root, "config"), nil },
		registry:  registry,
	})
	command.SetArgs([]string{"run", "environment", "--env", "PRIORITY=cli"})
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "loaded:cli") {
		t.Fatalf("output = %q", output.String())
	}
}

func TestBareCommandListsAllWorkflowsWithScope(t *testing.T) {
	root := t.TempDir()
	localDir := filepath.Join(root, ".wuko", "workflows")
	globalDir := filepath.Join(root, "home", ".wuko", "workflows")
	if err := os.MkdirAll(localDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(globalDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestWorkflow(t, filepath.Join(localDir, "local.yaml"), "local workflow")
	writeTestWorkflow(t, filepath.Join(globalDir, "global.yaml"), "global workflow")
	var output bytes.Buffer
	command := newRootCmd(dependencies{
		stdin: bytes.NewReader(nil), stdout: &output, stderr: &output,
		cwd:       func() (string, error) { return root, nil },
		homeDir:   func() (string, error) { return filepath.Join(root, "home"), nil },
		configDir: func() (string, error) { return filepath.Join(root, "config"), nil },
		registry:  step.NewRegistry(), isInteractive: func(io.Reader) bool { return false },
	})
	command.SetArgs(nil)
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	text := output.String()
	if !strings.Contains(text, "local\tlocal\tlocal workflow") || !strings.Contains(text, "global\tglobal\tglobal workflow") {
		t.Fatalf("output = %q", text)
	}
}

func TestListCommandIncludesScope(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".wuko", "workflows")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestWorkflow(t, filepath.Join(dir, "build.yaml"), "Build locally")
	var output bytes.Buffer
	command := newRootCmd(dependencies{
		stdin: bytes.NewReader(nil), stdout: &output, stderr: &output,
		cwd:       func() (string, error) { return root, nil },
		homeDir:   func() (string, error) { return filepath.Join(root, "home"), nil },
		configDir: func() (string, error) { return filepath.Join(root, "config"), nil },
		registry:  step.NewRegistry(),
	})
	command.SetArgs([]string{"list"})
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "build\tlocal\tBuild locally") {
		t.Fatalf("output = %q", output.String())
	}
}

func TestBareCommandInteractiveSelectionPrintsRunCommand(t *testing.T) {
	root := t.TempDir()
	localDir := filepath.Join(root, ".wuko", "workflows")
	globalDir := filepath.Join(root, "home", ".wuko", "workflows")
	if err := os.MkdirAll(localDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(globalDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestWorkflow(t, filepath.Join(localDir, "local.yaml"), "local workflow")
	writeTestWorkflow(t, filepath.Join(globalDir, "global.yaml"), "global workflow")
	var output bytes.Buffer
	command := newRootCmd(dependencies{
		stdin: bytes.NewBufferString("\r"), stdout: &output, stderr: &output,
		cwd:       func() (string, error) { return root, nil },
		homeDir:   func() (string, error) { return filepath.Join(root, "home"), nil },
		configDir: func() (string, error) { return filepath.Join(root, "config"), nil },
		registry:  step.NewRegistry(), isInteractive: func(io.Reader) bool { return true },
	})
	command.SetArgs(nil)
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "wuko run global") {
		t.Fatalf("output = %q", output.String())
	}
}

func TestRunCommandFromFile(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "workflow.yaml")
	writeTestWorkflow(t, path, "file workflow")
	registry := step.NewRegistry()
	if err := shell.Register(registry); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	command := newRootCmd(dependencies{
		stdin: bytes.NewReader(nil), stdout: &output, stderr: &output,
		cwd:       func() (string, error) { return root, nil },
		homeDir:   func() (string, error) { return filepath.Join(root, "home"), nil },
		configDir: func() (string, error) { return filepath.Join(root, "config"), nil },
		registry:  registry,
	})
	command.SetArgs([]string{"run", "--file", path, "--dry-run"})
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "Workflow workflow") {
		t.Fatalf("output = %q", output.String())
	}
}

func TestRunCommandRejectsMissingOrConflictingWorkflowSelector(t *testing.T) {
	for _, args := range [][]string{{"run"}, {"run", "name", "--file", "workflow.yaml"}} {
		command := newRootCmd(dependencies{
			stdin: bytes.NewReader(nil), stdout: io.Discard, stderr: io.Discard,
			cwd:     func() (string, error) { return t.TempDir(), nil },
			homeDir: func() (string, error) { return "", nil }, configDir: func() (string, error) { return "", nil },
			registry: step.NewRegistry(),
		})
		command.SetArgs(args)
		if err := command.ExecuteContext(t.Context()); err == nil {
			t.Fatalf("args %v: expected error", args)
		}
	}
}

func writeTestWorkflow(t *testing.T, path, description string) {
	t.Helper()
	name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	data := fmt.Sprintf("version: 1\nname: %s\ndescription: %s\nsteps:\n  - id: run\n    type: shell\n    with:\n      command: true\n", name, description)
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}
