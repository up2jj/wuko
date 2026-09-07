package git

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"

	"github.com/up2jj/wuko/process"
	"github.com/up2jj/wuko/step"
)

const (
	worktreeList   = "list"
	worktreeEnsure = "ensure"
	worktreeRemove = "remove"

	deleteSafe  = "safe"
	deleteNever = "never"
	deleteForce = "force"
)

type worktreeConfig struct {
	Operation      string `yaml:"operation"`
	Branch         string `yaml:"branch,omitempty"`
	Base           string `yaml:"base,omitempty"`
	Path           string `yaml:"path,omitempty"`
	Create         bool   `yaml:"create,omitempty"`
	Target         string `yaml:"target,omitempty"`
	Force          bool   `yaml:"force,omitempty"`
	DeleteBranch   string `yaml:"delete_branch,omitempty"`
	IntegratedInto string `yaml:"integrated_into,omitempty"`
	Comparison     string `yaml:"comparison,omitempty"`
}

type worktreeRunner struct {
	config worktreeConfig
}

type repositoryState struct {
	Root          string
	CurrentRoot   string
	DefaultBranch string
	Worktrees     []worktreeState
}

type worktreeState struct {
	Path       string
	Head       string
	Branch     string
	Detached   bool
	Bare       bool
	Locked     bool
	LockReason string
	Prunable   bool
}

// NewWorktree builds a host-local git_worktree step. It deliberately does not implement
// step.ExecutorAware: paths returned by this step are meaningful only on the Wuko host.
func NewWorktree(raw map[string]any) (step.Runner, error) {
	var config worktreeConfig
	if err := step.DecodeConfig(raw, &config); err != nil {
		return nil, err
	}
	if strings.Contains(config.Operation, "{{") {
		return nil, fmt.Errorf("operation must not be templated")
	}
	for _, field := range []string{"create", "force"} {
		if value, ok := raw[field]; ok && value == nil {
			return nil, fmt.Errorf("%s must be a boolean", field)
		}
	}
	switch config.Operation {
	case worktreeList:
		if err := rejectFields(raw, "branch", "base", "path", "create", "target", "force", "delete_branch", "integrated_into"); err != nil {
			return nil, err
		}
	case worktreeEnsure:
		if strings.TrimSpace(config.Branch) == "" {
			return nil, fmt.Errorf("branch is required for ensure")
		}
		if err := rejectFields(raw, "target", "force", "delete_branch", "integrated_into", "comparison"); err != nil {
			return nil, err
		}
	case worktreeRemove:
		if strings.TrimSpace(config.Target) == "" {
			return nil, fmt.Errorf("target is required for remove")
		}
		if err := rejectFields(raw, "branch", "base", "path", "create", "comparison"); err != nil {
			return nil, err
		}
		if config.DeleteBranch == "" {
			config.DeleteBranch = deleteSafe
		}
		if config.DeleteBranch != deleteSafe && config.DeleteBranch != deleteNever && config.DeleteBranch != deleteForce {
			return nil, fmt.Errorf("delete_branch must be safe, never, or force")
		}
	default:
		if strings.TrimSpace(config.Operation) == "" {
			return nil, fmt.Errorf("operation is required")
		}
		return nil, fmt.Errorf("operation must be list, ensure, or remove")
	}
	return &worktreeRunner{config: config}, nil
}

func rejectFields(raw map[string]any, fields ...string) error {
	for _, field := range fields {
		if _, ok := raw[field]; ok {
			return fmt.Errorf("%s is not valid for this operation", field)
		}
	}
	return nil
}

func (runner *worktreeRunner) Run(ctx context.Context, request step.Request) (step.Result, error) {
	if err := ctx.Err(); err != nil {
		return step.Result{}, err
	}
	for _, value := range []string{runner.config.Branch, runner.config.Base, runner.config.Path, runner.config.Target, runner.config.IntegratedInto, runner.config.Comparison} {
		if strings.Contains(value, "{{") {
			return step.Result{}, fmt.Errorf("git_worktree configuration contains an unresolved template")
		}
	}
	switch runner.config.Operation {
	case worktreeList:
		return runner.list(ctx, request)
	case worktreeEnsure:
		return runner.ensure(ctx, request)
	case worktreeRemove:
		return runner.remove(ctx, request)
	default:
		return step.Result{}, fmt.Errorf("operation %q was not resolved", runner.config.Operation)
	}
}

