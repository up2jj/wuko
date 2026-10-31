package workflow

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/up2jj/wuko/internal/testfs"
)

func TestLoadReporterSettings(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, yaml string
		want       ReporterConfig
	}{
		{"default", "", ReporterConfig{}},
		{"shorthand", "reporter: quiet\n", ReporterConfig{Type: "quiet"}},
		{"mapping", "reporter: {type: github, quiet: [set, decode], hide_skipped: true}\n", ReporterConfig{Type: "github", Quiet: []string{"set", "decode"}, HideSkipped: true}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "workflow.yaml")
			testfs.Write(t, path, "version: 1\nname: output\n"+test.yaml+"steps:\n  - quiet: true\n    steps:\n      - {id: work, type: shell, quiet: false}\n")
			definition, err := Load(path)
			if err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(test.want, definition.Reporter); diff != "" {
				t.Fatalf("reporter (-want +got):\n%s", diff)
			}
			if definition.Steps[0].Quiet == nil || !*definition.Steps[0].Quiet || *definition.Steps[0].Steps[0].Quiet {
				t.Fatal("quiet values lost")
			}
		})
	}
}

func TestLoadRejectsInvalidReporterSettings(t *testing.T) {
	t.Parallel()
	for _, config := range []string{"reporter: 3", "reporter: ''", "reporter: []", "reporter: {mode: quiet}", "reporter: {type: ''}", "reporter: {quiet: shell}", "reporter: {quiet: [3]}", "reporter: {quiet: ['']}", "reporter: {hide_skipped: 'true'}"} {
		t.Run(config, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "workflow.yaml")
			testfs.Write(t, path, "version: 1\nname: output\n"+config+"\nsteps: [{id: work, type: shell}]\n")
			if _, err := Load(path); err == nil {
				t.Fatal("invalid reporter accepted")
			}
		})
	}
	path := filepath.Join(t.TempDir(), "workflow.yaml")
	testfs.Write(t, path, "version: 1\nname: output\nsteps: [{id: work, type: shell, quiet: 'true'}]\n")
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "quiet must be a boolean") {
		t.Fatalf("error = %v", err)
	}
}

func TestQuietRequirePreservesNestedPreferences(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	testfs.Write(t, filepath.Join(dir, "steps.yaml"), "- {id: hidden, type: shell}\n- {id: visible, type: shell, quiet: false}\n")
	path := filepath.Join(dir, "workflow.yaml")
	testfs.Write(t, path, "version: 1\nname: output\nsteps:\n  - require: steps.yaml\n    quiet: true\n")
	definition, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(definition.Steps) != 1 || definition.Steps[0].Quiet == nil || !*definition.Steps[0].Quiet || len(definition.Steps[0].Steps) != 2 {
		t.Fatalf("steps = %#v", definition.Steps)
	}
}
