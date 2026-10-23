package git

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/up2jj/wuko/internal/gittest"
	"github.com/up2jj/wuko/internal/steptest"
	"github.com/up2jj/wuko/internal/testfs"
	"github.com/up2jj/wuko/process"
	"github.com/up2jj/wuko/process/processtest"
	"github.com/up2jj/wuko/step"
)

func TestNewValidatesConfiguration(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
			executor := &processtest.Executor{Reply: processtest.Reply{Result: tt.result}}
			result, err := runner.Run(t.Context(), step.Request{Executor: executor, RunDir: "/repo"})
			if err != nil {
				t.Fatal(err)
			}
			if result.Outputs != nil || result.Variables != nil {
				t.Fatalf("result = %#v, want empty result", result)
			}
			if executor.Last().Command != "git" || executor.Last().Dir != "/repo" {
				t.Fatalf("process options = %#v", executor.Last())
			}
		})
	}
}

func TestGitCommandsUseExecutorEnvironmentAndArguments(t *testing.T) {
	t.Parallel()
	executor := &processtest.Executor{}
	runner := steptest.Build(t, NewBranch, map[string]any{"operation": "assert", "branch": "main", "exists": true})
	result, err := runner.Run(t.Context(), step.Request{
		Executor: executor, RunDir: "/workspace", Env: map[string]string{"MODE": "test"},
		Attempt: 2, MaxAttempts: 3, OperationID: "git-branch",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(executor.Last().Args, []string{"show-ref", "--verify", "--quiet", "refs/heads/main"}) {
		t.Fatalf("args = %#v", executor.Last().Args)
	}
	if executor.Last().Env["MODE"] != "test" || executor.Last().Env[step.AttemptEnv] != "2" || executor.Last().Env[step.OperationIDEnv] != "git-branch" {
		t.Fatalf("environment = %#v", executor.Last().Env)
	}
	if _, ok := runner.(step.ExecutorAware); !ok {
		t.Fatalf("runner does not implement step.ExecutorAware")
	}
	if result.Outputs != nil || result.Variables != nil {
		t.Fatalf("result = %#v, want empty result", result)
	}
}

func TestBranchAssertionsClassifyMissingReferences(t *testing.T) {
	t.Parallel()
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
			runner := steptest.Build(t, NewBranch, map[string]any{"operation": "assert", "branch": "main", "exists": tt.expects})
			executor := &processtest.Executor{Reply: processtest.Reply{Err: &process.ExitError{Command: "git", Code: tt.existsCode}}}
			if tt.existsCode == 0 {
				executor.Reply.Err = nil
			}
			_, err := runner.Run(t.Context(), step.Request{Executor: executor})
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
	t.Parallel()
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
			runner := steptest.Build(t, NewRemoteBranch, raw)
			_, err := runner.Run(t.Context(), step.Request{Executor: &processtest.Executor{Reply: processtest.Reply{Result: process.Result{Stdout: tt.remoteRefs}}}})
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
	t.Parallel()
	valid := steptest.Build(t, NewBranchName, map[string]any{"name": "feature/release"})
	if _, err := valid.Run(t.Context(), step.Request{Executor: &processtest.Executor{}}); err != nil {
		t.Fatal(err)
	}

	invalid := steptest.Build(t, NewBranchName, map[string]any{"name": "bad branch"})
	executor := &processtest.Executor{Reply: processtest.Reply{Err: &process.ExitError{Command: "git", Code: 1}}}
	if _, err := invalid.Run(t.Context(), step.Request{Executor: executor}); err == nil || !strings.Contains(err.Error(), "not a valid Git branch name") {
		t.Fatalf("invalid branch error = %v", err)
	}

	onBranch := steptest.Build(t, NewOnBranch, map[string]any{"branch": "main"})
	if _, err := onBranch.Run(t.Context(), step.Request{Executor: &processtest.Executor{Reply: processtest.Reply{Result: process.Result{Stdout: "main\n"}}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := onBranch.Run(t.Context(), step.Request{Executor: &processtest.Executor{Reply: processtest.Reply{Result: process.Result{Stdout: "develop\n"}}}}); err == nil || !strings.Contains(err.Error(), "want \"main\"") {
		t.Fatalf("different branch error = %v", err)
	}
	if _, err := onBranch.Run(t.Context(), step.Request{Executor: &processtest.Executor{}}); err == nil || !strings.Contains(err.Error(), "detached HEAD") {
		t.Fatalf("detached branch error = %v", err)
	}
}

func TestGitAssertionsPropagateCancellationAndCommandFailures(t *testing.T) {
	t.Parallel()
	runner := steptest.Build(t, NewClean, map[string]any{})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := runner.Run(ctx, step.Request{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error = %v", err)
	}

	runner = steptest.Build(t, NewClean, map[string]any{})
	_, err := runner.Run(t.Context(), step.Request{Executor: &processtest.Executor{Reply: processtest.Reply{
		Result: process.Result{Stderr: "not a repository\n"},
		Err:    errors.New("git failed"),
	}}})
	if err == nil || !strings.Contains(err.Error(), "checking Git working tree: not a repository") {
		t.Fatalf("command failure = %v", err)
	}
}

func TestGitAssertionsAgainstRepositories(t *testing.T) {
	t.Parallel()
	dir := gittest.InitWithCommit(t)

	clean := steptest.Build(t, NewClean, map[string]any{})
	if _, err := clean.Run(t.Context(), step.Request{RunDir: dir}); err != nil {
		t.Fatal(err)
	}
	testfs.WriteFile(t, filepath.Join(dir, "untracked.txt"), []byte("dirty\n"), 0o644)
	if _, err := clean.Run(t.Context(), step.Request{RunDir: dir}); err == nil || !strings.Contains(err.Error(), "uncommitted changes") {
		t.Fatalf("dirty tree error = %v", err)
	}

	branch := steptest.Build(t, NewBranch, map[string]any{"operation": "assert", "branch": "topic", "exists": true})
	gittest.Run(t, dir, "branch", "topic")
	if _, err := branch.Run(t.Context(), step.Request{RunDir: dir}); err != nil {
		t.Fatal(err)
	}

	gittest.Run(t, dir, "update-ref", "refs/remotes/origin/topic", "HEAD")
	remote := steptest.Build(t, NewRemoteBranch, map[string]any{"branch": "topic", "remote": "origin", "exists": true})
	if _, err := remote.Run(t.Context(), step.Request{RunDir: dir}); err != nil {
		t.Fatal(err)
	}
}

func TestRemoteBranchExistsDoesNotMatchRemoteHead(t *testing.T) {
	t.Parallel()
	if remoteBranchExists("refs/remotes/HEAD\n", "", "HEAD") {
		t.Fatal("remote HEAD matched as a branch without a remote component")
	}
}