func loadRepository(ctx context.Context, request step.Request) (repositoryState, error) {
	rootResult, err := runGit(ctx, request, "rev-parse", "--show-toplevel")
	if err != nil {
		return repositoryState{}, gitCommandError("finding Git repository", rootResult, err)
	}
	root := strings.TrimSpace(rootResult.Stdout)
	listRequest := request
	listRequest.RunDir = root
	listed, err := runGit(ctx, listRequest, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return repositoryState{}, gitCommandError("listing Git worktrees", listed, err)
	}
	worktrees, err := parseWorktreePorcelain(listed.Stdout)
	if err != nil {
		return repositoryState{}, err
	}
	state := repositoryState{Root: worktrees[0].Path, CurrentRoot: root, Worktrees: worktrees}
	state.DefaultBranch, err = detectDefaultBranch(ctx, listRequest, worktrees)
	if err != nil {
		return repositoryState{}, err
	}
	return state, nil
}

func parseWorktreePorcelain(output string) ([]worktreeState, error) {
	var worktrees []worktreeState
	var current *worktreeState
	flush := func() {
		if current != nil {
			worktrees = append(worktrees, *current)
			current = nil
		}
	}
	for _, token := range strings.Split(output, "\x00") {
		if token == "" {
			flush()
			continue
		}
		key, value, _ := strings.Cut(token, " ")
		if key == "worktree" {
			flush()
			current = &worktreeState{Path: value}
			continue
		}
		if current == nil {
			return nil, fmt.Errorf("parsing Git worktree list: field %q appeared before worktree", key)
		}
		switch key {
		case "HEAD":
			current.Head = value
		case "branch":
			current.Branch = strings.TrimPrefix(value, "refs/heads/")
		case "detached":
			current.Detached = true
		case "bare":
			current.Bare = true
		case "locked":
			current.Locked, current.LockReason = true, value
		case "prunable":
			current.Prunable = true
		}
	}
	flush()
	if len(worktrees) == 0 {
		return nil, fmt.Errorf("parsing Git worktree list: no worktrees returned")
	}
	return worktrees, nil
}

func detectDefaultBranch(ctx context.Context, request step.Request, worktrees []worktreeState) (string, error) {
	result, err := runGit(ctx, request, "symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD")
	if err == nil {
		return strings.TrimPrefix(strings.TrimSpace(result.Stdout), "origin/"), nil
	}
	var exitErr *process.ExitError
	if !errors.As(err, &exitErr) || exitErr.Code != 1 {
		return "", gitCommandError("detecting default Git branch", result, err)
	}
	remoteHeads, err := runGit(ctx, request, "for-each-ref", "--format=%(symref:short)", "refs/remotes/*/HEAD")
	if err != nil {
		return "", gitCommandError("detecting remote default Git branch", remoteHeads, err)
	}
	remoteDefaults := make(map[string]struct{})
	for _, remoteHead := range strings.Fields(remoteHeads.Stdout) {
		_, branch, found := strings.Cut(remoteHead, "/")
		if found && branch != "" {
			remoteDefaults[branch] = struct{}{}
		}
	}
	if len(remoteDefaults) == 1 {
		for branch := range remoteDefaults {
			return branch, nil
		}
	}
	for _, candidate := range []string{"main", "master"} {
		exists, existsErr := localBranchExists(ctx, request, candidate)
		if existsErr != nil {
			return "", existsErr
		}
		if exists {
			return candidate, nil
		}
	}
	if len(worktrees) > 0 && worktrees[0].Branch != "" {
		return worktrees[0].Branch, nil
	}
	return "", nil
}

