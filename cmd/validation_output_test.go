package cmd

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/up2jj/wuko/internal/gittest"
	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/validation"
)

func TestValidateReportsEmptyDiscovery(t *testing.T) {
	t.Parallel()
	for _, format := range []string{"text", "json"} {
		t.Run(format, func(t *testing.T) {
			var output bytes.Buffer
			command := newRootCmd(listTestDependencies(t.TempDir(), &output))
			command.SetArgs([]string{"validate", "--format", format})
			if err := command.ExecuteContext(t.Context()); err != nil {
				t.Fatal(err)
			}
			if format == "text" {
				if got := output.String(); got != "No workflows found.\n" {
					t.Fatalf("output = %q", got)
				}
				return
			}
			var document validation.Document
			if err := json.Unmarshal(output.Bytes(), &document); err != nil {
				t.Fatalf("output = %q: %v", output.String(), err)
			}
			if !document.Valid || len(document.Issues) != 0 {
				t.Fatalf("document = %#v", document)
			}
		})
	}
}

func TestValidateEmptyDiscoveryStillChecksGitHooks(t *testing.T) {
	t.Parallel()
	root := gittest.Init(t)
	writeGitHookManifest(t, root, "pre-commit:\n    - workflow: missing")
	var output bytes.Buffer
	command := newRootCmd(gitHookTestDependencies(t, root, &output, io.Discard, func(string) string { return "" }))
	command.SetArgs([]string{"validate"})
	if err := command.ExecuteContext(t.Context()); err == nil {
		t.Fatal("expected missing hook workflow to fail validation")
	}
	if !strings.Contains(output.String(), "No workflows found.") {
		t.Fatalf("output = %q", output.String())
	}
}

func TestValidateJSONCollectsIndependentWorkflows(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
	root := gittest.Init(t)
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
