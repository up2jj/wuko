package cmd

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/steps/shell"
	"github.com/up2jj/wuko/workflow"
)

func TestRunWorkflowTargetSelectsOnlyRequestedSteps(t *testing.T) {
	root := t.TempDir()
	workflowDir := filepath.Join(root, ".wuko", "workflows")
	if err := os.MkdirAll(workflowDir, 0o755); err != nil {
		t.Fatal(err)
	}
	data := `version: 1
name: deploy
targets:
  production:
    steps:
      - id: deploy
        type: shell
        with: {script: printf production}
  staging:
    steps:
      - id: deploy
        type: shell
        with: {script: printf staging}
`
	if err := os.WriteFile(filepath.Join(workflowDir, "deploy.yaml"), []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	registry := step.NewRegistry()
	if err := shell.Register(registry); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	command := newRootCmd(dependencies{
		stdin: bytes.NewReader(nil), stdout: &output, stderr: io.Discard,
		cwd: func() (string, error) { return root, nil }, homeDir: func() (string, error) { return "", nil },
		configDir: func() (string, error) { return "", nil }, registry: registry,
	})
	command.SetArgs([]string{"run", "deploy", "staging"})
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "staging") || strings.Contains(output.String(), "production") {
		t.Fatalf("output = %q", output.String())
	}
}

func TestRunWorkflowTargetIsRequired(t *testing.T) {
	root := t.TempDir()
	workflowDir := filepath.Join(root, ".wuko", "workflows")
	if err := os.MkdirAll(workflowDir, 0o755); err != nil {
		t.Fatal(err)
	}
	data := `version: 1
name: deploy
targets:
  production:
    steps:
      - id: deploy
        type: shell
        with: {script: true}
`
	if err := os.WriteFile(filepath.Join(workflowDir, "deploy.yaml"), []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	command := newRootCmd(dependencies{
		stdin: bytes.NewReader(nil), stdout: io.Discard, stderr: io.Discard,
		cwd: func() (string, error) { return root, nil }, homeDir: func() (string, error) { return "", nil },
		configDir: func() (string, error) { return "", nil }, registry: step.NewRegistry(),
	})
	command.SetArgs([]string{"run", "deploy"})
	if err := command.ExecuteContext(t.Context()); err == nil || !strings.Contains(err.Error(), "requires a target") {
		t.Fatalf("error = %v, want target requirement", err)
	}
}

func TestTargetCommandsAndCompletion(t *testing.T) {
	root := t.TempDir()
	workflowDir := filepath.Join(root, ".wuko", "workflows")
	if err := os.MkdirAll(workflowDir, 0o755); err != nil {
		t.Fatal(err)
	}
	data := `version: 1
name: deploy
targets:
  production:
    steps:
      - id: deploy
        type: shell
        with: {script: true}
  staging:
    steps:
      - id: deploy
        type: shell
        with: {script: true}
`
	if err := os.WriteFile(filepath.Join(workflowDir, "deploy.yaml"), []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	registry := step.NewRegistry()
	if err := shell.Register(registry); err != nil {
		t.Fatal(err)
	}
	deps := dependencies{
		stdin: bytes.NewReader(nil), stdout: io.Discard, stderr: io.Discard,
		cwd: func() (string, error) { return root, nil }, homeDir: func() (string, error) { return "", nil },
		configDir: func() (string, error) { return "", nil }, registry: registry,
	}
	command := newRootCmd(deps)
	command.SetArgs([]string{"tree", "deploy", "production"})
	var treeOutput bytes.Buffer
	command.SetOut(&treeOutput)
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(treeOutput.String(), "deploy\n") {
		t.Fatalf("tree output = %q", treeOutput.String())
	}

	validateOutput := new(bytes.Buffer)
	validateCommand := newRootCmd(deps)
	validateCommand.SetOut(validateOutput)
	validateCommand.SetArgs([]string{"validate", "deploy", "staging"})
	if err := validateCommand.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(validateOutput.String(), "deploy (staging): valid") {
		t.Fatalf("validate output = %q", validateOutput.String())
	}

	uiTarget, err := resolveUIRunTarget(root, "", "", []string{"deploy", "production"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if uiTarget.targetName != "production" {
		t.Fatalf("ui target = %#v", uiTarget)
	}

	completion, directive := workflowCompletion(deps, true)(nil, []string{"deploy"}, "")
	if directive == 0 || !strings.Contains(strings.Join(completion, "\n"), "production\t") || !strings.Contains(strings.Join(completion, "\n"), "staging\t") {
		t.Fatalf("completion = %#v, directive = %v", completion, directive)
	}
}

func TestTargetPickerOptionAndCommand(t *testing.T) {
	option := workflowPickerOption(workflow.Source{
		Name: "deploy", Target: "production", Scope: "local", Description: "Deploy", Path: "/project/deploy.yaml",
	})
	if option.Label != "deploy production" || !strings.Contains(option.Description, "target: production") {
		t.Fatalf("option = %#v", option)
	}

	var output bytes.Buffer
	if err := writeWorkflowSource(&output, workflow.Source{
		Name: "deploy", Target: "production", Scope: "local", Description: "Deploy", Path: "/project/deploy.yaml",
	}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "\ttarget production\n") {
		t.Fatalf("source output = %q", output.String())
	}
}