func (runner *worktreeRunner) list(ctx context.Context, request step.Request) (step.Result, error) {
	repository, err := loadRepository(ctx, request)
	if err != nil {
		return step.Result{}, err
	}
	comparison := runner.config.Comparison
	if comparison == "" {
		comparison = repository.DefaultBranch
	}
	items := make([]any, 0, len(repository.Worktrees))
	for index, worktree := range repository.Worktrees {
		item, itemErr := describeWorktree(ctx, request, repository, worktree, comparison, index == 0)
		if itemErr != nil {
			return step.Result{}, itemErr
		}
		items = append(items, item)
	}
	return step.Result{Outputs: map[string]any{
		"repository_root": repository.Root,
		"default_branch":  repository.DefaultBranch,
		"comparison":      comparison,
		"items":           items,
	}}, nil
}

func describeWorktree(ctx context.Context, request step.Request, repository repositoryState, worktree worktreeState, comparison string, primary bool) (map[string]any, error) {
	worktreeRequest := request
	worktreeRequest.RunDir = worktree.Path
	changes := changeSummary("")
	statusAvailable := !worktree.Bare && !worktree.Prunable
	if statusAvailable {
		status, err := runGit(ctx, worktreeRequest, "status", "--porcelain=v1", "-z", "--untracked-files=normal")
		if err != nil {
			return nil, gitCommandError(fmt.Sprintf("reading Git status in %q", worktree.Path), status, err)
		}
		changes = changeSummary(status.Stdout)
	}
	repositoryRequest := request
	repositoryRequest.RunDir = repository.Root
	ahead, behind := 0, 0
	if comparison != "" && worktree.Head != "" {
		counts, countErr := runGit(ctx, repositoryRequest, "rev-list", "--left-right", "--count", comparison+"..."+worktree.Head)
		if countErr != nil {
			return nil, gitCommandError(fmt.Sprintf("comparing worktree %q with %q", worktree.Path, comparison), counts, countErr)
		}
		fields := strings.Fields(counts.Stdout)
		if len(fields) != 2 {
			return nil, fmt.Errorf("comparing worktree %q with %q: expected two counts", worktree.Path, comparison)
		}
		parsedBehind, parseErr := strconv.Atoi(fields[0])
		if parseErr != nil {
			return nil, fmt.Errorf("parsing behind count %q: %w", fields[0], parseErr)
		}
		parsedAhead, parseErr := strconv.Atoi(fields[1])
		if parseErr != nil {
			return nil, fmt.Errorf("parsing ahead count %q: %w", fields[1], parseErr)
		}
		behind, ahead = parsedBehind, parsedAhead
	}
	currentRoot, _ := filepath.Abs(repository.CurrentRoot)
	worktreeRoot, _ := filepath.Abs(worktree.Path)
	upstream, upstreamAhead, upstreamBehind, err := branchUpstream(ctx, repositoryRequest, worktree.Branch)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"branch": worktree.Branch, "path": worktree.Path, "head": worktree.Head,
		"short_head": shortObjectID(worktree.Head), "current": currentRoot == worktreeRoot,
		"primary": primary, "detached": worktree.Detached, "bare": worktree.Bare,
		"locked": worktree.Locked, "lock_reason": worktree.LockReason, "prunable": worktree.Prunable,
		"status_available": statusAvailable, "clean": statusAvailable && changes["total"].(int) == 0, "changes": changes,
		"upstream": upstream, "upstream_ahead": upstreamAhead, "upstream_behind": upstreamBehind,
		"ahead": ahead, "behind": behind,
	}, nil
}

func changeSummary(output string) map[string]any {
	summary := map[string]any{"staged": 0, "modified": 0, "untracked": 0, "conflicted": 0, "renamed": 0, "deleted": 0, "total": 0}
	skipPath := false
	for _, record := range strings.Split(output, "\x00") {
		if skipPath {
			skipPath = false
			continue
		}
		if len(record) < 2 {
			continue
		}
		x, y := record[0], record[1]
		summary["total"] = summary["total"].(int) + 1
		if x == '?' && y == '?' {
			summary["untracked"] = summary["untracked"].(int) + 1
			continue
		}
		if x == 'U' || y == 'U' || x == 'A' && y == 'A' || x == 'D' && y == 'D' {
			summary["conflicted"] = summary["conflicted"].(int) + 1
		}
		if x == 'R' || y == 'R' || x == 'C' || y == 'C' {
			summary["renamed"] = summary["renamed"].(int) + 1
			skipPath = true
		}
		if x == 'D' || y == 'D' {
			summary["deleted"] = summary["deleted"].(int) + 1
		}
		if x != ' ' {
			summary["staged"] = summary["staged"].(int) + 1
		}
		if y != ' ' {
			summary["modified"] = summary["modified"].(int) + 1
		}
	}
	return summary
}

