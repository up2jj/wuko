package cmd

import (
	"os/exec"
	"path/filepath"
	"testing"
)

func TestGoPluginScaffoldBuilds(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "wuko-plugin-acme")
	if err := writeGoPluginScaffold(directory, "acme"); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("go", "test", "./...")
	command.Dir = directory
	command.Env = append(command.Environ(), "GOCACHE="+filepath.Join(t.TempDir(), "cache"))
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("generated plugin tests: %v\n%s", err, output)
	}
}
