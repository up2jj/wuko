package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTimeStepRunsWithVarOverrideAndAppearsInTree(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "time.yaml")
	data := []byte(`version: 1
name: release-time
timezone: Europe/Warsaw
steps:
  - id: stamp
    type: time
    with: {format: "2006-01-02"}
  - id: verify
    type: assert
    with: {expr: 'vars.stamp == "fixture-date"', message: unexpected stamp}
`)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}

	command := NewRootCmd()
	command.SetArgs([]string{"run", "--file", path, "--var", "stamp=fixture-date"})
	command.SetOut(&bytes.Buffer{})
	command.SetErr(&bytes.Buffer{})
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	command = NewRootCmd()
	command.SetArgs([]string{"tree", "--file", path})
	command.SetOut(&output)
	command.SetErr(&bytes.Buffer{})
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "stamp (time)") {
		t.Fatalf("tree output = %q", output.String())
	}
}
