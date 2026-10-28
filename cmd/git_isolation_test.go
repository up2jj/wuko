package cmd

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/up2jj/wuko/internal/gittest"
	"github.com/up2jj/wuko/internal/steptest"
	"github.com/up2jj/wuko/internal/testfs"
	"github.com/up2jj/wuko/step"
)

func TestGitHookRunLoadsStagedManifestAndWorkflow(t *testing.T) {
	t.Parallel()
	root := gittest.Init(t)
	writeGitHookWorkflow(t, root, "check", `cat "$1"`, []string{"data.txt"})
	writeGitHookManifest(t, root, "pre-commit:\n    - workflow: check")
	dataPath := filepath.Join(root, "data.txt")
	manifestPath := filepath.Join(root, ".wuko", "git-hooks.yaml")
	workflowPath := filepath.Join(root, ".wuko", "workflows", "check.yaml")
	testfs.Write(t, dataPath, "staged data\n")
	gittest.Run(t, root, "add", ".")
	tree := gittest.Output(t, root, "write-tree")
	unstaged := map[string]string{
		dataPath: "unstaged data\n", manifestPath: "invalid: [manifest\n", workflowPath: "invalid: [workflow\n",
	}
	for path, content := range unstaged {
		testfs.Write(t, path, content)
	}
	var stdout bytes.Buffer
	command := newRootCmd(gitHookTestDependencies(t, root, &stdout, io.Discard, func(string) string { return "" }))
	command.SetArgs([]string{"git", "hook", "run", "pre-commit"})
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if stdout.String() != "staged data\n" {
		t.Fatalf("stdout = %q, want staged data", stdout.String())
	}
	for path, want := range unstaged {
		assertGitHookIsolationFile(t, path, want)
	}
	if got := gittest.Output(t, root, "write-tree"); got != tree {
		t.Fatalf("staged tree = %s, want %s", got, tree)
	}
}

func TestGitHookRunRestoresUnstagedFilesAfterFailure(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, script, wantError string
		invalidWorkflow         bool
	}{
		{name: "execution", script: "exit 7", wantError: `failed at step "run"`},
		{name: "loading", invalidWorkflow: true, wantError: "yaml:"},
		{name: "formatter", script: "printf 'formatted\n' > data.txt", wantError: "files were modified"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			root := gittest.Init(t)
			writeGitHookWorkflow(t, root, "check", test.script, nil)
			workflowPath := filepath.Join(root, ".wuko", "workflows", "check.yaml")
			if test.invalidWorkflow {
				testfs.Write(t, workflowPath, "version: 1\nsteps: [\n")
			}
			writeGitHookManifest(t, root, "pre-commit:\n    - workflow: check")
			dataPath := filepath.Join(root, "data.txt")
			testfs.Write(t, dataPath, "staged\n")
			gittest.Run(t, root, "add", ".")
			testfs.Write(t, dataPath, "unstaged\n")
			command := newRootCmd(gitHookTestDependencies(t, root, io.Discard, io.Discard, func(string) string { return "" }))
			command.SetArgs([]string{"git", "hook", "run", "pre-commit"})
			err := command.ExecuteContext(t.Context())
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("error = %v, want %q", err, test.wantError)
			}
			assertGitHookIsolationFile(t, dataPath, "unstaged\n")
		})
	}
}

