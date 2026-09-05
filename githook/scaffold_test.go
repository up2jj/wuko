package githook

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestScaffoldCreatesStarterManifestAndWorkflows(t *testing.T) {
	root := t.TempDir()
	paths, err := Scaffold(Repository{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 3 {
		t.Fatalf("created paths = %d, want 3", len(paths))
	}
	manifest, err := LoadManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Hooks) != 3 || manifest.Hooks["pre-commit"][0].Target != "staged" || manifest.Hooks["pre-push"][0].Target != "pushed" {
		t.Fatalf("manifest = %#v", manifest)
	}
	for _, relative := range []string{ManifestPath, ".wuko/workflows/git-check.yaml", ".wuko/workflows/git-commit-message.yaml"} {
		info, err := os.Stat(filepath.Join(root, filepath.FromSlash(relative)))
		if err != nil {
			t.Fatalf("stat %s: %v", relative, err)
		}
		if info.Mode().Perm() != 0o644 {
			t.Fatalf("mode for %s = %o, want 644", relative, info.Mode().Perm())
		}
	}
	commitWorkflow, err := os.ReadFile(filepath.Join(root, ".wuko", "workflows", "git-commit-message.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(commitWorkflow), "type: file") || !strings.Contains(string(commitWorkflow), ".steps.read_message.content") || strings.Contains(string(commitWorkflow), "type: shell") {
		t.Fatalf("commit-message workflow did not compose file reading with validation:\n%s", commitWorkflow)
	}
}

func TestScaffoldRefusesCollisionBeforeCreatingFiles(t *testing.T) {
	root := t.TempDir()
	workflowDir := filepath.Join(root, ".wuko", "workflows")
	if err := os.MkdirAll(workflowDir, 0o755); err != nil {
		t.Fatal(err)
	}
	existing := filepath.Join(workflowDir, "git-check.yaml")
	if err := os.WriteFile(existing, []byte("keep me\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Scaffold(Repository{Root: root})
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("error = %v", err)
	}
	data, readErr := os.ReadFile(existing)
	if readErr != nil || string(data) != "keep me\n" {
		t.Fatalf("existing workflow = %q, %v", data, readErr)
	}
	if _, statErr := os.Stat(filepath.Join(root, filepath.FromSlash(ManifestPath))); !os.IsNotExist(statErr) {
		t.Fatalf("manifest was created before collision refusal: %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(workflowDir, "git-commit-message.yaml")); !os.IsNotExist(statErr) {
		t.Fatalf("second workflow was created before collision refusal: %v", statErr)
	}
}
