// Package githubrelease checks whether a GitHub repository has drifted since its latest release.
package githubrelease

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strings"
	"unicode"

	"github.com/up2jj/wuko/process"
	"github.com/up2jj/wuko/step"
)

const operationCheckDrift = "check_drift"

// Config selects the GitHub repository operation to perform.
type Config struct {
	Operation  string `yaml:"operation"`
	Repository string `yaml:"repository"`
}

// Runner executes a read-only GitHub release check through the gh CLI.
type Runner struct {
	config Config
}

type repositoryRecord struct {
	DefaultBranch string `json:"default_branch"`
}

type releaseRecord struct {
	TagName     string `json:"tag_name"`
	HTMLURL     string `json:"html_url"`
	PublishedAt string `json:"published_at"`
}

type compareRecord struct {
	HTMLURL      string `json:"html_url"`
	AheadBy      int    `json:"ahead_by"`
	BehindBy     int    `json:"behind_by"`
	TotalCommits int    `json:"total_commits"`
}

func Register(registry *step.Registry) error {
	return registry.Register("github_release", New)
}

func New(raw map[string]any) (step.Runner, error) {
	var config Config
	if err := step.DecodeConfig(raw, &config); err != nil {
		return nil, err
	}
	config.Operation = strings.TrimSpace(config.Operation)
	config.Repository = strings.TrimSpace(config.Repository)
	if config.Operation == "" {
		return nil, fmt.Errorf("operation is required")
	}
	if config.Operation != operationCheckDrift {
		return nil, fmt.Errorf("operation must be %q", operationCheckDrift)
	}
	if config.Repository == "" {
		return nil, fmt.Errorf("repository is required")
	}
	if !templated(config.Repository) {
		if err := validateRepository(config.Repository); err != nil {
			return nil, err
		}
	}
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
	repositoryInfo, err := r.repository(ctx, request, repository)
	if err != nil {
		return step.Result{}, err
	}
	branch := strings.TrimSpace(repositoryInfo.DefaultBranch)
	if branch == "" {
		return step.Result{}, fmt.Errorf("GitHub repository %q has no default branch", repository)
	}

	release, found, err := r.latestRelease(ctx, request, repository)
	if err != nil {
		return step.Result{}, err
	}
	if !found {
		return step.Result{Outputs: noReleaseOutputs(repository, branch)}, nil
	}
	if strings.TrimSpace(release.TagName) == "" {
		return step.Result{}, fmt.Errorf("decoding latest GitHub release: tag_name is missing")
	}

	comparison, err := r.compare(ctx, request, repository, release.TagName, branch)
	if err != nil {
		return step.Result{}, err
	}
	status := "current"
	if comparison.AheadBy > 0 {
		status = "changed"
	}
	return step.Result{Outputs: map[string]any{
		"repository":    repository,
		"found":         true,
		"status":        status,
		"has_changes":   comparison.AheadBy > 0,
		"release_tag":   release.TagName,
		"release_url":   release.HTMLURL,
		"published_at":  release.PublishedAt,
		"branch":        branch,
		"ahead_by":      comparison.AheadBy,
		"behind_by":     comparison.BehindBy,
		"total_commits": comparison.TotalCommits,
		"compare_url":   comparison.HTMLURL,
	}}, nil
}

func (r *Runner) repository(ctx context.Context, request step.Request, repository string) (repositoryRecord, error) {
	result, err := runCommand(ctx, request, "api", "repos/"+repository)
	if err != nil {
		return repositoryRecord{}, commandError("reading GitHub repository", result, err)
	}
	var record repositoryRecord
	if err := json.Unmarshal([]byte(result.Stdout), &record); err != nil {
		return repositoryRecord{}, fmt.Errorf("decoding GitHub repository: %w", err)
	}
	return record, nil
}

func (r *Runner) latestRelease(ctx context.Context, request step.Request, repository string) (releaseRecord, bool, error) {
	result, err := runCommand(ctx, request, "api", "repos/"+repository+"/releases/latest")
	if err != nil {
		if notFound(result, err) {
			return releaseRecord{}, false, nil
		}
		return releaseRecord{}, false, commandError("reading latest GitHub release", result, err)
	}
	var release releaseRecord
	if err := json.Unmarshal([]byte(result.Stdout), &release); err != nil {
		return releaseRecord{}, false, fmt.Errorf("decoding latest GitHub release: %w", err)
	}
	return release, true, nil
}

func (r *Runner) compare(ctx context.Context, request step.Request, repository, tag, branch string) (compareRecord, error) {
	endpoint := "repos/" + repository + "/compare/" + tag + "..." + branch
	result, err := runCommand(ctx, request, "api", endpoint)
	if err != nil {
		return compareRecord{}, commandError("comparing GitHub release", result, err)
	}
	var comparison compareRecord
	if err := json.Unmarshal([]byte(result.Stdout), &comparison); err != nil {
		return compareRecord{}, fmt.Errorf("decoding GitHub comparison: %w", err)
	}
	return comparison, nil
}

func runCommand(ctx context.Context, request step.Request, args ...string) (process.Result, error) {
	executor := request.Executor
	if executor == nil {
		executor = process.LocalExecutor{}
	}
	result, err := executor.Run(ctx, process.Options{
		Command: "gh",
		Args:    args,
		Dir:     request.RunDir,
		Env:     step.ApplyAttemptEnvironment(maps.Clone(request.Env), request),
	})
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return result, ctxErr
		}
		return result, fmt.Errorf("gh %s: %w", strings.Join(args, " "), err)
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

func noReleaseOutputs(repository, branch string) map[string]any {
	return map[string]any{
		"repository":    repository,
		"found":         false,
		"status":        "no_release",
		"has_changes":   false,
		"release_tag":   "",
		"release_url":   "",
		"published_at":  "",
		"branch":        branch,
		"ahead_by":      0,
		"behind_by":     0,
		"total_commits": 0,
		"compare_url":   "",
	}
}

func notFound(result process.Result, err error) bool {
	var exitErr *process.ExitError
	if !errors.As(err, &exitErr) {
		return false
	}
	if exitErr.Code != 1 {
		return false
	}
	detail := strings.ToLower(strings.TrimSpace(result.Stderr + "\n" + result.Stdout))
	return strings.Contains(detail, "404") || strings.Contains(detail, "not found")
}

func (r *Runner) validateResolvedConfig() error {
	if templated(r.config.Operation) || templated(r.config.Repository) {
		return errors.New("github_release configuration contains an unresolved template")
	}
	return validateRepository(r.config.Repository)
}

func validateRepository(repository string) error {
	parts := strings.Split(repository, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return fmt.Errorf("repository must be an owner/repository identifier")
	}
	for _, part := range parts {
		for _, character := range part {
			if unicode.IsSpace(character) || unicode.IsControl(character) {
				return fmt.Errorf("repository must not contain whitespace or control characters")
			}
		}
	}
	return nil
}

func templated(value string) bool { return strings.Contains(value, "{{") }
