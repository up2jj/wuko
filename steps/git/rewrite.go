package git

import (
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"

	"github.com/up2jj/wuko/step"
)

const (
	stageAll     = "all"
	stageTracked = "tracked"
	stageNone    = "none"

	mergeFFOnly = "ff_only"
	mergeNoFF   = "no_ff"
)

type squashConfig struct {
	Target    string          `yaml:"target,omitempty"`
	Message   string          `yaml:"message"`
	Body      string          `yaml:"body,omitempty"`
	Trailers  []commitTrailer `yaml:"trailers,omitempty"`
	Author    *commitIdentity `yaml:"author,omitempty"`
	Committer *commitIdentity `yaml:"committer,omitempty"`
	Signoff   bool            `yaml:"signoff,omitempty"`
	Verify    *bool           `yaml:"verify,omitempty"`
	Stage     string          `yaml:"stage,omitempty"`
}

type squashRunner struct {
	config squashConfig
	verify bool
}

type rebaseConfig struct {
	Target string `yaml:"target,omitempty"`
}

type rebaseRunner struct {
	config rebaseConfig
}

type mergeConfig struct {
	Target  string `yaml:"target,omitempty"`
	Source  string `yaml:"source,omitempty"`
	Mode    string `yaml:"mode,omitempty"`
	Message string `yaml:"message,omitempty"`
}

type mergeRunner struct {
	config mergeConfig
}

