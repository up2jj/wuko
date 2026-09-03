// Package git implements Git-related workflow checks, commits, and message operations.
package git

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strings"

	"github.com/up2jj/wuko/process"
	"github.com/up2jj/wuko/step"
)

const (
	stepClean        = "clean"
	stepBranch       = "branch"
	stepRemoteBranch = "remote_branch"
	stepBranchName   = "branch_name"
	stepOnBranch     = "on_branch"

	operationAssert = "assert"
)

type cleanConfig struct{}

type branchConfig struct {
	Operation string `yaml:"operation"`
	Branch    string `yaml:"branch"`
	Exists    *bool  `yaml:"exists"`
}

type remoteBranchConfig struct {
	Branch string `yaml:"branch"`
	Remote string `yaml:"remote,omitempty"`
	Exists *bool  `yaml:"exists"`
}

type branchNameConfig struct {
	Name string `yaml:"name"`
}

type onBranchConfig struct {
	Branch string `yaml:"branch"`
}

// Runner executes one Git assertion.
type Runner struct {
	kind         string
	operation    string
	branch       string
	remote       string
	name         string
	expectsExist bool
}

// Register adds all Git workflow steps to a registry.
func Register(registry *step.Registry) error {
	registrations := []struct {
		name    string
		builder step.Builder
	}{
		{"git_clean", NewClean},
		{"git_branch", NewBranch},
		{"git_remote_branch", NewRemoteBranch},
		{"git_branch_name", NewBranchName},
		{"git_on_branch", NewOnBranch},
		{"git_conventional_commit", NewConventionalCommit},
		{"git_commit", NewCommit},
	}
	for _, registration := range registrations {
		if err := registry.Register(registration.name, registration.builder); err != nil {
			return err
		}
	}
	return nil
}

// NewClean builds a git_clean step.
func NewClean(raw map[string]any) (step.Runner, error) {
	var config cleanConfig
	if err := step.DecodeConfig(raw, &config); err != nil {
		return nil, err
	}
	return &Runner{kind: stepClean}, nil
}

// NewBranch builds a git_branch step.
func NewBranch(raw map[string]any) (step.Runner, error) {
	var config branchConfig
	if err := step.DecodeConfig(raw, &config); err != nil {
		return nil, err
	}
	if strings.TrimSpace(config.Operation) == "" {
		return nil, fmt.Errorf("operation is required")
	}
	if config.Operation != operationAssert {
		return nil, fmt.Errorf("operation must be %q", operationAssert)
	}
	if strings.TrimSpace(config.Branch) == "" {
		return nil, fmt.Errorf("branch is required")
	}
	if config.Exists == nil {
		return nil, fmt.Errorf("exists is required")
	}
	return &Runner{
		kind:         stepBranch,
		operation:    config.Operation,
		branch:       config.Branch,
		expectsExist: *config.Exists,
	}, nil
}

// NewRemoteBranch builds a git_remote_branch step.
func NewRemoteBranch(raw map[string]any) (step.Runner, error) {
	var config remoteBranchConfig
	if err := step.DecodeConfig(raw, &config); err != nil {
		return nil, err
	}
	if strings.TrimSpace(config.Branch) == "" {
		return nil, fmt.Errorf("branch is required")
	}
	if config.Exists == nil {
		return nil, fmt.Errorf("exists is required")
	}
	if _, configured := raw["remote"]; configured && strings.TrimSpace(config.Remote) == "" {
		return nil, fmt.Errorf("remote must not be blank")
	}
	return &Runner{
		kind:         stepRemoteBranch,
		branch:       config.Branch,
		remote:       config.Remote,
		expectsExist: *config.Exists,
	}, nil
}

// NewBranchName builds a git_branch_name step.
func NewBranchName(raw map[string]any) (step.Runner, error) {
	var config branchNameConfig
	if err := step.DecodeConfig(raw, &config); err != nil {
		return nil, err
	}
	if strings.TrimSpace(config.Name) == "" {
		return nil, fmt.Errorf("name is required")
	}
	return &Runner{kind: stepBranchName, name: config.Name}, nil
}

// NewOnBranch builds a git_on_branch step.
func NewOnBranch(raw map[string]any) (step.Runner, error) {
	var config onBranchConfig
	if err := step.DecodeConfig(raw, &config); err != nil {
		return nil, err
	}
	if strings.TrimSpace(config.Branch) == "" {
		return nil, fmt.Errorf("branch is required")
	}
	return &Runner{kind: stepOnBranch, branch: config.Branch}, nil
}

func (*Runner) ExecutorAware() {}

func (r *Runner) Run(ctx context.Context, request step.Request) (step.Result, error) {
	if err := ctx.Err(); err != nil {
		return step.Result{}, err
	}
	if err := r.validateResolvedConfig(); err != nil {
		return step.Result{}, err
	}

	switch r.kind {
	case stepClean:
		return r.runClean(ctx, request)
	case stepBranch:
		return r.runBranch(ctx, request)
	case stepRemoteBranch:
		return r.runRemoteBranch(ctx, request)
	case stepBranchName:
		return r.runBranchName(ctx, request)
	case stepOnBranch:
		return r.runOnBranch(ctx, request)
	default:
		return step.Result{}, fmt.Errorf("unknown Git assertion %q", r.kind)
	}
}

