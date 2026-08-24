package workflow

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadWorktreeBlock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workflow.yaml")
	writeTestFile(t, path, `version: 1
name: worktree
steps:
  - id: changes
    worktree:
      revision: HEAD
      publish:
        branch: wuko/result
      steps:
        - id: inspect
          type: shell
`)
	definition, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	block := definition.Steps[0]
	if !block.IsWorktreeBlock() || block.ID != "changes" {
		t.Fatalf("worktree block = %#v", block)
	}
	if block.Worktree.Revision != "HEAD" || block.Worktree.Path != "auto" || block.Worktree.Publish.Branch != "wuko/result" {
		t.Fatalf("worktree configuration = %#v", block.Worktree)
	}
	if len(block.Worktree.Steps) != 1 || block.Worktree.Steps[0].ID != "inspect" {
		t.Fatalf("worktree children = %#v", block.Worktree.Steps)
	}
}

func TestWorktreeBlockValidation(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{name: "missing id", body: "  - worktree: {revision: HEAD, steps: [{id: run, type: shell}]}\n", want: "invalid id"},
		{name: "missing revision", body: "  - id: run\n    worktree: {steps: [{id: child, type: shell}]}\n", want: "revision must be a non-empty"},
		{name: "empty steps", body: "  - id: run\n    worktree: {revision: HEAD, steps: []}\n", want: "at least one step"},
		{name: "mixed working directory", body: "  - id: run\n    working_directory: .\n    worktree: {revision: HEAD, steps: [{id: child, type: shell}]}\n", want: "worktree block cannot be combined"},
		{name: "unknown field", body: "  - id: run\n    worktree: {revision: HEAD, keep: true, steps: [{id: child, type: shell}]}\n", want: "field keep not found"},
		{name: "duplicate id", body: "  - id: run\n    type: shell\n  - id: run\n    worktree: {revision: HEAD, steps: [{id: child, type: shell}]}\n", want: `duplicate step id "run"`},
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

func TestWorktreeChildSequences(t *testing.T) {
	step := Step{ID: "child", Type: "shell"}
	block := Step{ID: "changes", Worktree: &WorktreeGroup{Revision: "HEAD", Path: "auto", Steps: []Step{step}}}
	children := block.ChildSequences()
	if len(children) != 1 || children[0].Role != ChildSteps || len(children[0].Steps) != 1 || children[0].Steps[0].ID != "child" {
		t.Fatalf("children = %#v", children)
	}
}