// NewSquash builds a host-local git_squash step.
func NewSquash(raw map[string]any) (step.Runner, error) {
	var config squashConfig
	if err := step.DecodeConfig(raw, &config); err != nil {
		return nil, err
	}
	if strings.TrimSpace(config.Message) == "" {
		return nil, fmt.Errorf("message is required")
	}
	if strings.ContainsRune(config.Message, '\x00') {
		return nil, fmt.Errorf("message must not contain NUL")
	}
	if _, ok := raw["body"]; ok && strings.TrimSpace(config.Body) == "" {
		return nil, fmt.Errorf("body must not be blank")
	}
	if strings.ContainsRune(config.Body, '\x00') {
		return nil, fmt.Errorf("body must not contain NUL")
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
	if _, ok := raw["verify"]; ok && config.Verify == nil {
		return nil, fmt.Errorf("verify must be a boolean")
	}
	if value, ok := raw["signoff"]; ok && value == nil {
		return nil, fmt.Errorf("signoff must be a boolean")
	}
	if config.Stage == "" {
		config.Stage = stageAll
	}
	if strings.Contains(config.Stage, "{{") {
		return nil, fmt.Errorf("stage must not be templated")
	}
	if config.Stage != stageAll && config.Stage != stageTracked && config.Stage != stageNone {
		return nil, fmt.Errorf("stage must be all, tracked, or none")
	}
	verify := true
	if config.Verify != nil {
		verify = *config.Verify
	}
	return &squashRunner{config: config, verify: verify}, nil
}

// NewRebase builds a host-local git_rebase step.
func NewRebase(raw map[string]any) (step.Runner, error) {
	var config rebaseConfig
	if err := step.DecodeConfig(raw, &config); err != nil {
		return nil, err
	}
	return &rebaseRunner{config: config}, nil
}

// NewMerge builds a host-local git_merge step.
func NewMerge(raw map[string]any) (step.Runner, error) {
	var config mergeConfig
	if err := step.DecodeConfig(raw, &config); err != nil {
		return nil, err
	}
	if config.Mode == "" {
		config.Mode = mergeFFOnly
	}
	if strings.Contains(config.Mode, "{{") {
		return nil, fmt.Errorf("mode must not be templated")
	}
	if config.Mode != mergeFFOnly && config.Mode != mergeNoFF {
		return nil, fmt.Errorf("mode must be ff_only or no_ff")
	}
	if config.Mode == mergeNoFF && strings.TrimSpace(config.Message) == "" {
		return nil, fmt.Errorf("message is required for no_ff merge")
	}
	if strings.ContainsRune(config.Message, '\x00') {
		return nil, fmt.Errorf("message must not contain NUL")
	}
	if config.Mode == mergeFFOnly {
		if _, ok := raw["message"]; ok {
			return nil, fmt.Errorf("message is only valid for no_ff merge")
		}
	}
	return &mergeRunner{config: config}, nil
}

func (runner *squashRunner) Run(ctx context.Context, request step.Request) (step.Result, error) {
	if err := unresolvedRewriteValues("git_squash", runner.config.Target, runner.config.Message, runner.config.Body); err != nil {
		return step.Result{}, err
	}
	repository, err := loadRepository(ctx, request)
	if err != nil {
		return step.Result{}, err
	}
	target := runner.config.Target
	if target == "" {
		target = repository.DefaultBranch
	}
	if target == "" {
		return step.Result{}, fmt.Errorf("git_squash target is required because no default branch could be detected")
	}
	branch, err := currentBranch(ctx, request)
	if err != nil {
		return step.Result{}, err
	}
	if branch == "" {
		return step.Result{}, fmt.Errorf("git_squash requires an attached branch")
	}
	oldHead, err := gitOutput(ctx, request, "rev-parse", "HEAD")
	if err != nil {
		return step.Result{}, err
	}
	base, err := gitOutput(ctx, request, "merge-base", target, oldHead)
	if err != nil {
		return step.Result{}, fmt.Errorf("finding squash base: %w", err)
	}
	switch runner.config.Stage {
	case stageAll:
		if result, stageErr := runGit(ctx, request, "add", "-A", "--", ":/"); stageErr != nil {
			return step.Result{}, gitCommandError("staging all changes", result, stageErr)
		}
	case stageTracked:
		if result, stageErr := runGit(ctx, request, "add", "-u", "--", ":/"); stageErr != nil {
			return step.Result{}, gitCommandError("staging tracked changes", result, stageErr)
		}
	}
	empty, err := gitSuccess(ctx, request, "diff", "--cached", "--quiet", base, "--")
	if err != nil {
		return step.Result{}, err
	}
	if empty {
		return step.Result{}, fmt.Errorf("squash has no net changes against %s; the branch was left untouched", base)
	}
	backupRef := "refs/wuko/backups/" + sanitizeRefPart(branch) + "-" + shortObjectID(oldHead)
	if result, backupErr := runGit(ctx, request, "update-ref", backupRef, oldHead); backupErr != nil {
		return step.Result{}, gitCommandError("creating squash recovery ref", result, backupErr)
	}
	if result, resetErr := runGit(ctx, request, "reset", "--soft", base); resetErr != nil {
		return step.Result{}, gitCommandError("preparing squash commit", result, resetErr)
	}
	commit := commitRunner{config: commitConfig{
		Message: runner.config.Message, Body: runner.config.Body, Trailers: runner.config.Trailers,
		Author: runner.config.Author, Committer: runner.config.Committer,
		Signoff: runner.config.Signoff, OnEmpty: onEmptyFail,
	}, verify: runner.verify}
	commitRequest := request
	commitRequest.Env = maps.Clone(request.Env)
	if commitRequest.Env == nil {
		commitRequest.Env = make(map[string]string)
	}
	commit.applyIdentity(commitRequest.Env)
	result, err := runGit(ctx, commitRequest, commit.commitArgs()...)
	if err != nil {
		return step.Result{}, gitCommandError(fmt.Sprintf("creating squash commit; original HEAD is preserved at %s", backupRef), result, err)
	}
	newHead, err := gitOutput(ctx, request, "rev-parse", "HEAD")
	if err != nil {
		return step.Result{}, err
	}
	return step.Result{Outputs: map[string]any{
		"branch": branch, "target": target, "base": base, "old_head": oldHead,
		"commit": newHead, "backup_ref": backupRef,
	}}, nil
}

func (runner *rebaseRunner) Run(ctx context.Context, request step.Request) (step.Result, error) {
	if err := unresolvedRewriteValues("git_rebase", runner.config.Target); err != nil {
		return step.Result{}, err
	}
	repository, err := loadRepository(ctx, request)
	if err != nil {
		return step.Result{}, err
	}
	target := runner.config.Target
	if target == "" {
		target = repository.DefaultBranch
	}
	if target == "" {
		return step.Result{}, fmt.Errorf("git_rebase target is required because no default branch could be detected")
	}
	before, err := gitOutput(ctx, request, "rev-parse", "HEAD")
	if err != nil {
		return step.Result{}, err
	}
	branch, err := currentBranch(ctx, request)
	if err != nil {
		return step.Result{}, err
	}
	if branch == "" {
		return step.Result{}, fmt.Errorf("git_rebase requires an attached branch")
	}
	upToDate, err := gitSuccess(ctx, request, "merge-base", "--is-ancestor", target, before)
	if err != nil {
		return step.Result{}, err
	}
	if upToDate {
		return step.Result{Outputs: map[string]any{"target": target, "before": before, "after": before, "rebased": false}}, nil
	}
	result, err := runGit(ctx, request, "rebase", target)
	if err != nil {
		return step.Result{}, gitCommandError("rebasing Git branch (conflict state was left open)", result, err)
	}
	after, err := gitOutput(ctx, request, "rev-parse", "HEAD")
	if err != nil {
		return step.Result{}, err
	}
	return step.Result{Outputs: map[string]any{"target": target, "before": before, "after": after, "rebased": before != after}}, nil
}

func (runner *mergeRunner) Run(ctx context.Context, request step.Request) (step.Result, error) {
	if err := unresolvedRewriteValues("git_merge", runner.config.Target, runner.config.Source, runner.config.Message); err != nil {
		return step.Result{}, err
	}
	repository, err := loadRepository(ctx, request)
	if err != nil {
		return step.Result{}, err
	}
	target := runner.config.Target
	if target == "" {
		target = repository.DefaultBranch
	}
	if target == "" {
		return step.Result{}, fmt.Errorf("git_merge target is required because no default branch could be detected")
	}
	source := runner.config.Source
	if source == "" {
		source, err = currentBranch(ctx, request)
		if err != nil {
			return step.Result{}, err
		}
		if source == "" {
			source = "HEAD"
		}
	}
	repoRequest := request
	repoRequest.RunDir = repository.Root
	targetBefore, err := gitOutput(ctx, repoRequest, "rev-parse", "refs/heads/"+target)
	if err != nil {
		return step.Result{}, fmt.Errorf("resolving local merge target %q: %w", target, err)
	}
	sourceID, _, err := resolveCommit(ctx, request, source, false)
	if err != nil {
		return step.Result{}, fmt.Errorf("resolving merge source %q: %w", source, err)
	}
	if targetBefore == sourceID {
		return mergeResult(target, source, runner.config.Mode, targetBefore, targetBefore, false), nil
	}
	var targetWorktree *worktreeState
	for index := range repository.Worktrees {
		if repository.Worktrees[index].Branch == target {
			targetWorktree = &repository.Worktrees[index]
			break
		}
	}
	if targetWorktree == nil && runner.config.Mode == mergeFFOnly {
		fastForward, predicateErr := gitSuccess(ctx, repoRequest, "merge-base", "--is-ancestor", targetBefore, sourceID)
		if predicateErr != nil {
			return step.Result{}, predicateErr
		}
		if !fastForward {
			return step.Result{}, fmt.Errorf("source %q cannot fast-forward target %q", source, target)
		}
		updated, updateErr := runGit(ctx, repoRequest, "update-ref", "refs/heads/"+target, sourceID, targetBefore)
		if updateErr != nil {
			return step.Result{}, gitCommandError(fmt.Sprintf("fast-forwarding target %q", target), updated, updateErr)
		}
		return mergeResult(target, source, runner.config.Mode, targetBefore, sourceID, false), nil
	}
	mergeDir := ""
	cleanup := func() error { return nil }
	temporary := false
	if targetWorktree != nil {
		mergeDir = targetWorktree.Path
	} else {
		mergeDir, cleanup, err = temporaryMergeWorktree(ctx, repoRequest, repository.Root, target)
		if err != nil {
			return step.Result{}, err
		}
		temporary = true
	}
	cleaned := false
	defer func() {
		if temporary && !cleaned {
			_ = cleanup()
		}
	}()
	mergeRequest := request
	mergeRequest.RunDir = mergeDir
	status, err := runGit(ctx, mergeRequest, "status", "--porcelain=v1", "-z", "--untracked-files=normal")
	if err != nil {
		return step.Result{}, gitCommandError("checking merge target worktree", status, err)
	}
	if status.Stdout != "" {
		return step.Result{}, fmt.Errorf("merge target worktree %q has uncommitted changes", mergeDir)
	}
	args := []string{"merge"}
	if runner.config.Mode == mergeFFOnly {
		args = append(args, "--ff-only")
	} else {
		args = append(args, "--no-ff", "-m", runner.config.Message)
	}
	result, mergeErr := runGit(ctx, mergeRequest, append(args, sourceID)...)
	if mergeErr != nil {
		action := "merging Git branch"
		if !temporary {
			action = fmt.Sprintf("merging Git branch (merge state was left open in %q)", mergeDir)
		}
		return step.Result{}, gitCommandError(action, result, mergeErr)
	}
	after, err := gitOutput(ctx, mergeRequest, "rev-parse", "HEAD")
	if err != nil {
		return step.Result{}, err
	}
	if cleanupErr := cleanup(); cleanupErr != nil {
		return step.Result{}, fmt.Errorf("merge succeeded but temporary worktree cleanup failed: %w", cleanupErr)
	}
	cleaned = true
	return mergeResult(target, source, runner.config.Mode, targetBefore, after, runner.config.Mode == mergeNoFF && targetBefore != after), nil
}

func temporaryMergeWorktree(ctx context.Context, request step.Request, root, target string) (string, func() error, error) {
	dir, err := os.MkdirTemp(filepath.Dir(root), ".wuko-merge-")
	if err != nil {
		return "", nil, fmt.Errorf("creating temporary merge directory: %w", err)
	}
	if err := os.Remove(dir); err != nil {
		return "", nil, fmt.Errorf("preparing temporary merge directory: %w", err)
	}
	result, err := runGit(ctx, request, "worktree", "add", dir, target)
	if err != nil {
		return "", nil, gitCommandError("creating temporary target worktree", result, err)
	}
	cleanup := func() error {
		result, removeErr := runGit(context.WithoutCancel(ctx), request, "worktree", "remove", "--force", dir)
		if removeErr != nil {
			return gitCommandError("removing temporary target worktree", result, removeErr)
		}
		return nil
	}
	return dir, cleanup, nil
}

func mergeResult(target, source, mode, before, after string, mergeCommit bool) step.Result {
	return step.Result{Outputs: map[string]any{
		"target": target, "source": source, "mode": mode, "before": before,
		"after": after, "changed": before != after, "merge_commit": mergeCommit,
	}}
}

func currentBranch(ctx context.Context, request step.Request) (string, error) {
	result, err := runGit(ctx, request, "branch", "--show-current")
	if err != nil {
		return "", gitCommandError("reading current Git branch", result, err)
	}
	return strings.TrimSpace(result.Stdout), nil
}

func unresolvedRewriteValues(stepName string, values ...string) error {
	for _, value := range values {
		if strings.Contains(value, "{{") {
			return fmt.Errorf("%s configuration contains an unresolved template", stepName)
		}
	}
	return nil
}

func sanitizeRefPart(value string) string {
	value = strings.Trim(value, "/.")
	value = strings.NewReplacer("/", "-", "\\", "-", " ", "-").Replace(value)
	if value == "" {
		return "branch"
	}
	return value
}
