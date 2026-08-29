package workflow

import (
	"strings"
	"testing"
)

func TestLoadEnvironmentBlock(t *testing.T) {
	path := t.TempDir() + "/workflow.yml"
	writeTestFile(t, path, `version: 1
name: scoped-env
steps:
  - env:
      GOOS: linux
      CGO_ENABLED: "0"
    steps:
      - env: {GOARCH: arm64}
        steps:
          - {id: build, type: shell, with: {command: go}}
`)
	definition, err := NewLoader(nil).Load(t.Context(), path, LoadOptions{RunDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	block := definition.Steps[0]
	if !block.IsEnvironmentBlock() || block.IsConditionalBlock() || block.Env["GOOS"] != "linux" || block.Env["CGO_ENABLED"] != "0" {
		t.Fatalf("env block = %#v", block)
	}
	nested := block.Steps[0]
	if !nested.IsEnvironmentBlock() || nested.Env["GOARCH"] != "arm64" || nested.Steps[0].ID != "build" {
		t.Fatalf("nested env block = %#v", nested)
	}
}

func TestEnvironmentBlockValidation(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{name: "non object", body: "  - env: value\n    steps: [{id: run, type: shell}]\n", want: "environment must be an object"},
		{name: "numeric value", body: "  - env: {PORT: 8080}\n    steps: [{id: run, type: shell}]\n", want: "environment names and values must be strings"},
		{name: "empty environment", body: "  - env: {}\n    steps: [{id: run, type: shell}]\n", want: "at least one variable"},
		{name: "invalid name", body: "  - env: {'NOT VALID': value}\n    steps: [{id: run, type: shell}]\n", want: "invalid environment name"},
		{name: "empty steps", body: "  - env: {MODE: test}\n    steps: []\n", want: "at least one step"},
		{name: "mixed id", body: "  - id: scope\n    env: {MODE: test}\n    steps: [{id: run, type: shell}]\n", want: "cannot be combined"},
		{name: "mixed directory", body: "  - env: {MODE: test}\n    working_directory: .\n    steps: [{id: run, type: shell}]\n", want: "cannot be combined"},
		{name: "step level env", body: "  - id: run\n    type: shell\n    env: {MODE: test}\n    with: {command: go}\n", want: "cannot be combined"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := t.TempDir() + "/workflow.yml"
			writeTestFile(t, path, "version: 1\nname: invalid\nsteps:\n"+test.body)
			_, err := NewLoader(nil).Load(t.Context(), path, LoadOptions{RunDir: t.TempDir()})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestEnvironmentBlockExpandsRequiredSteps(t *testing.T) {
	dir := t.TempDir()
	fragment := dir + "/steps.yml"
	writeTestFile(t, fragment, `- env: {MODE: scoped}
  steps:
    - {id: build, type: shell, with: {command: go}}
`)
	workflowPath := dir + "/workflow.yml"
	writeTestFile(t, workflowPath, "version: 1\nname: required-env\nsteps:\n  - require: steps.yml\n")
	definition, err := NewLoader(nil).Load(t.Context(), workflowPath, LoadOptions{RunDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if block := definition.Steps[0]; !block.IsEnvironmentBlock() || block.Steps[0].ID != "build" {
		t.Fatalf("required env block = %#v", block)
	}
}
