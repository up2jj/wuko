// Package githubpr resolves pull requests through the GitHub CLI.
package githubpr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strconv"
	"strings"

	"github.com/up2jj/wuko/process"
	"github.com/up2jj/wuko/step"
)

const (
	operationFind         = "find"
	pullRequestJSONFields = "number,url,title,state,isDraft,headRefName,baseRefName"
)

type Config struct {
	Operation  string `yaml:"operation"`
	Repository string `yaml:"repository,omitempty"`
	Branch     string `yaml:"branch,omitempty"`
}

type Runner struct{ config Config }

type pullRequest struct {
	Number      int    `json:"number"`
	URL         string `json:"url"`
	Title       string `json:"title"`
	State       string `json:"state"`
	IsDraft     bool   `json:"isDraft"`
	HeadRefName string `json:"headRefName"`
	BaseRefName string `json:"baseRefName"`
}

func Register(registry *step.Registry) error { return registry.Register("github_pr", New) }

func New(raw map[string]any) (step.Runner, error) {
	var config Config
	if err := step.DecodeConfig(raw, &config); err != nil {
		return nil, err
	}
	if strings.TrimSpace(config.Operation) == "" {
		return nil, fmt.Errorf("operation is required")
	}
	if config.Operation != operationFind {
		return nil, fmt.Errorf("operation must be %q", operationFind)
	}
	if _, configured := raw["repository"]; configured && strings.TrimSpace(config.Repository) == "" {
		return nil, fmt.Errorf("repository must not be blank")
	}
	if _, configured := raw["branch"]; configured && strings.TrimSpace(config.Branch) == "" {
		return nil, fmt.Errorf("branch must not be blank")
	}
	config.Repository = strings.TrimSpace(config.Repository)
	config.Branch = strings.TrimSpace(config.Branch)
	return &Runner{config: config}, nil
}

func (*Runner) ExecutorAware() {}

func (r *Runner) Run(ctx context.Context, request step.Request) (step.Result, error) {
	if err := ctx.Err(); err != nil {
		return step.Result{}, err
	}
	if err := r.validateResolvedConfig(); err != nil {
		return step.Result{}, err
	}

	repository := r.config.Repository
	if repository == "" {
		repository = strings.TrimSpace(request.Env["GITHUB_REPOSITORY"])
	}

	if r.config.Branch == "" {
		if number, ok := pullRequestNumber(request.Env["GITHUB_REF"]); ok {
			return r.viewPullRequest(ctx, request, repository, number)
		}
		if branch := strings.TrimSpace(request.Env["GITHUB_HEAD_REF"]); branch != "" {
			return r.listPullRequests(ctx, request, repository, branch)
		}
	}

	branch := r.config.Branch
	if branch == "" {
		var err error
		branch, err = currentBranch(ctx, request)
		if err != nil {
			return step.Result{}, err
		}
	}
	return r.listPullRequests(ctx, request, repository, branch)
}

func (r *Runner) viewPullRequest(ctx context.Context, request step.Request, repository, number string) (step.Result, error) {
	args := []string{"pr", "view", number, "--json", pullRequestJSONFields}
	args = withRepository(args, repository)
	result, err := runCommand(ctx, request, "gh", args...)
	if err != nil {
		return step.Result{}, commandError("viewing GitHub pull request", result, err)
	}

	var pull pullRequest
	if err := json.Unmarshal([]byte(result.Stdout), &pull); err != nil {
		return step.Result{}, fmt.Errorf("decoding GitHub pull request: %w", err)
	}
	if pull.Number <= 0 {
		return step.Result{}, fmt.Errorf("decoding GitHub pull request: number is missing or invalid")
	}
	return step.Result{Outputs: pullRequestOutputs(pull, repository)}, nil
}

