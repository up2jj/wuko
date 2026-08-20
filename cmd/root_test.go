package cmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/up2jj/wuko/step"
	keyvaluestep "github.com/up2jj/wuko/steps/key_value"
	luastep "github.com/up2jj/wuko/steps/lua"
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

func TestRootCommandRegistersJSONPathStep(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "jsonpath.yaml")
	data := `version: 1
name: jsonpath
vars:
  document: {project: {name: wuko}}
steps:
  - id: name
    type: jsonpath
    with:
      from: vars.document
      query: $.project.name
      result: one
`
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	command := NewRootCmd()
	command.SetIn(bytes.NewReader(nil))
	command.SetOut(io.Discard)
	command.SetErr(io.Discard)
	command.SetArgs([]string{"run", "--file", path})
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestRootCommandRegistersAssertStep(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "assert.yaml")
	sentinel := filepath.Join(root, "should-not-exist")
	data := fmt.Sprintf(`version: 1
name: assertions
steps:
  - id: release
    type: set
    with:
      variable: release
      value: wuko
  - id: verify
    type: assert
    with:
      expr: steps.release.value == "wuko"
      message: release value is invalid
  - id: verify_empty_result
    type: assert
    with:
      expr: len(steps.verify) == 0
      message: successful assertion published outputs
  - id: reject
    type: assert
    with:
      expr: false
      message: Release {{ .vars.release }} rejected
  - id: after_failure
    type: file
    with:
      operation: write
      path: %q
      content: should not be written
`, sentinel)
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	command := NewRootCmd()
	command.SetIn(bytes.NewReader(nil))
	command.SetOut(io.Discard)
	command.SetErr(io.Discard)
	command.SetArgs([]string{"run", "--file", path})
	err := command.ExecuteContext(t.Context())
	if err == nil || !strings.Contains(err.Error(), "assertion failed: Release wuko rejected") {
		t.Fatalf("error = %v", err)
	}
	if _, statErr := os.Stat(sentinel); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("step after failed assertion ran: %v", statErr)
	}
}

