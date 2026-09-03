package engine_test

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/up2jj/wuko/engine"
	"github.com/up2jj/wuko/process"
	"github.com/up2jj/wuko/step"
	assertstep "github.com/up2jj/wuko/steps/assert"
	gitstep "github.com/up2jj/wuko/steps/git"
	"github.com/up2jj/wuko/workflow"
)

func TestGitCommitRendersConfigurationAndCommitsOutputs(t *testing.T) {
	dir := t.TempDir()
	runEngineGit(t, dir, "init", "--quiet")
	if err := os.WriteFile(filepath.Join(dir, "change.txt"), []byte("content\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	definition := testDefinition(t, "commit",
		workflow.Step{ID: "commit", Type: "git_commit", With: map[string]any{
			"message": "{{ .vars.message }}", "body": "{{ .vars.body }}", "paths": []any{"{{ .vars.path }}"},
			"author":   map[string]any{"name": "{{ .vars.author }}", "email": "{{ .vars.email }}"},
			"trailers": []any{map[string]any{"token": "Refs", "value": "{{ .vars.task }}"}},
		}},
		workflow.Step{ID: "check", Type: "assert", With: map[string]any{
			"expr": "steps.commit.created && steps.commit.commit != ''", "message": "commit was not created",
		}},
	)
	definition.Vars = map[string]any{
		"message": "feat: rendered commit", "body": "Rendered body.", "path": "change.txt",
		"author": "Workflow Bot", "email": "workflow@example.test", "task": "WUKO-42",
	}
	registry := newTestRegistry(t, nil)
	for _, register := range []func(*step.Registry) error{gitstep.Register, assertstep.Register} {
		if err := register(registry); err != nil {
			t.Fatal(err)
		}
	}
	state, err := engine.New(registry).Run(t.Context(), definition, engine.Options{RunDir: dir, Stdout: io.Discard, Stderr: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	commit := strings.TrimSpace(runEngineGit(t, dir, "rev-parse", "HEAD"))
	outputs := state.Steps["commit"].(map[string]any)
	if outputs["created"] != true || outputs["commit"] != commit {
		t.Fatalf("commit outputs = %#v, HEAD = %q", outputs, commit)
	}
	message := runEngineGit(t, dir, "show", "-s", "--format=%B", "HEAD")
	for _, want := range []string{"feat: rendered commit", "Rendered body.", "Refs: WUKO-42"} {
		if !strings.Contains(message, want) {
			t.Errorf("message does not contain %q:\n%s", want, message)
		}
	}
	identity := strings.TrimSpace(runEngineGit(t, dir, "show", "-s", "--format=%an|%ae|%cn|%ce", "HEAD"))
	if identity != "Workflow Bot|workflow@example.test|Workflow Bot|workflow@example.test" {
		t.Fatalf("identity = %q", identity)
	}
}

type countingGitExecutor struct{ calls int }

func (executor *countingGitExecutor) Run(context.Context, process.Options) (process.Result, error) {
	executor.calls++
	return process.Result{}, nil
}

func TestGitCommitValidationAndDryRunDoNotExecuteGit(t *testing.T) {
	definition := testDefinition(t, "commit", workflow.Step{ID: "commit", Type: "git_commit", With: map[string]any{
		"message": "{{ .vars.message }}", "paths": []any{"{{ .vars.path }}"},
		"author": map[string]any{"name": "{{ .vars.name }}", "email": "{{ .vars.email }}"},
	}})
	definition.Vars = map[string]any{"message": "feat: dry run", "path": ".", "name": "Bot", "email": "bot@example.test"}
	registry := newTestRegistry(t, nil)
	if err := gitstep.Register(registry); err != nil {
		t.Fatal(err)
	}
	executor := &countingGitExecutor{}
	workflowEngine := engine.New(registry)
	options := engine.Options{RunDir: t.TempDir(), Executor: executor, DryRun: true, Stdout: io.Discard, Stderr: io.Discard}
	if _, err := workflowEngine.Run(t.Context(), definition, options); err != nil {
		t.Fatal(err)
	}
	if executor.calls != 0 {
		t.Fatalf("dry run executed %d Git commands", executor.calls)
	}
}

func runEngineGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = dir
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return string(output)
}