func branchUpstream(ctx context.Context, request step.Request, branch string) (string, int, int, error) {
	if branch == "" || request.RunDir == "" {
		return "", 0, 0, nil
	}
	result, err := runGit(ctx, request, "for-each-ref", "--format=%(upstream:short)", "refs/heads/"+branch)
	if err != nil {
		return "", 0, 0, gitCommandError(fmt.Sprintf("reading upstream for %q", branch), result, err)
	}
	upstream := strings.TrimSpace(result.Stdout)
	if upstream == "" {
		return upstream, 0, 0, nil
	}
	counts, err := runGit(ctx, request, "rev-list", "--left-right", "--count", upstream+"..."+branch)
	if err != nil {
		return "", 0, 0, gitCommandError(fmt.Sprintf("comparing %q with upstream %q", branch, upstream), counts, err)
	}
	fields := strings.Fields(counts.Stdout)
	if len(fields) != 2 {
		return "", 0, 0, fmt.Errorf("comparing %q with upstream %q: expected two counts", branch, upstream)
	}
	behind, err := strconv.Atoi(fields[0])
	if err != nil {
		return "", 0, 0, fmt.Errorf("parsing upstream behind count %q: %w", fields[0], err)
	}
	ahead, err := strconv.Atoi(fields[1])
	if err != nil {
		return "", 0, 0, fmt.Errorf("parsing upstream ahead count %q: %w", fields[1], err)
	}
	return upstream, ahead, behind, nil
}

func (runner *worktreeRunner) ensure(ctx context.Context, request step.Request) (step.Result, error) {
	repository, err := loadRepository(ctx, request)
	if err != nil {
		return step.Result{}, err
	}
	if err := validateBranch(ctx, request, runner.config.Branch); err != nil {
		return step.Result{}, err
	}
	for _, worktree := range repository.Worktrees {
		if worktree.Branch == runner.config.Branch {
			return ensureResult(repository, worktree, false, false, ""), nil
		}
	}
	path, err := worktreePath(repository.Root, runner.config.Path, runner.config.Branch)
	if err != nil {
		return step.Result{}, err
	}
	if _, statErr := os.Stat(path); statErr == nil {
		return step.Result{}, fmt.Errorf("worktree path %q already exists", path)
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return step.Result{}, fmt.Errorf("checking worktree path %q: %w", path, statErr)
	}
	repoRequest := request
	repoRequest.RunDir = repository.Root
	exists, err := localBranchExists(ctx, repoRequest, runner.config.Branch)
	if err != nil {
		return step.Result{}, err
	}
	args := []string{"worktree", "add"}
	branchCreated := false
	resolvedBase := ""
	if exists {
		args = append(args, path, runner.config.Branch)
	} else {
		remotes, remoteErr := matchingRemoteBranches(ctx, repoRequest, runner.config.Branch)
		if remoteErr != nil {
			return step.Result{}, remoteErr
		}
		if len(remotes) > 1 {
			return step.Result{}, fmt.Errorf("branch %q exists on multiple remotes; create a local branch explicitly", runner.config.Branch)
		}
		if len(remotes) == 1 {
			resolvedBase = remotes[0]
			args = append(args, "--track", "-b", runner.config.Branch, path, remotes[0])
			branchCreated = true
		} else {
			if !runner.config.Create {
				return step.Result{}, fmt.Errorf("branch %q does not exist locally or on exactly one fetched remote; set create: true", runner.config.Branch)
			}
			resolvedBase = runner.config.Base
			if resolvedBase == "" {
				resolvedBase = repository.DefaultBranch
			}
			if resolvedBase == "" {
				return step.Result{}, fmt.Errorf("base is required because no default branch could be detected")
			}
			args = append(args, "-b", runner.config.Branch, path, resolvedBase)
			branchCreated = true
		}
	}
	added, err := runGit(ctx, repoRequest, args...)
	if err != nil {
		return step.Result{}, gitCommandError(fmt.Sprintf("creating worktree for %q", runner.config.Branch), added, err)
	}
	worktreeRequest := request
	worktreeRequest.RunDir = path
	headResult, err := runGit(ctx, worktreeRequest, "rev-parse", "HEAD")
	if err != nil {
		return step.Result{}, gitCommandError("reading created worktree", headResult, err)
	}
	return ensureResult(repository, worktreeState{Path: path, Head: strings.TrimSpace(headResult.Stdout), Branch: runner.config.Branch}, true, branchCreated, resolvedBase), nil
}

