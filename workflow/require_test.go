package workflow

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadExpandsRequiredStepFilesInPlace(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "workflow.yaml"), `version: 1
name: split
steps:
  - id: prepare
    type: shell
    with: {command: prepare}
  - require: fragments/build.yaml
  - id: publish
    type: shell
    with: {command: publish}
`)
	writeTestFile(t, filepath.Join(dir, "fragments", "build.yaml"), `
- id: build
  type: shell
  with: {command: build}
- require: nested/test.yaml
`)
	writeTestFile(t, filepath.Join(dir, "fragments", "nested", "test.yaml"), `steps:
  - id: test
    type: shell
    with: {command: test}
`)

	definition, err := Load(filepath.Join(dir, "workflow.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"prepare", "build", "test", "publish"}
	if len(definition.Steps) != len(want) {
		t.Fatalf("steps = %#v, want ids %v", definition.Steps, want)
	}
	for i, id := range want {
		if definition.Steps[i].ID != id {
			t.Errorf("step %d id = %q, want %q", i+1, definition.Steps[i].ID, id)
		}
		if definition.Steps[i].Require != nil {
			t.Errorf("step %d still contains require", i+1)
		}
	}
}

func TestLoadReplacesNestedRequiredSequenceWithExpandedSteps(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "workflow.yaml"), `version: 1
name: nested-expansion
steps:
  - id: grouped
    batch:
      items: vars.items
      size: 2
      steps:
        - require: fragment.yaml
`)
	writeTestFile(t, filepath.Join(dir, "fragment.yaml"), `
- {id: prepare, type: shell}
- {id: run, type: shell}
`)

	definition, err := Load(filepath.Join(dir, "workflow.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	children := definition.Steps[0].Batch.Steps
	if len(children) != 2 || children[0].ID != "prepare" || children[1].ID != "run" {
		t.Fatalf("expanded children = %#v", children)
	}
}

func TestLoadValidatesExpandedRequiredStepsTogether(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "workflow.yaml"), `version: 1
name: duplicate
steps:
  - require: first.yaml
  - require: second.yaml
`)
	for _, name := range []string{"first.yaml", "second.yaml"} {
		writeTestFile(t, filepath.Join(dir, name), "- id: repeated\n  type: shell\n  with: {command: true}\n")
	}

	_, err := Load(filepath.Join(dir, "workflow.yaml"))
	if err == nil || !strings.Contains(err.Error(), `duplicate step id "repeated"`) {
		t.Fatalf("error = %v, want duplicate id", err)
	}
}

func TestLoadRejectsRequiredStepCycle(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "workflow.yaml"), "version: 1\nname: cycle\nsteps:\n  - require: first.yaml\n")
	writeTestFile(t, filepath.Join(dir, "first.yaml"), "- require: nested/second.yaml\n")
	writeTestFile(t, filepath.Join(dir, "nested", "second.yaml"), "- require: ../first.yaml\n")

	_, err := Load(filepath.Join(dir, "workflow.yaml"))
	if err == nil || !strings.Contains(err.Error(), "first.yaml -> second.yaml -> first.yaml") {
		t.Fatalf("error = %v, want require cycle", err)
	}
}

func TestLoadRejectsInvalidRequiredStepFiles(t *testing.T) {
	tests := []struct {
		name     string
		entry    string
		fragment string
		want     string
	}{
		{name: "empty path", entry: "  - require: ''\n", want: "non-empty local file path"},
		{name: "mixed fields", entry: "  - require: fragment.yaml\n    id: mixed\n", fragment: validFragment, want: "cannot be combined"},
		{name: "unknown fragment field", entry: "  - require: fragment.yaml\n", fragment: "- id: run\n  type: shell\n  unknown: true\n", want: "field unknown not found"},
		{name: "multiple documents", entry: "  - require: fragment.yaml\n", fragment: validFragment + "---\n- id: other\n  type: shell\n", want: "multiple YAML documents"},
		{name: "empty fragment", entry: "  - require: fragment.yaml\n", fragment: "[]\n", want: "at least one step"},
		{name: "scalar fragment", entry: "  - require: fragment.yaml\n", fragment: "run\n", want: "step list or an object containing steps"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			writeTestFile(t, filepath.Join(dir, "workflow.yaml"), "version: 1\nname: invalid\nsteps:\n"+tt.entry)
			if tt.fragment != "" {
				writeTestFile(t, filepath.Join(dir, "fragment.yaml"), tt.fragment)
			}
			_, err := Load(filepath.Join(dir, "workflow.yaml"))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

const validFragment = "- id: run\n  type: shell\n  with: {command: true}\n"

func writeTestFile(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}
