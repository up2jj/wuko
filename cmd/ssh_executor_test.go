package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuiltinSSHExecutorDryRunDoesNotConnect(t *testing.T) {
	file := filepath.Join(t.TempDir(), "remote.yaml")
	if err := os.WriteFile(file, []byte(`version: 1
name: remote-dry-run
steps:
  - executor:
      type: ssh
      with: {host: never-connect.invalid, directory: /remote/does-not-exist}
    steps:
      - id: remote_command
        type: shell
        with: {command: hostname}
`), 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	command := NewRootCmd()
	command.SetIn(bytes.NewReader(nil))
	command.SetOut(&output)
	command.SetErr(&output)
	command.SetArgs([]string{"--env-loader", "none", "run", "--file", file, "--dry-run"})
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatalf("SSH dry run: %v\n%s", err, output.String())
	}
	if !strings.Contains(output.String(), "remote_command") {
		t.Fatalf("dry run did not describe the SSH step: %s", output.String())
	}
}
