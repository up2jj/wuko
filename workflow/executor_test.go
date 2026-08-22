package workflow

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadExecutorBlockWithFinally(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workflow.yaml")
	writeTestFile(t, path, `version: 1
name: mixed
steps:
  - executor:
      type: docker
      with: {image: golang:1.26}
    steps:
      - {id: build, type: shell, with: {command: go}}
    finally:
      - {id: clean, type: shell, with: {command: go}}
  - {id: package, type: shell, with: {command: tar}}
`)
	definition, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	block := definition.Steps[0]
	if !block.IsExecutorBlock() || block.Executor.Type != "docker" || block.Executor.With["image"] != "golang:1.26" {
		t.Fatalf("executor block = %#v", block)
	}
	if block.Steps[0].ID != "build" || block.Finally[0].ID != "clean" || definition.Steps[1].ID != "package" {
		t.Fatalf("executor children = %#v/%#v", block.Steps, block.Finally)
	}
}

func TestExecutorBlockSchemaRestrictions(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{name: "missing type", body: "  - executor: {with: {image: alpine}}\n    steps: [{id: run, type: shell}]\n", want: "executor type is required"},
		{name: "missing steps", body: "  - executor: {type: docker, with: {image: alpine}}\n    steps: []\n", want: "at least one step"},
		{name: "empty working directory", body: "  - executor: {type: docker, with: {image: alpine}}\n    working_directory: \"\"\n    steps: [{id: run, type: shell}]\n", want: "cannot be combined with other step fields"},
		{name: "nested executor", body: "  - executor: {type: docker, with: {image: alpine}}\n    steps:\n      - executor: {type: docker, with: {image: alpine}}\n        steps: [{id: run, type: shell}]\n", want: "only supported in sequential workflow scopes"},
		{name: "parallel fanout", body: "  - executor: {type: docker, with: {image: alpine}}\n    steps:\n      - id: loop\n        foreach: {items: '[1]', max_concurrency: 2, steps: [{id: run, type: shell}]}\n", want: "max_concurrency 1"},
		{name: "parallel batch", body: "  - executor: {type: docker, with: {image: alpine}}\n    steps:\n      - id: loop\n        batch: {items: '[1, 2]', size: 1, max_concurrency: 2, steps: [{id: run, type: shell}]}\n", want: "max_concurrency 1"},
		{name: "return in cleanup", body: "  - executor: {type: docker, with: {image: alpine}}\n    steps: [{id: run, type: shell}]\n    finally:\n      - return: {outputs: {result: '\"done\"'}}\n", want: "not supported inside finally"},
		{name: "action", body: "  - executor: {type: docker, with: {image: alpine}}\n    steps: [{id: run, uses: 'https://example.test/action'}]\n", want: "actions are not supported"},
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
