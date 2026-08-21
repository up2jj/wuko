package workflow

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadWorkingDirectoryBlock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workflow.yaml")
	writeTestFile(t, path, `version: 1
name: scoped
steps:
  - working_directory: "{{ .vars.project }}"
    steps:
      - id: build
        type: shell
      - working_directory: nested
        steps:
          - id: test
            type: shell
`)
	definition, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	block := definition.Steps[0]
	if !block.IsWorkingDirectoryBlock() || block.IsConditionalBlock() || block.WorkingDirectory != "{{ .vars.project }}" {
		t.Fatalf("working_directory block = %#v", block)
	}
	if nested := block.Steps[1]; !nested.IsWorkingDirectoryBlock() || nested.WorkingDirectory != "nested" || nested.Steps[0].ID != "test" {
		t.Fatalf("nested block = %#v", nested)
	}
}

func TestWorkingDirectoryBlockValidation(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{name: "blank", body: "  - working_directory: ''\n    steps: [{id: run, type: shell}]\n", want: "non-empty path"},
		{name: "non-string", body: "  - working_directory: 42\n    steps: [{id: run, type: shell}]\n", want: "must be a string path"},
		{name: "empty", body: "  - working_directory: build\n    steps: []\n", want: "at least one step"},
		{name: "mixed id", body: "  - id: scope\n    working_directory: build\n    steps: [{id: run, type: shell}]\n", want: "cannot be combined"},
		{name: "mixed if", body: "  - if: true\n    working_directory: build\n    steps: [{id: run, type: shell}]\n", want: "cannot be combined"},
		{name: "duplicate id", body: "  - {id: run, type: shell}\n  - working_directory: build\n    steps: [{id: run, type: shell}]\n", want: `duplicate step id "run"`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "workflow.yaml")
			writeTestFile(t, path, "version: 1\nname: invalid\nsteps:\n"+test.body)
			_, err := Load(path)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestWorkingDirectoryBlockPreservesInheritedNestingRules(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workflow.yaml")
	writeTestFile(t, path, `version: 1
name: invalid
steps:
  - concurrent:
      steps:
        - working_directory: backend
          steps:
            - if: true
              steps: [{id: test, type: shell}]
        - {id: lint, type: shell}
`)
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "conditional blocks are not supported inside concurrent groups") {
		t.Fatalf("nested restriction error = %v", err)
	}
}

func TestWorkingDirectoryBlockExpandsRequiredStepsAndPreservesLocations(t *testing.T) {
	dir := t.TempDir()
	fragment := filepath.Join(dir, "fragment.yaml")
	writeTestFile(t, fragment, `- working_directory: backend
  steps:
    - require: nested.yaml
`)
	nested := filepath.Join(dir, "nested.yaml")
	writeTestFile(t, nested, `- id: build
  type: shell
`)
	workflowPath := filepath.Join(dir, "workflow.yaml")
	writeTestFile(t, workflowPath, `version: 1
name: required
steps:
  - require: fragment.yaml
`)
	definition, err := Load(workflowPath)
	if err != nil {
		t.Fatal(err)
	}
	block := definition.Steps[0]
	if !block.IsWorkingDirectoryBlock() || len(block.Steps) != 1 || block.Steps[0].ID != "build" {
		t.Fatalf("expanded block = %#v", block)
	}
	if block.Location.Source != fragment || block.Steps[0].Location.Source != nested {
		t.Fatalf("locations = %#v, %#v", block.Location, block.Steps[0].Location)
	}
}