func ensureResult(repository repositoryState, worktree worktreeState, created, branchCreated bool, base string) step.Result {
	return step.Result{Outputs: map[string]any{
		"created": created, "branch_created": branchCreated, "base": base,
		"repository_root": repository.Root,
		"worktree":        map[string]any{"path": worktree.Path, "branch": worktree.Branch, "head": worktree.Head, "short_head": shortObjectID(worktree.Head)},
	}}
}

func validateBranch(ctx context.Context, request step.Request, branch string) error {
	result, err := runGit(ctx, request, "check-ref-format", "--branch", branch)
	if err != nil {
		return gitCommandError(fmt.Sprintf("validating branch %q", branch), result, err)
	}
	return nil
}

func worktreePath(root, configured, branch string) (string, error) {
	if configured != "" {
		if filepath.IsAbs(configured) {
			return filepath.Clean(configured), nil
		}
		return filepath.Abs(filepath.Join(root, configured))
	}
	name := strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' || r == '.' {
			return r
		}
		return '-'
	}, branch)
	return filepath.Join(filepath.Dir(root), filepath.Base(root)+"."+name), nil
}

func matchingRemoteBranches(ctx context.Context, request step.Request, branch string) ([]string, error) {
	result, err := runGit(ctx, request, "for-each-ref", "--format=%(refname:short)", "refs/remotes")
	if err != nil {
		return nil, gitCommandError("listing remote Git branches", result, err)
	}
	var matches []string
	for _, candidate := range strings.Fields(result.Stdout) {
		remote, name, found := strings.Cut(candidate, "/")
		if !found || remote == "" || name != branch || name == "HEAD" {
			continue
		}
		matches = append(matches, candidate)
	}
	return matches, nil
}

func (runner *worktreeRunner) remove(ctx context.Context, request step.Request) (step.Result, error) {
	repository, err := loadRepository(ctx, request)
	if err != nil {
		return step.Result{}, err
	}
	target, found := findWorktree(repository, runner.config.Target)
	if !found {
		return step.Result{}, fmt.Errorf("worktree %q was not found", runner.config.Target)
	}
	if target.Path == repository.Worktrees[0].Path {
		return step.Result{}, fmt.Errorf("refusing to remove primary worktree %q", target.Path)
	}
	targetRequest := request
	targetRequest.RunDir = target.Path
	status, err := runGit(ctx, targetRequest, "status", "--porcelain=v1", "-z", "--untracked-files=normal")
	if err != nil {
		return step.Result{}, gitCommandError("checking worktree before removal", status, err)
	}
	if status.Stdout != "" && !runner.config.Force {
		return step.Result{}, fmt.Errorf("worktree %q has uncommitted changes; set force: true to remove it", target.Path)
	}
	repoRequest := request
	repoRequest.RunDir = repository.Root
	canDelete := false
	reason := "disabled"
	if target.Branch == "" {
		reason = "detached"
	} else if runner.config.DeleteBranch == deleteForce {
		canDelete, reason = true, "forced"
	} else if runner.config.DeleteBranch == deleteSafe {
		integration := runner.config.IntegratedInto
		if integration == "" {
			integration = repository.DefaultBranch
		}
		canDelete, reason, err = safelyIntegrated(ctx, repoRequest, target.Branch, integration)
		if err != nil {
			return step.Result{}, err
		}
	}
	args := []string{"worktree", "remove"}
	if runner.config.Force {
		args = append(args, "--force")
	}
	removed, err := runGit(ctx, repoRequest, append(args, target.Path)...)
	if err != nil {
		return step.Result{}, gitCommandError(fmt.Sprintf("removing worktree %q", target.Path), removed, err)
	}
	deleted := false
	if canDelete {
		ref := "refs/heads/" + target.Branch
		remaining, listErr := loadRepository(ctx, repoRequest)
		if listErr != nil {
			return step.Result{}, listErr
		}
		for _, worktree := range remaining.Worktrees {
			if worktree.Branch == target.Branch {
				canDelete, reason = false, "checked_out_elsewhere"
				break
			}
		}
		if canDelete {
			deletedResult, deleteErr := runGit(ctx, repoRequest, "update-ref", "-d", ref, target.Head)
			if deleteErr != nil {
				return step.Result{}, gitCommandError(fmt.Sprintf("deleting branch %q", target.Branch), deletedResult, deleteErr)
			}
			deleted = true
		}
	}
	return step.Result{Outputs: map[string]any{
		"removed": true, "path": target.Path, "branch": target.Branch,
		"branch_deleted": deleted, "branch_delete_reason": reason,
	}}, nil
}

