package git

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/up2jj/wuko/process"
	"github.com/up2jj/wuko/step"
)

type recordingExecutor struct {
	options process.Options
	result  process.Result
	err     error
}

func (executor *recordingExecutor) Run(_ context.Context, options process.Options) (process.Result, error) {
	executor.options = options
	return executor.result, executor.err
}

func TestNewValidatesConfiguration(t *testing.T) {
	tests := []struct {
		name    string
		builder step.Builder
		raw     map[string]any
		want    string
	}{
		{"clean unknown field", NewClean, map[string]any{"branch": "main"}, "field"},
		{"branch missing operation", NewBranch, map[string]any{"branch": "main", "exists": true}, "operation is required"},
		{"branch unsupported operation", NewBranch, map[string]any{"operation": "create", "branch": "main", "exists": true}, "operation must be"},
		{"branch missing name", NewBranch, map[string]any{"operation": "assert", "exists": true}, "branch is required"},
		{"branch missing exists", NewBranch, map[string]any{"operation": "assert", "branch": "main"}, "exists is required"},
		{"branch blank", NewBranch, map[string]any{"operation": "assert", "branch": "  ", "exists": true}, "branch is required"},
		{"branch unknown field", NewBranch, map[string]any{"operation": "assert", "branch": "main", "exists": true, "unknown": true}, "field"},
		{"remote missing branch", NewRemoteBranch, map[string]any{"exists": true}, "branch is required"},
		{"remote missing exists", NewRemoteBranch, map[string]any{"branch": "main"}, "exists is required"},
		{"remote blank remote", NewRemoteBranch, map[string]any{"branch": "main", "remote": "  ", "exists": true}, "remote must not be blank"},
		{"remote unknown field", NewRemoteBranch, map[string]any{"branch": "main", "exists": true, "unknown": true}, "field"},
		{"branch name missing", NewBranchName, map[string]any{}, "name is required"},
		{"branch name blank", NewBranchName, map[string]any{"name": "  "}, "name is required"},
		{"branch name unknown field", NewBranchName, map[string]any{"name": "main", "unknown": true}, "field"},
		{"on branch missing", NewOnBranch, map[string]any{}, "branch is required"},
		{"on branch blank", NewOnBranch, map[string]any{"branch": "  "}, "branch is required"},
		{"on branch unknown field", NewOnBranch, map[string]any{"branch": "main", "unknown": true}, "field"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := tt.builder(tt.raw); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("New(%#v) error = %v, want %q", tt.raw, err, tt.want)
			}
		})
	}
}

func TestSuccessfulAssertionsReturnEmptyResults(t *testing.T) {
	tests := []struct {
		name    string
		builder step.Builder
		raw     map[string]any
		result  process.Result
	}{
		{"clean", NewClean, map[string]any{}, process.Result{}},
		{"branch", NewBranch, map[string]any{"operation": "assert", "branch": "main", "exists": true}, process.Result{}},
		{"remote branch", NewRemoteBranch, map[string]any{"branch": "main", "exists": true}, process.Result{Stdout: "refs/remotes/origin/main\n"}},
		{"branch name", NewBranchName, map[string]any{"name": "main"}, process.Result{}},
		{"on branch", NewOnBranch, map[string]any{"branch": "main"}, process.Result{Stdout: "main\n"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner, err := tt.builder(tt.raw)
			if err != nil {
				t.Fatal(err)
			}
			executor := &recordingExecutor{result: tt.result}
			result, err := runner.Run(t.Context(), step.Request{Executor: executor, RunDir: "/repo"})
			if err != nil {
				t.Fatal(err)
			}
			if result.Outputs != nil || result.Variables != nil {
				t.Fatalf("result = %#v, want empty result", result)
			}
			if executor.options.Command != "git" || executor.options.Dir != "/repo" {
				t.Fatalf("process options = %#v", executor.options)
			}
		})
	}
}

