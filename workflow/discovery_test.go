package workflow

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDiscoverPrecedence(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	nested := filepath.Join(project, "nested")
	home := filepath.Join(root, "home")
	config := filepath.Join(root, "config")
	for _, dir := range []string{nested, filepath.Join(project, ".wuko", "workflows"), filepath.Join(home, ".wuko", "workflows"), filepath.Join(config, "wuko", "workflows")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeWorkflow(t, filepath.Join(config, "wuko", "workflows", "shared.yaml"), "config")
	writeWorkflow(t, filepath.Join(home, ".wuko", "workflows", "shared.yaml"), "home")
	projectPath := filepath.Join(project, ".wuko", "workflows", "shared.yaml")
	writeWorkflow(t, projectPath, "project")

	sources, err := Discover(nested, home, config)
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 1 || sources[0].Path != projectPath || sources[0].Description != "project" {
		t.Fatalf("sources = %#v", sources)
	}
}

func TestDiscoverAllIncludesScopesAndEffectivePrecedence(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	nested := filepath.Join(project, "nested")
	home := filepath.Join(root, "home")
	config := filepath.Join(root, "config")
	for _, dir := range []string{
		nested,
		filepath.Join(project, ".wuko", "workflows"),
		filepath.Join(home, ".wuko", "workflows"),
		filepath.Join(config, "wuko", "workflows"),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeWorkflow(t, filepath.Join(project, ".wuko", "workflows", "shared.yaml"), "local")
	writeWorkflow(t, filepath.Join(home, ".wuko", "workflows", "shared.yaml"), "global home")
	writeWorkflow(t, filepath.Join(config, "wuko", "workflows", "config.yaml"), "global config")

	sources, err := DiscoverAll(nested, home, config)
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 3 {
		t.Fatalf("sources = %#v, want 3 entries", sources)
	}
	if sources[0].Name != "config" || sources[0].Scope != "global" || !sources[0].Effective {
		t.Fatalf("sources[0] = %#v", sources[0])
	}
	if sources[1].Name != "shared" || sources[1].Scope != "local" || !sources[1].Effective {
		t.Fatalf("sources[1] = %#v", sources[1])
	}
	if sources[2].Name != "shared" || sources[2].Scope != "global" || sources[2].Effective {
		t.Fatalf("sources[2] = %#v", sources[2])
	}
}

func TestDiscoverRejectsDuplicateExtensions(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".wuko", "workflows")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeWorkflow(t, filepath.Join(dir, "same.yaml"), "one")
	writeWorkflow(t, filepath.Join(dir, "same.yml"), "two")
	if _, err := Discover(root, "", ""); err == nil {
		t.Fatal("expected duplicate workflow error")
	}
}

func TestLoadRejectsNonStringEnvironmentAndMultipleDocuments(t *testing.T) {
	tests := map[string]string{
		"numeric environment": "version: 1\nname: bad\nenv:\n  PORT: 123\nsteps:\n  - id: run\n    type: shell\n    with: {}\n",
		"multiple documents":  "version: 1\nname: bad\nsteps:\n  - id: run\n    type: shell\n    with: {}\n---\nextra: true\n",
	}
	for name, data := range tests {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "bad.yaml")
			if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(path); err == nil {
				t.Fatal("expected load error")
			}
		})
	}
}

func writeWorkflow(t *testing.T, path, description string) {
	t.Helper()
	data := "version: 1\nname: shared\ndescription: " + description + "\nsteps:\n  - id: run\n    type: shell\n    with:\n      command: true\n"
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}
