package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/up2jj/wuko/step"
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
	var output bytes.Buffer
	command := newRootCmd(dependencies{
		stdin: bytes.NewReader(nil), stdout: &output, stderr: &output,
		cwd:       func() (string, error) { return root, nil },
		homeDir:   func() (string, error) { return filepath.Join(root, "home"), nil },
		configDir: func() (string, error) { return filepath.Join(root, "config"), nil },
		registry:  registry,
	})
	command.SetArgs([]string{"run", "hello", "--var", "name=world"})
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "world") {
		t.Fatalf("output = %q", output.String())
	}
}
