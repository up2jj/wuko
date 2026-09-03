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
	onEmptySkip   = "skip"
	onEmptyFail   = "fail"
	onEmptyCommit = "commit"
)

type commitIdentity struct {
	Name  string `yaml:"name"`
	Email string `yaml:"email"`
}

type commitTrailer struct {
	Token string `yaml:"token"`
	Value string `yaml:"value"`
}

type commitConfig struct {
	Message   string          `yaml:"message"`
	Body      string          `yaml:"body,omitempty"`
	Trailers  []commitTrailer `yaml:"trailers,omitempty"`
	Paths     []string        `yaml:"paths,omitempty"`
	Author    *commitIdentity `yaml:"author,omitempty"`
	Committer *commitIdentity `yaml:"committer,omitempty"`
	Signoff   bool            `yaml:"signoff,omitempty"`
	Verify    *bool           `yaml:"verify,omitempty"`
	OnEmpty   string          `yaml:"on_empty,omitempty"`
}

// commitRunner creates one Git commit from the repository index.
type commitRunner struct {
	config commitConfig
	verify bool
}

// NewCommit builds a git_commit step.
func NewCommit(raw map[string]any) (step.Runner, error) {
	var config commitConfig
	if err := step.DecodeConfig(raw, &config); err != nil {
		return nil, err
	}
	if strings.TrimSpace(config.Message) == "" {
		return nil, fmt.Errorf("message is required")
	}
	if strings.ContainsRune(config.Message, '\x00') {
		return nil, fmt.Errorf("message must not contain NUL")
	}
	if _, configured := raw["body"]; configured {
		if strings.TrimSpace(config.Body) == "" {
			return nil, fmt.Errorf("body must not be blank")
		}
		if strings.ContainsRune(config.Body, '\x00') {
			return nil, fmt.Errorf("body must not contain NUL")
		}
	}
	if err := validateCommitPaths(raw, config.Paths); err != nil {
		return nil, err
	}
	if err := validateCommitIdentity(raw, "author", config.Author); err != nil {
		return nil, err
	}
	if err := validateCommitIdentity(raw, "committer", config.Committer); err != nil {
		return nil, err
	}
	if err := validateCommitTrailers(raw, config.Trailers); err != nil {
		return nil, err
	}
	if _, configured := raw["on_empty"]; configured && strings.TrimSpace(config.OnEmpty) == "" {
		return nil, fmt.Errorf("on_empty must not be blank")
	}
	if _, configured := raw["signoff"]; configured && raw["signoff"] == nil {
		return nil, fmt.Errorf("signoff must be a boolean")
	}
	if _, configured := raw["verify"]; configured && config.Verify == nil {
		return nil, fmt.Errorf("verify must be a boolean")
	}
	if _, configured := raw["on_empty"]; !configured {
		config.OnEmpty = onEmptySkip
	}
	if strings.Contains(config.OnEmpty, "{{") {
		return nil, fmt.Errorf("on_empty must not be templated")
	}
	if config.OnEmpty != onEmptySkip && config.OnEmpty != onEmptyFail && config.OnEmpty != onEmptyCommit {
		return nil, fmt.Errorf("on_empty must be skip, fail, or commit")
	}
	verify := true
	if config.Verify != nil {
		verify = *config.Verify
	}
	return &commitRunner{config: config, verify: verify}, nil
}

func validateCommitPaths(raw map[string]any, paths []string) error {
	if _, configured := raw["paths"]; !configured {
		return nil
	}
	if len(paths) == 0 {
		return fmt.Errorf("paths must contain at least one pathspec")
	}
	for index, path := range paths {
		if strings.TrimSpace(path) == "" {
			return fmt.Errorf("paths[%d] must not be blank", index)
		}
		if strings.ContainsRune(path, '\x00') {
			return fmt.Errorf("paths[%d] must not contain NUL", index)
		}
	}
	return nil
}

func validateCommitIdentity(raw map[string]any, field string, identity *commitIdentity) error {
	if _, configured := raw[field]; !configured {
		return nil
	}
	if identity == nil {
		return fmt.Errorf("%s must be an object containing name and email", field)
	}
	if strings.TrimSpace(identity.Name) == "" {
		return fmt.Errorf("%s name is required", field)
	}
	if strings.TrimSpace(identity.Email) == "" {
		return fmt.Errorf("%s email is required", field)
	}
	for _, part := range []struct{ label, value string }{{"name", identity.Name}, {"email", identity.Email}} {
		if strings.ContainsAny(part.value, "\x00\r\n") {
			return fmt.Errorf("%s %s must be a single line without NUL", field, part.label)
		}
	}
	return nil
}

func validateCommitTrailers(raw map[string]any, trailers []commitTrailer) error {
	if _, configured := raw["trailers"]; !configured {
		return nil
	}
	if len(trailers) == 0 {
		return fmt.Errorf("trailers must contain at least one trailer")
	}
	for index, trailer := range trailers {
		if strings.TrimSpace(trailer.Token) == "" {
			return fmt.Errorf("trailers[%d] token is required", index)
		}
		if strings.ContainsAny(trailer.Token, "\x00\r\n:=") {
			return fmt.Errorf("trailers[%d] token must not contain NUL, a line break, ':', or '='", index)
		}
		if strings.TrimSpace(trailer.Value) == "" {
			return fmt.Errorf("trailers[%d] value is required", index)
		}
		if strings.ContainsAny(trailer.Value, "\x00\r\n") {
			return fmt.Errorf("trailers[%d] value must be a single line without NUL", index)
		}
	}
	return nil
}