func TestGitHookRunRestoresAfterCancellationAndManagedCleanup(t *testing.T) {
	t.Parallel()
	root := gittest.Init(t)
	writeGitHookManifest(t, root, "pre-commit:\n    - workflow: check")
	testfs.Write(t, filepath.Join(root, ".wuko", "workflows", "check.yaml"), `version: 1
name: check
steps:
  - id: prepare
    type: managed_isolation_check
  - id: cancel
    type: cancel_isolation_check
finally:
  - id: observe
    type: shell
    with: {script: "cat data.txt"}
`)
	dataPath := filepath.Join(root, "data.txt")
	testfs.Write(t, dataPath, "staged\n")
	gittest.Run(t, root, "add", ".")
	testfs.Write(t, dataPath, "unstaged\n")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var stdout bytes.Buffer
	deps := gitHookTestDependencies(t, root, &stdout, io.Discard, func(string) string { return "" })
	cleaned := false
	if err := deps.registry.Register("managed_isolation_check", func(map[string]any) (step.Runner, error) {
		return &gitHookIsolationCleaner{path: dataPath, cleaned: &cleaned}, nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := deps.registry.Register("cancel_isolation_check", func(map[string]any) (step.Runner, error) {
		return steptest.RunnerFunc(func(ctx context.Context, _ step.Request) (step.Result, error) {
			cancel()
			return step.Result{}, ctx.Err()
		}), nil
	}); err != nil {
		t.Fatal(err)
	}
	command := newRootCmd(deps)
	command.SetArgs([]string{"git", "hook", "run", "pre-commit"})
	if err := command.ExecuteContext(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want cancellation", err)
	}
	if !cleaned || stdout.String() != "staged\n" {
		t.Fatalf("managed cleanup = %v, finally output = %q; want both inside isolation", cleaned, stdout.String())
	}
	assertGitHookIsolationFile(t, dataPath, "unstaged\n")
}

type gitHookIsolationCleaner struct {
	path    string
	cleaned *bool
}

func (*gitHookIsolationCleaner) Run(context.Context, step.Request) (step.Result, error) {
	return step.Result{}, nil
}

func (runner *gitHookIsolationCleaner) Cleanup(ctx context.Context, _ step.Result) error {
	data, err := os.ReadFile(runner.path)
	if err != nil {
		return err
	}
	if string(data) != "staged\n" || ctx.Err() != nil {
		return errors.New("managed cleanup ran outside isolation or with a canceled context")
	}
	*runner.cleaned = true
	return nil
}

func TestGitHookRunLeavesPushWorkingDirectoryUnchanged(t *testing.T) {
	t.Parallel()
	root := gittest.Init(t)
	writeGitHookWorkflow(t, root, "check", "cat data.txt", nil)
	writeGitHookManifest(t, root, "pre-push:\n    - workflow: check")
	dataPath := filepath.Join(root, "data.txt")
	testfs.Write(t, dataPath, "staged\n")
	gittest.Run(t, root, "add", ".")
	testfs.Write(t, dataPath, "unstaged\n")
	var stdout bytes.Buffer
	command := newRootCmd(gitHookTestDependencies(t, root, &stdout, io.Discard, func(string) string { return "" }))
	command.SetArgs([]string{"git", "hook", "run", "pre-push", "--", "origin", "https://example.com/repo"})
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if stdout.String() != "unstaged\n" {
		t.Fatalf("stdout = %q, want the original worktree during push", stdout.String())
	}
}

func assertGitHookIsolationFile(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(want, string(got)); diff != "" {
		t.Fatalf("file mismatch (-want +got):\n%s", diff)
	}
}

func TestGitHookRunSkipsIsolationWithoutPreCommitBinding(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, manifest string
	}{
		{name: "no manifest"},
		{name: "other hook", manifest: "pre-push:\n    - workflow: check"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			root := gittest.Init(t)
			if test.manifest != "" {
				writeGitHookWorkflow(t, root, "check", "true", nil)
				writeGitHookManifest(t, root, test.manifest)
			}
			dataPath := filepath.Join(root, "data.txt")
			testfs.Write(t, dataPath, "staged\n")
			gittest.Run(t, root, "add", ".")
			testfs.Write(t, dataPath, "unstaged\n")
			// An intent-to-add entry would be refused by isolation; without a pre-commit
			// binding the hook must not interfere with the commit at all.
			testfs.Write(t, filepath.Join(root, "intent.txt"), "intent\n")
			gittest.Run(t, root, "add", "-N", "intent.txt")
			command := newRootCmd(gitHookTestDependencies(t, root, io.Discard, io.Discard, func(string) string { return "" }))
			command.SetArgs([]string{"git", "hook", "run", "pre-commit"})
			if err := command.ExecuteContext(t.Context()); err != nil {
				t.Fatal(err)
			}
			assertGitHookIsolationFile(t, dataPath, "unstaged\n")
		})
	}
}
