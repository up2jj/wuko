package workflow

import (
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/up2jj/wuko/internal/testfs"
	"github.com/up2jj/wuko/internal/transporttest"
)

func TestLoadConditionalBlockAndSingleStepCondition(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "workflow.yaml")
	testfs.Write(t, path, `version: 1
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

func TestLoadNestedConditionalBlocks(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "workflow.yaml")
	testfs.Write(t, path, `version: 1
name: nested
vars: {skip_check: false}
steps:
  - if: '!vars.skip_check'
    steps:
      - {id: check, type: shell}
      - if: steps.check.exit_code != 0
        steps:
          - {id: offer, type: shell}
          - {id: install, type: shell}
`)
	definition, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	outer := definition.Steps[0]
	if !outer.IsConditionalBlock() || len(outer.Steps) != 2 {
		t.Fatalf("outer block = %#v", outer)
	}
	inner := outer.Steps[1]
	if !inner.IsConditionalBlock() || inner.If != "steps.check.exit_code != 0" || len(inner.Steps) != 2 {
		t.Fatalf("inner block = %#v", inner)
	}
	if inner.Location.Line != 8 || inner.Steps[1].Location.Source != path || inner.Steps[1].Location.Line != 11 {
		t.Fatalf("locations = %#v, %#v", inner.Location, inner.Steps[1].Location)
	}
}

func TestConditionalBlockValidation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		body string
		want string
	}{
		{name: "null steps", body: "  - if: true\n    steps: null\n", want: "steps must be a list"},
		{name: "missing if", body: "  - steps: [{id: run, type: shell}]\n", want: "must set if"},
		{name: "empty", body: "  - if: true\n    steps: []\n", want: "at least one step"},
		{name: "mixed fields", body: "  - id: block\n    if: true\n    steps: [{id: run, type: shell}]\n", want: "cannot be combined"},
		{name: "nested duplicate id", body: "  - if: true\n    steps:\n      - {id: run, type: shell}\n      - if: true\n        steps: [{id: run, type: shell}]\n", want: `duplicate step id "run"`},
		{name: "nested inside concurrent", body: "  - if: true\n    steps:\n      - concurrent:\n          steps:\n            - if: true\n              steps: [{id: run, type: shell}]\n            - {id: other, type: shell}\n", want: "inside concurrent"},
		{name: "inside concurrent", body: "  - concurrent:\n      steps:\n        - if: true\n          steps: [{id: run, type: shell}]\n        - {id: other, type: shell}\n", want: "inside concurrent"},
		{name: "duplicate outer id", body: "  - {id: run, type: shell}\n  - if: true\n    steps: [{id: run, type: shell}]\n", want: `duplicate step id "run"`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "workflow.yaml")
			testfs.Write(t, path, "version: 1\nname: invalid\nsteps:\n"+test.body)
			_, err := Load(path)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestConditionalBlocksFollowInheritedControlNestingRules(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "workflow.yaml")
	testfs.Write(t, path, `version: 1
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

	testfs.Write(t, path, `version: 1
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
	t.Parallel()
	dir := t.TempDir()
	fragment := filepath.Join(dir, "fragment.yaml")
	testfs.Write(t, filepath.Join(dir, "local.yaml"), "- {id: local, type: shell}\n")
	testfs.Write(t, fragment, `- if: vars.enabled
  steps:
    - id: remote
      uses: https://actions.example.test/build
      with: {target: linux}
    - require: local.yaml
`)
	workflowPath := filepath.Join(dir, "workflow.yaml")
	testfs.Write(t, workflowPath, `version: 1
name: caller
vars: {enabled: false}
steps:
  - require: fragment.yaml
`)
	client := transporttest.Client(func(*http.Request) (*http.Response, error) {
		return transporttest.Response(http.StatusOK, []byte(validAction)), nil
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