func (*commitRunner) ExecutorAware() {}

func (runner *commitRunner) Run(ctx context.Context, request step.Request) (step.Result, error) {
	if err := ctx.Err(); err != nil {
		return step.Result{}, err
	}
	if len(runner.config.Paths) > 0 {
		if err := stagePaths(ctx, request, runner.config.Paths); err != nil {
			return step.Result{}, err
		}
	}

	hasChanges, err := stagedChanges(ctx, request)
	if err != nil {
		return step.Result{}, err
	}
	if !hasChanges && runner.config.OnEmpty != onEmptyCommit {
		if runner.config.OnEmpty == onEmptyFail {
			return step.Result{}, fmt.Errorf("nothing to commit")
		}
		commit, err := currentCommit(ctx, request)
		if err != nil {
			return step.Result{}, err
		}
		return commitResult(false, commit), nil
	}

	commitRequest := request
	commitRequest.Env = maps.Clone(request.Env)
	if commitRequest.Env == nil {
		commitRequest.Env = make(map[string]string)
	}
	runner.applyIdentity(commitRequest.Env)
	result, err := runGit(ctx, commitRequest, runner.commitArgs()...)
	if err != nil {
		return step.Result{}, gitCommandError("creating Git commit", result, err)
	}
	commit, err := currentCommit(ctx, request)
	if err != nil {
		return step.Result{}, fmt.Errorf("commit created but %w", err)
	}
	return commitResult(true, commit), nil
}

// stagePaths stages the configured pathspecs. Git fails the whole command when one pathspec matches
// no file, so unmatched pathspecs are dropped and the remaining ones staged; on_empty then decides
// what an empty index means.
func stagePaths(ctx context.Context, request step.Request, paths []string) error {
	result, err := runGit(ctx, request, stageArgs(paths)...)
	if err == nil {
		return nil
	}
	var exitErr *process.ExitError
	if !errors.As(err, &exitErr) {
		return gitCommandError("staging Git paths", result, err)
	}
	matched, matchErr := matchingPaths(ctx, request, paths)
	if matchErr != nil || len(matched) == len(paths) {
		return gitCommandError("staging Git paths", result, err)
	}
	if len(matched) == 0 {
		return nil
	}
	retried, retryErr := runGit(ctx, request, stageArgs(matched)...)
	if retryErr != nil {
		return gitCommandError("staging Git paths", retried, retryErr)
	}
	return nil
}

func stageArgs(paths []string) []string {
	return append([]string{"add", "-A", "--"}, paths...)
}

// matchingPaths returns the pathspecs that match a tracked or untracked file. Ignored files count as
// matches so that Git keeps reporting them instead of the step silently skipping them.
func matchingPaths(ctx context.Context, request step.Request, paths []string) ([]string, error) {
	matched := make([]string, 0, len(paths))
	for _, path := range paths {
		result, err := runGit(ctx, request, "ls-files", "-z", "--cached", "--others", "--", path)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(result.Stdout) != "" {
			matched = append(matched, path)
		}
	}
	return matched, nil
}

func stagedChanges(ctx context.Context, request step.Request) (bool, error) {
	result, err := runGit(ctx, request, "diff", "--cached", "--quiet", "--exit-code", "--")
	if err == nil {
		return false, nil
	}
	var exitErr *process.ExitError
	if errors.As(err, &exitErr) && exitErr.Code == 1 {
		return true, nil
	}
	return false, gitCommandError("inspecting staged Git changes", result, err)
}

func currentCommit(ctx context.Context, request step.Request) (string, error) {
	result, err := runGit(ctx, request, "rev-parse", "--verify", "--quiet", "HEAD^{commit}")
	if err == nil {
		commit := strings.TrimSpace(result.Stdout)
		if commit == "" {
			return "", fmt.Errorf("resolving Git HEAD: git returned an empty object ID")
		}
		return commit, nil
	}
	var exitErr *process.ExitError
	if errors.As(err, &exitErr) && exitErr.Code == 1 {
		return "", nil
	}
	return "", gitCommandError("resolving Git HEAD", result, err)
}

func (runner *commitRunner) commitArgs() []string {
	args := []string{"commit"}
	if runner.config.OnEmpty == onEmptyCommit {
		args = append(args, "--allow-empty")
	}
	if runner.config.Signoff {
		args = append(args, "--signoff")
	}
	if !runner.verify {
		args = append(args, "--no-verify")
	}
	args = append(args, "-m", runner.config.Message)
	if runner.config.Body != "" {
		args = append(args, "-m", runner.config.Body)
	}
	for _, trailer := range runner.config.Trailers {
		args = append(args, "--trailer", trailer.Token+"="+trailer.Value)
	}
	return args
}

func (runner *commitRunner) applyIdentity(environment map[string]string) {
	if runner.config.Author != nil {
		environment["GIT_AUTHOR_NAME"] = runner.config.Author.Name
		environment["GIT_AUTHOR_EMAIL"] = runner.config.Author.Email
		environment["GIT_COMMITTER_NAME"] = runner.config.Author.Name
		environment["GIT_COMMITTER_EMAIL"] = runner.config.Author.Email
	}
	if runner.config.Committer != nil {
		environment["GIT_COMMITTER_NAME"] = runner.config.Committer.Name
		environment["GIT_COMMITTER_EMAIL"] = runner.config.Committer.Email
	}
}

func commitResult(created bool, commit string) step.Result {
	return step.Result{Outputs: map[string]any{"created": created, "commit": commit}}
}