func (r *Runner) listPullRequests(ctx context.Context, request step.Request, repository, branch string) (step.Result, error) {
	args := []string{"pr", "list", "--state", "open", "--head", branch, "--limit", "2", "--json", pullRequestJSONFields}
	args = withRepository(args, repository)
	result, err := runCommand(ctx, request, "gh", args...)
	if err != nil {
		return step.Result{}, commandError("listing GitHub pull requests", result, err)
	}

	var pulls []pullRequest
	if err := json.Unmarshal([]byte(result.Stdout), &pulls); err != nil {
		return step.Result{}, fmt.Errorf("decoding GitHub pull requests: %w", err)
	}
	switch len(pulls) {
	case 0:
		return step.Result{Outputs: noPullRequestOutputs()}, nil
	case 1:
		if pulls[0].Number <= 0 {
			return step.Result{}, fmt.Errorf("decoding GitHub pull request: number is missing or invalid")
		}
		return step.Result{Outputs: pullRequestOutputs(pulls[0], repository)}, nil
	default:
		return step.Result{}, fmt.Errorf("branch %q matches multiple open GitHub pull requests", branch)
	}
}

func currentBranch(ctx context.Context, request step.Request) (string, error) {
	result, err := runCommand(ctx, request, "git", "branch", "--show-current")
	if err != nil {
		return "", commandError("reading current Git branch", result, err)
	}
	branch := strings.TrimSpace(result.Stdout)
	if branch == "" {
		return "", fmt.Errorf("cannot determine GitHub pull request: repository is in detached HEAD state")
	}
	return branch, nil
}

func runCommand(ctx context.Context, request step.Request, command string, args ...string) (process.Result, error) {
	executor := request.Executor
	if executor == nil {
		executor = process.LocalExecutor{}
	}
	result, err := executor.Run(ctx, process.Options{
		Command: command,
		Args:    args,
		Dir:     request.RunDir,
		Env:     step.ApplyAttemptEnvironment(maps.Clone(request.Env), request),
	})
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return result, ctxErr
		}
		return result, fmt.Errorf("%s %s: %w", command, strings.Join(args, " "), err)
	}
	return result, nil
}

func commandError(action string, result process.Result, err error) error {
	detail := strings.TrimSpace(result.Stderr)
	if detail == "" {
		detail = strings.TrimSpace(result.Stdout)
	}
	if detail == "" {
		return fmt.Errorf("%s: %w", action, err)
	}
	return fmt.Errorf("%s: %s: %w", action, detail, err)
}

func withRepository(args []string, repository string) []string {
	if repository == "" {
		return args
	}
	return append(args, "--repo", repository)
}

func pullRequestNumber(ref string) (string, bool) {
	parts := strings.Split(strings.TrimSpace(ref), "/")
	if len(parts) != 4 || parts[0] != "refs" || parts[1] != "pull" || parts[3] == "" {
		return "", false
	}
	number, err := strconv.ParseUint(parts[2], 10, 64)
	if err != nil || number == 0 {
		return "", false
	}
	return parts[2], true
}

func pullRequestOutputs(pull pullRequest, repository string) map[string]any {
	return map[string]any{
		"found":       true,
		"number":      pull.Number,
		"url":         pull.URL,
		"title":       pull.Title,
		"state":       pull.State,
		"is_draft":    pull.IsDraft,
		"head_branch": pull.HeadRefName,
		"base_branch": pull.BaseRefName,
		"repository":  repository,
	}
}

func noPullRequestOutputs() map[string]any {
	return map[string]any{
		"found":       false,
		"number":      0,
		"url":         "",
		"title":       "",
		"state":       "",
		"is_draft":    false,
		"head_branch": "",
		"base_branch": "",
		"repository":  "",
	}
}

func (r *Runner) validateResolvedConfig() error {
	if strings.Contains(r.config.Operation, "{{") || strings.Contains(r.config.Repository, "{{") || strings.Contains(r.config.Branch, "{{") {
		return errors.New("github_pr configuration contains an unresolved template")
	}
	return nil
}
