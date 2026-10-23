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

func TestWorkflowCommandsSuggestFileFlagForLocalPaths(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "workflow.yaml")
	// The hint should not parse or execute the file before the user supplies --file.
	if err := os.WriteFile(path, []byte("unfinished: [\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"run", "tree", "validate", "ui"} {
		for _, selector := range []string{"./workflow.yaml", "workflow.yaml", path} {
			t.Run(name+"/"+selector, func(t *testing.T) {
				var output bytes.Buffer
				command := newRootCmd(listTestDependencies(root, &output))
				command.SetArgs([]string{name, selector})
				err := command.ExecuteContext(t.Context())
				if err == nil || !strings.Contains(err.Error(), "use --file to load a local workflow") {
					t.Fatalf("ExecuteContext() error = %v, want --file hint", err)
				}
				if strings.Contains(err.Error(), "yaml:") {
					t.Fatalf("file was parsed during name lookup: %v", err)
				}
			})
		}
	}
}