func TestRunCommandImportsVariableFilesBeforeInlineOverrides(t *testing.T) {
	root := t.TempDir()
	workflowDir := filepath.Join(root, ".wuko", "workflows")
	if err := os.MkdirAll(workflowDir, 0o755); err != nil {
		t.Fatal(err)
	}
	data := `version: 1
name: imported
vars: {name: workflow}
steps:
  - id: print
    type: shell
    with:
      script: "printf '%s:%s' \"$1\" \"$2\""
      args: ["{{ .vars.name }}", "{{ .vars.channel }}"]
`
	if err := os.WriteFile(filepath.Join(workflowDir, "imported.yaml"), []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "defaults.toml"), []byte("name = \"file\"\nchannel = \"stable\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	registry := step.NewRegistry()
	if err := shell.Register(registry); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	command := newRootCmd(dependencies{
		stdin: bytes.NewReader(nil), stdout: &output, stderr: io.Discard,
		cwd: func() (string, error) { return root, nil }, homeDir: func() (string, error) { return "", nil }, configDir: func() (string, error) { return "", nil },
		registry: registry,
	})
	command.SetArgs([]string{"run", "imported", "--var-file", "defaults.toml", "--var", "name=inline"})
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if output.String() != "inline:stable" {
		t.Fatalf("output = %q", output.String())
	}
}

func TestRunDebugPinpointsInvalidLuaSyntax(t *testing.T) {
	root := t.TempDir()
	workflowDir := filepath.Join(root, ".wuko", "workflows")
	if err := os.MkdirAll(workflowDir, 0o755); err != nil {
		t.Fatal(err)
	}
	data := `version: 1
name: bad-lua
steps:
  - id: prepare
    type: lua
    with:
      source: |
        local value = )
        wuko.output("value", value)
`
	if err := os.WriteFile(filepath.Join(workflowDir, "bad-lua.yaml"), []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	registry := step.NewRegistry()
	if err := luastep.Register(registry); err != nil {
		t.Fatal(err)
	}
	var output, diagnostics bytes.Buffer
	command := newRootCmd(dependencies{
		stdin: bytes.NewReader(nil), stdout: &output, stderr: &diagnostics,
		cwd: func() (string, error) { return root, nil }, homeDir: func() (string, error) { return "", nil }, configDir: func() (string, error) { return "", nil },
		registry: registry,
	})
	command.SetArgs([]string{"run", "bad-lua", "--debug"})
	err := command.ExecuteContext(t.Context())
	if err == nil || !strings.Contains(err.Error(), "compiling Lua") {
		t.Fatalf("error = %v", err)
	}
	trace := diagnostics.String()
	for _, want := range []string{"[debug +", ".wuko/workflows/bad-lua.yaml:4:5", "step prepare (lua)", "validation failed", "compiling Lua"} {
		if !strings.Contains(trace, want) {
			t.Fatalf("diagnostics = %q, want %q", trace, want)
		}
	}
	if output.Len() != 0 {
		t.Fatalf("stdout = %q", output.String())
	}
}

func TestRunDebugRedactsRenderedEnvironment(t *testing.T) {
	root := t.TempDir()
	workflowDir := filepath.Join(root, ".wuko", "workflows")
	if err := os.MkdirAll(workflowDir, 0o755); err != nil {
		t.Fatal(err)
	}
	data := `version: 1
name: redacted
steps:
  - id: deploy
    type: shell
    with:
      script: "true"
      env:
        DEPLOY_TOKEN: supersecret
`
	if err := os.WriteFile(filepath.Join(workflowDir, "redacted.yaml"), []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	registry := step.NewRegistry()
	if err := shell.Register(registry); err != nil {
		t.Fatal(err)
	}
	var diagnostics bytes.Buffer
	command := newRootCmd(dependencies{
		stdin: bytes.NewReader(nil), stdout: io.Discard, stderr: &diagnostics,
		cwd: func() (string, error) { return root, nil }, homeDir: func() (string, error) { return "", nil }, configDir: func() (string, error) { return "", nil },
		registry: registry,
	})
	command.SetArgs([]string{"--debug", "run", "redacted"})
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	trace := diagnostics.String()
	if strings.Contains(trace, "supersecret") || !strings.Contains(trace, "<redacted>") {
		t.Fatalf("diagnostics = %q", trace)
	}
}

func TestRunDebugReportsLuaRuntimeAttempts(t *testing.T) {
	root := t.TempDir()
	workflowDir := filepath.Join(root, ".wuko", "workflows")
	if err := os.MkdirAll(workflowDir, 0o755); err != nil {
		t.Fatal(err)
	}
	data := `version: 1
name: runtime-lua
steps:
  - id: prepare
    type: lua
    retry:
      max_attempts: 2
      initial_delay: 0s
      max_delay: 0s
    with:
      source: error("lua boom")
`
	if err := os.WriteFile(filepath.Join(workflowDir, "runtime-lua.yaml"), []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	registry := step.NewRegistry()
	if err := luastep.Register(registry); err != nil {
		t.Fatal(err)
	}
	var diagnostics bytes.Buffer
	command := newRootCmd(dependencies{
		stdin: bytes.NewReader(nil), stdout: io.Discard, stderr: &diagnostics,
		cwd: func() (string, error) { return root, nil }, homeDir: func() (string, error) { return "", nil }, configDir: func() (string, error) { return "", nil },
		registry: registry,
	})
	command.SetArgs([]string{"run", "runtime-lua", "--debug"})
	err := command.ExecuteContext(t.Context())
	if err == nil || !strings.Contains(err.Error(), "running Lua") {
		t.Fatalf("error = %v", err)
	}
	trace := diagnostics.String()
	for _, want := range []string{"step prepare (lua)", "attempt failed", "attempt=1/2", "attempt=2/2", "lua boom"} {
		if !strings.Contains(trace, want) {
			t.Fatalf("diagnostics = %q, want %q", trace, want)
		}
	}
}

func TestDebugFlagCoversWorkflowCommands(t *testing.T) {
	root := t.TempDir()
	workflowDir := filepath.Join(root, ".wuko", "workflows")
	if err := os.MkdirAll(workflowDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workflowDir, "debuggable.yaml"), []byte("version: 1\nname: debuggable\ndescription: debuggable workflow\nsteps:\n  - id: run\n    type: shell\n    with: {script: 'true'}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	registry := step.NewRegistry()
	if err := shell.Register(registry); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		args []string
		want string
	}{
		{name: "picker", args: []string{"--debug"}, want: "workflow picker"},
		{name: "list", args: []string{"list", "--debug"}, want: "discovery succeeded"},
		{name: "validate", args: []string{"--debug", "validate", "debuggable"}, want: "validation succeeded"},
		{name: "tree", args: []string{"tree", "debuggable", "--debug"}, want: "load succeeded"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var diagnostics bytes.Buffer
			command := newRootCmd(dependencies{
				stdin: bytes.NewReader(nil), stdout: io.Discard, stderr: &diagnostics,
				cwd: func() (string, error) { return root, nil }, homeDir: func() (string, error) { return "", nil }, configDir: func() (string, error) { return "", nil },
				registry: registry, isInteractive: func(io.Reader) bool { return false },
			})
			command.SetArgs(test.args)
			if err := command.ExecuteContext(t.Context()); err != nil {
				t.Fatal(err)
			}
			if got := diagnostics.String(); !strings.Contains(got, "[debug +") || !strings.Contains(got, test.want) {
				t.Fatalf("diagnostics = %q, want %q", got, test.want)
			}
		})
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

func TestBareCommandInteractiveWithoutWorkflowsPrintsGuidance(t *testing.T) {
	root := t.TempDir()
	var output bytes.Buffer
	command := newRootCmd(dependencies{
		stdin: bytes.NewReader(nil), stdout: &output, stderr: &output,
		cwd:       func() (string, error) { return root, nil },
		homeDir:   func() (string, error) { return filepath.Join(root, "home"), nil },
		configDir: func() (string, error) { return filepath.Join(root, "config"), nil },
		registry:  step.NewRegistry(), isInteractive: func(io.Reader) bool { return true },
	})
	command.SetArgs(nil)
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	want := `No workflows found.

Create .wuko/workflows/hello.yaml:

  version: 1
  name: hello
  steps:
    - id: greet
      type: shell
      with:
        script: echo "Hello from Wuko"

Run it:
  wuko run hello

Run a file directly:
  wuko run --file ./workflow.yaml

Run a trusted remote workflow:
  wuko run https://example.com/workflow.yaml
  wuko run github:owner/repo@main:path/to/workflow.yaml

More help:
  wuko --help
`
	if got := output.String(); got != want {
		t.Fatalf("output = %q, want %q", got, want)
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