func (r *Runner) runClean(ctx context.Context, request step.Request) (step.Result, error) {
	result, err := runGit(ctx, request, "status", "--porcelain", "--untracked-files=normal")
	if err != nil {
		return step.Result{}, gitCommandError("checking Git working tree", result, err)
	}
	if strings.TrimSpace(result.Stdout) != "" {
		return step.Result{}, fmt.Errorf("assertion failed: Git working tree has uncommitted changes")
	}
	return step.Result{}, nil
}

func (r *Runner) runBranch(ctx context.Context, request step.Request) (step.Result, error) {
	if r.operation != operationAssert {
		return step.Result{}, fmt.Errorf("operation %q was not resolved", r.operation)
	}
	exists, err := localBranchExists(ctx, request, r.branch)
	if err != nil {
		return step.Result{}, err
	}
	if exists != r.expectsExist {
		return step.Result{}, branchAssertionError("local branch", r.branch, r.expectsExist)
	}
	return step.Result{}, nil
}

func (r *Runner) runRemoteBranch(ctx context.Context, request step.Request) (step.Result, error) {
	result, err := runGit(ctx, request, "for-each-ref", "--format=%(refname)", "refs/remotes")
	if err != nil {
		return step.Result{}, gitCommandError("checking remote branches", result, err)
	}
	exists := remoteBranchExists(result.Stdout, r.remote, r.branch)
	if exists != r.expectsExist {
		label := "remote branch"
		if r.remote != "" {
			label = fmt.Sprintf("remote branch on %q", r.remote)
		}
		return step.Result{}, branchAssertionError(label, r.branch, r.expectsExist)
	}
	return step.Result{}, nil
}

func (r *Runner) runBranchName(ctx context.Context, request step.Request) (step.Result, error) {
	result, err := runGit(ctx, request, "check-ref-format", "--branch", r.name)
	if err == nil {
		return step.Result{}, nil
	}
	var exitErr *process.ExitError
	if errors.As(err, &exitErr) {
		return step.Result{}, fmt.Errorf("assertion failed: %q is not a valid Git branch name", r.name)
	}
	return step.Result{}, gitCommandError("validating Git branch name", result, err)
}

func (r *Runner) runOnBranch(ctx context.Context, request step.Request) (step.Result, error) {
	result, err := runGit(ctx, request, "branch", "--show-current")
	if err != nil {
		return step.Result{}, gitCommandError("reading current Git branch", result, err)
	}
	current := strings.TrimSpace(result.Stdout)
	if current != r.branch {
		if current == "" {
			return step.Result{}, fmt.Errorf("assertion failed: repository is in detached HEAD state; want branch %q", r.branch)
		}
		return step.Result{}, fmt.Errorf("assertion failed: current branch is %q; want %q", current, r.branch)
	}
	return step.Result{}, nil
}

func localBranchExists(ctx context.Context, request step.Request, branch string) (bool, error) {
	result, err := runGit(ctx, request, "show-ref", "--verify", "--quiet", "refs/heads/"+branch)
	if err == nil {
		return true, nil
	}
	var exitErr *process.ExitError
	if errors.As(err, &exitErr) && exitErr.Code == 1 {
		return false, nil
	}
	return false, gitCommandError(fmt.Sprintf("checking local branch %q", branch), result, err)
}

func remoteBranchExists(output, remote, branch string) bool {
	prefix := "refs/remotes/"
	want := prefix + remote + "/" + branch
	for _, ref := range strings.Split(output, "\n") {
		ref = strings.TrimSpace(ref)
		if ref == "" || !strings.HasPrefix(ref, prefix) {
			continue
		}
		if remote != "" {
			if ref == want {
				return true
			}
			continue
		}
		if strings.HasSuffix(ref, "/"+branch) && strings.TrimPrefix(ref, prefix) != branch {
			return true
		}
	}
	return false
}

func runGit(ctx context.Context, request step.Request, args ...string) (process.Result, error) {
	executor := request.Executor
	if executor == nil {
		executor = process.LocalExecutor{}
	}
	result, err := executor.Run(ctx, process.Options{
		Command: "git",
		Args:    args,
		Dir:     request.RunDir,
		Env:     step.ApplyAttemptEnvironment(maps.Clone(request.Env), request),
	})
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return result, ctxErr
		}
		return result, fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return result, nil
}

func gitCommandError(action string, result process.Result, err error) error {
	detail := strings.TrimSpace(result.Stderr)
	if detail == "" {
		detail = strings.TrimSpace(result.Stdout)
	}
	if detail == "" {
		return fmt.Errorf("%s: %w", action, err)
	}
	return fmt.Errorf("%s: %s: %w", action, detail, err)
}

func branchAssertionError(label, branch string, wantExists bool) error {
	if wantExists {
		return fmt.Errorf("assertion failed: %s %q does not exist", label, branch)
	}
	return fmt.Errorf("assertion failed: %s %q already exists", label, branch)
}

func (r *Runner) validateResolvedConfig() error {
	for _, value := range []string{r.operation, r.branch, r.remote, r.name} {
		if strings.Contains(value, "{{") {
			return fmt.Errorf("Git configuration contains an unresolved template")
		}
	}
	return nil
}
