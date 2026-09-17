package cmd

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/validation"
)

func TestValidateJSONCollectsIndependentWorkflows(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, ".wuko", "workflows")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"first", "second"} {
		data := []byte("version: 1\nname: " + name + "\nsteps:\n  - id: broken\n")
		if err := os.WriteFile(filepath.Join(directory, name+".yaml"), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var output bytes.Buffer
	command := newRootCmd(dependencies{
		stdin: bytes.NewReader(nil), stdout: &output, stderr: io.Discard,
		cwd: func() (string, error) { return root, nil }, homeDir: func() (string, error) { return t.TempDir(), nil },
		configDir: func() (string, error) { return t.TempDir(), nil }, registry: step.NewRegistry(),
	})
	command.SetArgs([]string{"validate", "--format", "json"})
	err := command.ExecuteContext(t.Context())
	if err == nil {
		t.Fatal("expected validation failure")
	}
	var document validation.Document
	if decodeErr := json.Unmarshal(output.Bytes(), &document); decodeErr != nil {
		t.Fatalf("output = %q: %v", output.String(), decodeErr)
	}
	if document.Valid || len(document.Issues) != 2 {
		t.Fatalf("document = %#v", document)
	}
	if document.Issues[0].Workflow != "first" || document.Issues[1].Workflow != "second" {
		t.Fatalf("issues = %#v", document.Issues)
	}
}

func TestValidateJSONKeepsWorkflowIssuesWhenGitHookBindingFails(t *testing.T) {
	root := initGitHookRepository(t)
	directory := filepath.Join(root, ".wuko", "workflows")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"first", "second"} {
		data := []byte("version: 1\nname: " + name + "\nsteps:\n  - id: broken\n")
		if err := os.WriteFile(filepath.Join(directory, name+".yaml"), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeGitHookManifest(t, root, "pre-commit:\n    - workflow: second")
	var output bytes.Buffer
	command := newRootCmd(gitHookTestDependencies(t, root, &output, io.Discard, func(string) string { return "" }))
	command.SetArgs([]string{"validate", "--format", "json"})
	if err := command.ExecuteContext(t.Context()); err == nil {
		t.Fatal("expected validation failure")
	}
	var document validation.Document
	if decodeErr := json.Unmarshal(output.Bytes(), &document); decodeErr != nil {
		t.Fatalf("output = %q: %v", output.String(), decodeErr)
	}
	workflows := make(map[string]bool)
	for _, issue := range document.Issues {
		workflows[issue.Workflow] = true
	}
	if document.Valid || !workflows["first"] || !workflows["second"] {
		t.Fatalf("document = %#v", document)
	}
}
