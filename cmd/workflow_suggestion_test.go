package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkflowCommandsSuggestMisspelledNames(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, ".wuko", "workflows", "deploy.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestWorkflow(t, path, "Deploy")
	for _, name := range []string{"run", "tree", "validate", "ui"} {
		t.Run(name, func(t *testing.T) {
			var output bytes.Buffer
			command := newRootCmd(listTestDependencies(root, &output))
			command.SetArgs([]string{name, "depoly"})
			err := command.ExecuteContext(t.Context())
			if err == nil || !strings.Contains(err.Error(), `did you mean "deploy"?`) {
				t.Fatalf("ExecuteContext() error = %v", err)
			}
		})
	}
}