func findWorktree(repository repositoryState, target string) (worktreeState, bool) {
	targetPath, _ := filepath.Abs(target)
	for _, worktree := range repository.Worktrees {
		path, _ := filepath.Abs(worktree.Path)
		if worktree.Branch == target || path == targetPath {
			return worktree, true
		}
	}
	return worktreeState{}, false
}

func safelyIntegrated(ctx context.Context, request step.Request, branch, target string) (bool, string, error) {
	if target == "" {
		return false, "no_integration_target", nil
	}
	branchID, _, err := resolveCommit(ctx, request, "refs/heads/"+branch, false)
	if err != nil {
		return false, "", err
	}
	targetID, _, err := resolveCommit(ctx, request, target, false)
	if err != nil {
		return false, "", err
	}
	if branchID == targetID {
		return true, "same_commit", nil
	}
	if ok, err := gitSuccess(ctx, request, "merge-base", "--is-ancestor", branchID, targetID); err != nil {
		return false, "", err
	} else if ok {
		return true, "ancestor", nil
	}
	if ok, err := gitSuccess(ctx, request, "diff", "--quiet", targetID+"..."+branchID); err != nil {
		return false, "", err
	} else if ok {
		return true, "no_added_changes", nil
	}
	branchTree, err := gitOutput(ctx, request, "rev-parse", branchID+"^{tree}")
	if err != nil {
		return false, "", err
	}
	targetTree, err := gitOutput(ctx, request, "rev-parse", targetID+"^{tree}")
	if err != nil {
		return false, "", err
	}
	if branchTree == targetTree {
		return true, "trees_match", nil
	}
	merged, mergeErr := runGit(ctx, request, "merge-tree", "--write-tree", targetID, branchID)
	if mergeErr == nil {
		fields := strings.Fields(merged.Stdout)
		if len(fields) > 0 && fields[0] == targetTree {
			return true, "merge_adds_nothing", nil
		}
	}
	cherry, cherryErr := runGit(ctx, request, "cherry", targetID, branchID)
	if cherryErr != nil {
		return false, "", gitCommandError("checking patch equivalence", cherry, cherryErr)
	}
	for _, line := range strings.Split(strings.TrimSpace(cherry.Stdout), "\n") {
		if strings.HasPrefix(line, "+") {
			return false, "unintegrated", nil
		}
	}
	return true, "patch_equivalent", nil
}

func gitSuccess(ctx context.Context, request step.Request, args ...string) (bool, error) {
	result, err := runGit(ctx, request, args...)
	if err == nil {
		return true, nil
	}
	var exitErr *process.ExitError
	if errors.As(err, &exitErr) && exitErr.Code == 1 {
		return false, nil
	}
	return false, gitCommandError("running Git predicate", result, err)
}

func gitOutput(ctx context.Context, request step.Request, args ...string) (string, error) {
	result, err := runGit(ctx, request, args...)
	if err != nil {
		return "", gitCommandError("reading Git value", result, err)
	}
	return strings.TrimSpace(result.Stdout), nil
}

func shortObjectID(objectID string) string {
	if len(objectID) <= 12 {
		return objectID
	}
	return objectID[:12]
}
