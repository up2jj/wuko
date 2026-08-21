package workflow

import (
	"net/http"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadConditionalBlockAndSingleStepCondition(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workflow.yaml")
	writeTestFile(t, path, `version: 1
name: conditional
vars: {enabled: true}
steps:
  - if: vars.enabled
    steps:
      - {id: build, type: shell}
      - {id: deploy, type: shell, if: steps.build.exit_code == 0}
`)
	definition, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	block := definition.Steps[0]
	if !block.IsConditionalBlock() || block.If != "vars.enabled" || len(block.Steps) != 2 {
		t.Fatalf("conditional block = %#v", block)
	}
	if block.Steps[1].If != "steps.build.exit_code == 0" || block.Steps[1].IsConditionalBlock() {
		t.Fatalf("single-step condition = %#v", block.Steps[1])
	}
}

func TestConditionalBlockValidation(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{name: "null steps", body: "  - if: true\n    steps: null\n", want: "steps must be a list"},
		{name: "missing if", body: "  - steps: [{id: run, type: shell}]\n", want: "must set if"},
		{name: "empty", body: "  - if: true\n    steps: []\n", want: "at least one step"},
		{name: "mixed fields", body: "  - id: block\n    if: true\n    steps: [{id: run, type: shell}]\n", want: "cannot be combined"},
		{name: "nested", body: "  - if: true\n    steps:\n      - if: true\n        steps: [{id: run, type: shell}]\n", want: "nested conditional"},
		{name: "inside concurrent", body: "  - concurrent:\n      steps:\n        - if: true\n          steps: [{id: run, type: shell}]\n        - {id: other, type: shell}\n", want: "inside concurrent"},
		{name: "duplicate outer id", body: "  - {id: run, type: shell}\n  - if: true\n    steps: [{id: run, type: shell}]\n", want: `duplicate step id "run"`},
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

func TestConditionalBlocksFollowInheritedControlNestingRules(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workflow.yaml")
	writeTestFile(t, path, `version: 1
name: controls
vars: {items: [one]}
steps:
  - id: loop
    foreach:
      items: vars.items
      steps:
        - if: true
          steps:
            - concurrent:
                steps:
                  - {id: first, type: shell}
                  - {id: second, type: shell}
`)
	if _, err := Load(path); err != nil {
		t.Fatal(err)
	}

	writeTestFile(t, path, `version: 1
name: invalid
vars: {items: [one]}
steps:
  - id: loop
    foreach:
      items: vars.items
      steps:
        - if: true
          steps:
            - id: nested
              matrix:
                axes: {os: [linux]}
                steps: [{id: run, type: shell}]
`)
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "nested matrix") {
		t.Fatalf("nested control error = %v", err)
	}
}

func TestConditionalBlockExpandsRequiredStepsResolvesActionsAndPreservesLocations(t *testing.T) {
	dir := t.TempDir()
	fragment := filepath.Join(dir, "fragment.yaml")
	writeTestFile(t, filepath.Join(dir, "local.yaml"), "- {id: local, type: shell}\n")
	writeTestFile(t, fragment, `- if: vars.enabled
  steps:
    - id: remote
      uses: https://actions.example.test/build
      with: {target: linux}
    - require: local.yaml
`)
	workflowPath := filepath.Join(dir, "workflow.yaml")
	writeTestFile(t, workflowPath, `version: 1
name: caller
vars: {enabled: false}
steps:
  - require: fragment.yaml
`)
	client := testHTTPClient(func(*http.Request) (*http.Response, error) {
		return testResponse(http.StatusOK, []byte(validAction)), nil
	})
	definition, err := NewLoader(client).Load(t.Context(), workflowPath, LoadOptions{RunDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	block := definition.Steps[0]
	if len(block.Steps) != 2 || block.Steps[0].Action == nil || block.Steps[1].ID != "local" {
		t.Fatalf("expanded block = %#v", block)
	}
	if block.Location.Source != fragment || block.Steps[0].Location.Source != fragment || block.Steps[1].Location.Source != filepath.Join(dir, "local.yaml") {
		t.Fatalf("locations = %#v, %#v, %#v", block.Location, block.Steps[0].Location, block.Steps[1].Location)
	}
}
