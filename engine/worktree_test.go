package engine

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/workflow"
)

func TestWorktreeRunsMultipleCommitsPublishesAndCleansUp(t *testing.T) {
	root, base := initWorktreeRepository(t)
	var observedDir string
	registry := newTestRegistry(t, map[string]step.Builder{
		"commit_changes": func(map[string]any) (step.Runner, error) {
			return runnerFunc(func(ctx context.Context, request step.Request) (step.Result, error) {
				observedDir = request.RunDir
				for index := range 2 {
					name := filepath.Join(request.RunDir, fmt.Sprintf("change-%d.txt", index))
					if err := os.WriteFile(name, []byte(fmt.Sprintf("change %d\n", index)), 0o644); err != nil {
						return step.Result{}, err
					}
					if err := testGit(request.RunDir, "add", filepath.Base(name)); err != nil {
						return step.Result{}, err
					}
					if err := testGit(request.RunDir, "-c", "user.name=Wuko Test", "-c", "user.email=wuko@example.test", "commit", "-m", fmt.Sprintf("change %d", index)); err != nil {
						return step.Result{}, err
					}
				}
				return step.Result{Outputs: map[string]any{"dir": request.RunDir}}, nil
			}), nil
		},
	})
	definition := &workflow.Definition{
		Version: 1, Name: "worktree", Dir: root,
		Steps: []workflow.Step{{
			ID: "changes",
			Worktree: &workflow.WorktreeGroup{
				Revision: "HEAD", Path: "auto",
				Publish: &workflow.WorktreePublish{Branch: "wuko/result"},
				Steps:   []workflow.Step{{ID: "commit", Type: "commit_changes", With: map[string]any{}}},
			},
		}},
	}
	state, err := New(registry).Run(t.Context(), definition, Options{RunDir: root})
	if err != nil {
		t.Fatal(err)
	}
	metadata := state.Steps["changes"].(map[string]any)
	if metadata["base_commit"] != base {
		t.Fatalf("base commit = %v, want %s", metadata["base_commit"], base)
	}
	if metadata["commit"] == base || metadata["branch"] != "wuko/result" || metadata["published"] != true {
		t.Fatalf("metadata = %#v", metadata)
	}
	if observedDir == "" || observedDir == root {
		t.Fatalf("nested run directory = %q", observedDir)
	}
	if _, err := os.Stat(observedDir); !os.IsNotExist(err) {
		t.Fatalf("worktree path error = %v, want removed path", err)
	}
	if _, err := os.Stat(filepath.Join(root, "change-0.txt")); !os.IsNotExist(err) {
		t.Fatalf("original repository was changed: %v", err)
	}
	if got := strings.TrimSpace(testGitOutput(root, "rev-list", "--count", "wuko/result")); got != "3" {
		t.Fatalf("result branch commit count = %q, want 3", got)
	}
	if got := strings.TrimSpace(testGitOutput(root, "branch", "--show-current")); got != "master" && got != "main" {
		t.Fatalf("current branch = %q", got)
	}
}

func TestWorktreePublishingFailsWithoutNewCommit(t *testing.T) {
	root, _ := initWorktreeRepository(t)
	registry := newTestRegistry(t, map[string]step.Builder{
		"noop": func(map[string]any) (step.Runner, error) {
			return runnerFunc(func(context.Context, step.Request) (step.Result, error) { return step.Result{}, nil }), nil
		},
	})
	definition := &workflow.Definition{
		Version: 1, Name: "worktree-noop", Dir: root,
		Steps: []workflow.Step{{ID: "changes", Worktree: &workflow.WorktreeGroup{
			Revision: "HEAD", Path: "auto", Publish: &workflow.WorktreePublish{Branch: "wuko/noop"},
			Steps: []workflow.Step{{ID: "noop", Type: "noop", With: map[string]any{}}},
		}}},
	}
	if _, err := New(registry).Run(t.Context(), definition, Options{RunDir: root}); err == nil || !strings.Contains(err.Error(), "created no commit") {
		t.Fatalf("error = %v, want no-commit error", err)
	}
	if strings.Contains(testGitOutput(root, "branch", "--list", "wuko/noop"), "wuko/noop") {
		t.Fatal("no-op branch was created")
	}
}

func TestNestedWorktreeDefersRunOnceBeforeRemoval(t *testing.T) {
	root, _ := initWorktreeRepository(t)
	var cleanupRuns int
	registry := newTestRegistry(t, map[string]step.Builder{
		"record": func(map[string]any) (step.Runner, error) {
			return runnerFunc(func(_ context.Context, request step.Request) (step.Result, error) {
				if request.StepID == "cleanup" {
					cleanupRuns++
				}
				return step.Result{}, nil
			}), nil
		},
	})
	definition := &workflow.Definition{
		Version: 1, Name: "nested-worktree-defer", Dir: root,
		Steps: []workflow.Step{{ID: "outer", Worktree: &workflow.WorktreeGroup{
			Revision: "HEAD", Path: "auto",
			Steps: []workflow.Step{{ID: "inner", Worktree: &workflow.WorktreeGroup{
				Revision: "HEAD", Path: "auto",
				Steps: []workflow.Step{{ID: "main", Type: "record", With: map[string]any{}, Defer: []workflow.Step{{ID: "cleanup", Type: "record", With: map[string]any{}}}}},
			}}},
		}}},
	}
	if _, err := New(registry).Run(t.Context(), definition, Options{RunDir: root}); err != nil {
		t.Fatal(err)
	}
	if cleanupRuns != 1 {
		t.Fatalf("nested worktree defer runs = %d, want 1", cleanupRuns)
	}
}

func initWorktreeRepository(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	if err := testGit(root, "init", "-q"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("initial\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := testGit(root, "add", "README.md"); err != nil {
		t.Fatal(err)
	}
	if err := testGit(root, "-c", "user.name=Wuko Test", "-c", "user.email=wuko@example.test", "commit", "-qm", "initial"); err != nil {
		t.Fatal(err)
	}
	return root, strings.TrimSpace(testGitOutput(root, "rev-parse", "HEAD"))
}

func testGit(dir string, args ...string) error {
	command := exec.Command("git", args...)
	command.Dir = dir
	if output, err := command.CombinedOutput(); err != nil {
		return fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return nil
}

func testGitOutput(dir string, args ...string) string {
	command := exec.Command("git", args...)
	command.Dir = dir
	output, err := command.Output()
	if err != nil {
		panic(fmt.Sprintf("git %s: %v", strings.Join(args, " "), err))
	}
	return string(output)
}
