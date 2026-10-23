package cmd

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/up2jj/wuko/internal/testfs"
)

func TestWorkflowCommandsSuggestMisspelledNames(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := filepath.Join(root, ".wuko", "workflows", "deploy.yaml")
	testfs.MkdirAll(t, filepath.Dir(path), 0o755)
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
	t.Parallel()
	root := t.TempDir()
	path := filepath.Join(root, "workflow.yaml")
	// The hint should not parse or execute the file before the user supplies --file.
	testfs.WriteFile(t, path, []byte("unfinished: [\n"), 0o600)
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