func TestGitCommandsUseExecutorEnvironmentAndArguments(t *testing.T) {
	executor := &recordingExecutor{}
	runner, err := NewBranch(map[string]any{"operation": "assert", "branch": "main", "exists": true})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(t.Context(), step.Request{
		Executor: executor, RunDir: "/workspace", Env: map[string]string{"MODE": "test"},
		Attempt: 2, MaxAttempts: 3, OperationID: "git-branch",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(executor.options.Args, []string{"show-ref", "--verify", "--quiet", "refs/heads/main"}) {
		t.Fatalf("args = %#v", executor.options.Args)
	}
	if executor.options.Env["MODE"] != "test" || executor.options.Env[step.AttemptEnv] != "2" || executor.options.Env[step.OperationIDEnv] != "git-branch" {
		t.Fatalf("environment = %#v", executor.options.Env)
	}
	if _, ok := runner.(step.ExecutorAware); !ok {
		t.Fatalf("runner does not implement step.ExecutorAware")
	}
	if result.Outputs != nil || result.Variables != nil {
		t.Fatalf("result = %#v, want empty result", result)
	}
}

func TestBranchAssertionsClassifyMissingReferences(t *testing.T) {
	for _, tt := range []struct {
		name       string
		existsCode int
		expects    bool
		wantError  string
	}{
		{"present expected", 0, true, ""},
		{"missing expected", 1, false, ""},
		{"present unexpected", 0, false, "already exists"},
		{"missing unexpected", 1, true, "does not exist"},
		{"unexpected git failure", 128, true, "checking local branch"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			runner, err := NewBranch(map[string]any{"operation": "assert", "branch": "main", "exists": tt.expects})
			if err != nil {
				t.Fatal(err)
			}
			executor := &recordingExecutor{err: &process.ExitError{Command: "git", Code: tt.existsCode}}
			if tt.existsCode == 0 {
				executor.err = nil
			}
			_, err = runner.Run(t.Context(), step.Request{Executor: executor})
			if tt.wantError == "" {
				if err != nil {
					t.Fatalf("Run() error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("Run() error = %v, want %q", err, tt.wantError)
			}
		})
	}
}

func TestRemoteBranchAssertionsMatchAnyOrNamedRemote(t *testing.T) {
	for _, tt := range []struct {
		name       string
		remote     string
		branch     string
		expects    bool
		wantError  string
		remoteRefs string
	}{
		{"any remote present", "", "release", true, "", "refs/remotes/origin/release\n"},
		{"named remote present", "upstream", "feature/release", true, "", "refs/remotes/upstream/feature/release\n"},
		{"named remote absent", "origin", "release", false, "", "refs/remotes/upstream/release\n"},
		{"unexpected present", "origin", "release", false, "already exists", "refs/remotes/origin/release\n"},
		{"unexpected absent", "origin", "release", true, "does not exist", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			raw := map[string]any{"branch": tt.branch, "exists": tt.expects}
			if tt.remote != "" {
				raw["remote"] = tt.remote
			}
			runner, err := NewRemoteBranch(raw)
			if err != nil {
				t.Fatal(err)
			}
			_, err = runner.Run(t.Context(), step.Request{Executor: &recordingExecutor{result: process.Result{Stdout: tt.remoteRefs}}})
			if tt.wantError == "" {
				if err != nil {
					t.Fatalf("Run() error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("Run() error = %v, want %q", err, tt.wantError)
			}
		})
	}
}

func TestBranchNameAndCurrentBranchAssertions(t *testing.T) {
	valid, err := NewBranchName(map[string]any{"name": "feature/release"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := valid.Run(t.Context(), step.Request{Executor: &recordingExecutor{}}); err != nil {
		t.Fatal(err)
	}

	invalid, err := NewBranchName(map[string]any{"name": "bad branch"})
	if err != nil {
		t.Fatal(err)
	}
	executor := &recordingExecutor{err: &process.ExitError{Command: "git", Code: 1}}
	if _, err := invalid.Run(t.Context(), step.Request{Executor: executor}); err == nil || !strings.Contains(err.Error(), "not a valid Git branch name") {
		t.Fatalf("invalid branch error = %v", err)
	}

	onBranch, err := NewOnBranch(map[string]any{"branch": "main"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := onBranch.Run(t.Context(), step.Request{Executor: &recordingExecutor{result: process.Result{Stdout: "main\n"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := onBranch.Run(t.Context(), step.Request{Executor: &recordingExecutor{result: process.Result{Stdout: "develop\n"}}}); err == nil || !strings.Contains(err.Error(), "want \"main\"") {
		t.Fatalf("different branch error = %v", err)
	}
	if _, err := onBranch.Run(t.Context(), step.Request{Executor: &recordingExecutor{}}); err == nil || !strings.Contains(err.Error(), "detached HEAD") {
		t.Fatalf("detached branch error = %v", err)
	}
}

func TestGitAssertionsPropagateCancellationAndCommandFailures(t *testing.T) {
	runner, err := NewClean(map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := runner.Run(ctx, step.Request{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error = %v", err)
	}

	runner, err = NewClean(map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.Run(t.Context(), step.Request{Executor: &recordingExecutor{
		result: process.Result{Stderr: "not a repository\n"},
		err:    errors.New("git failed"),
	}})
	if err == nil || !strings.Contains(err.Error(), "checking Git working tree: not a repository") {
		t.Fatalf("command failure = %v", err)
	}
}

func TestGitAssertionsAgainstRepositories(t *testing.T) {
	dir := initGitRepository(t)

	clean, err := NewClean(map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := clean.Run(t.Context(), step.Request{RunDir: dir}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "untracked.txt"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := clean.Run(t.Context(), step.Request{RunDir: dir}); err == nil || !strings.Contains(err.Error(), "uncommitted changes") {
		t.Fatalf("dirty tree error = %v", err)
	}

	branch, err := NewBranch(map[string]any{"operation": "assert", "branch": "topic", "exists": true})
	if err != nil {
		t.Fatal(err)
	}
	runGitTest(t, dir, "branch", "topic")
	if _, err := branch.Run(t.Context(), step.Request{RunDir: dir}); err != nil {
		t.Fatal(err)
	}

	runGitTest(t, dir, "update-ref", "refs/remotes/origin/topic", "HEAD")
	remote, err := NewRemoteBranch(map[string]any{"branch": "topic", "remote": "origin", "exists": true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := remote.Run(t.Context(), step.Request{RunDir: dir}); err != nil {
		t.Fatal(err)
	}
}

func initGitRepository(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runGitTest(t, dir, "init", "--quiet")
	runGitTest(t, dir, "config", "user.name", "Wuko Test")
	runGitTest(t, dir, "config", "user.email", "wuko@example.test")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, dir, "add", "README.md")
	runGitTest(t, dir, "commit", "--quiet", "-m", "initial")
	return dir
}

func runGitTest(t *testing.T, dir string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = dir
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return string(output)
}

func TestRemoteBranchExistsDoesNotMatchRemoteHead(t *testing.T) {
	if remoteBranchExists("refs/remotes/HEAD\n", "", "HEAD") {
		t.Fatal("remote HEAD matched as a branch without a remote component")
	}
}
