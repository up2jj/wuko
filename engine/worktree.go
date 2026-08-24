package engine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/up2jj/wuko/diagnostic"
	"github.com/up2jj/wuko/process"
	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/workflow"
)

const worktreeCleanupTimeout = 5 * time.Second

func (e *Engine) validateWorktreeBlock(ctx context.Context, definition *workflow.Definition, block workflow.Step, options Options, state *State) error {
	started := time.Now()
	trace(options, diagnostic.Event{
		Phase: diagnostic.PhaseValidation, Status: diagnostic.StatusStarted, Time: started,
		WorkflowName: definition.Name, Location: block.Location, Message: "validating worktree block",
	})
	fail := func(err error) error {
		trace(options, diagnostic.Event{
			Phase: diagnostic.PhaseValidation, Status: diagnostic.StatusFailed, Time: time.Now(), Duration: time.Since(started),
			WorkflowName: definition.Name, Location: block.Location, Error: err,
		})
		return fmt.Errorf("worktree block: %w", err)
	}
	if err := block.Worktree.Validate(); err != nil {
		return fail(err)
	}
	if err := validateTemplates(options.renderer, block.Worktree.Revision, false); err != nil {
		return fail(fmt.Errorf("revision template: %w", err))
	}
	if block.Worktree.Path != "auto" {
		if err := validateTemplates(options.renderer, block.Worktree.Path, false); err != nil {
			return fail(fmt.Errorf("path template: %w", err))
		}
	}
	if block.Worktree.Publish != nil {
		if err := validateTemplates(options.renderer, block.Worktree.Publish.Branch, false); err != nil {
			return fail(fmt.Errorf("publish branch template: %w", err))
		}
	}
	childOptions := options
	childOptions.depth++
	childOptions.deferContextValidation = true
	if err := e.validateSteps(ctx, definition, block.Worktree.Steps, childOptions, state); err != nil {
		return fail(err)
	}
	trace(options, diagnostic.Event{
		Phase: diagnostic.PhaseValidation, Status: diagnostic.StatusSucceeded, Time: time.Now(), Duration: time.Since(started),
		WorkflowName: definition.Name, Location: block.Location, Message: "validated worktree block",
	})
	return nil
}

func (e *Engine) executeWorktreeBlock(ctx context.Context, definition *workflow.Definition, block workflow.Step, options Options, state *State, index, total int) (outcome stepOutcome) {
	if err := ctx.Err(); err != nil {
		outcome.err = err
		return outcome
	}
	startedAt := time.Now()
	outcome.started = true
	bodyMainTotal := leafStepCount(block.Worktree.Steps)
	bodyTotal := bodyMainTotal + newDeferStack(block.Worktree.Steps).stepCount()
	bodyStats := RunStats{StartedAt: startedAt, Total: bodyTotal, Steps: make([]StepStats, 0, bodyTotal)}
	outcome.nested = &bodyStats
	var cleanup func() error
	defer func() {
		if cleanup != nil {
			cleanupErr := cleanup()
			if cleanupErr != nil {
				outcome.err = errors.Join(outcome.err, cleanupErr)
			}
		}
		bodyStats.FinishedAt = time.Now()
		bodyStats.Duration = bodyStats.FinishedAt.Sub(bodyStats.StartedAt)
		status := statusFromError(outcome.err)
		iteration := IterationStats{Index: 0, Status: status, StartedAt: bodyStats.StartedAt, Duration: bodyStats.Duration, Error: outcome.err, Steps: bodyStats.Steps}
		outcome.stats = StepStats{
			ID: block.ID, Type: "worktree", Index: index, Status: status,
			StartedAt: startedAt, Duration: time.Since(startedAt), Error: outcome.err,
			Iterations: []IterationStats{iteration},
		}
		reportStepFinished(options, definition.Name, block.ID, "worktree", index, total, outcome.stats)
	}()

	renderedRevision, err := options.renderer.Render(block.Worktree.Revision, templateData(definition, options.RunDir, state))
	if err != nil {
		outcome.err = fmt.Errorf("workflow %q worktree %q: rendering revision: %w", definition.Name, block.ID, err)
		return outcome
	}
	renderedPath := block.Worktree.Path
	if renderedPath != "auto" {
		renderedPath, err = options.renderer.Render(renderedPath, templateData(definition, options.RunDir, state))
		if err != nil {
			outcome.err = fmt.Errorf("workflow %q worktree %q: rendering path: %w", definition.Name, block.ID, err)
			return outcome
		}
	}
	path, err := prepareWorktreePath(options.RunDir, renderedPath)
	if err != nil {
		outcome.err = fmt.Errorf("workflow %q worktree %q: %w", definition.Name, block.ID, err)
		return outcome
	}
	repoOutput, err := e.runGit(ctx, options, state, options.RunDir, "rev-parse", "--show-toplevel")
	if err != nil {
		outcome.err = fmt.Errorf("workflow %q worktree %q: locating repository: %w", definition.Name, block.ID, err)
		return outcome
	}
	repoRoot := strings.TrimSpace(repoOutput)
	if repoRoot == "" {
		outcome.err = fmt.Errorf("workflow %q worktree %q: git returned an empty repository root", definition.Name, block.ID)
		return outcome
	}
	if strings.HasPrefix(strings.TrimSpace(renderedRevision), "-") {
		outcome.err = fmt.Errorf("workflow %q worktree %q: revision must not begin with '-'", definition.Name, block.ID)
		return outcome
	}
	if _, err := e.runGit(ctx, options, state, repoRoot, "worktree", "add", "--detach", path, strings.TrimSpace(renderedRevision)); err != nil {
		outcome.err = fmt.Errorf("workflow %q worktree %q: creating worktree: %w", definition.Name, block.ID, err)
		return outcome
	}
	cleanup = func() error {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), worktreeCleanupTimeout)
		defer cancel()
		_, removeErr := e.runGit(cleanupCtx, options, state, repoRoot, "worktree", "remove", "--force", path)
		_, pruneErr := e.runGit(cleanupCtx, options, state, repoRoot, "worktree", "prune")
		if removeErr != nil {
			removeErr = fmt.Errorf("removing worktree %s: %w", path, removeErr)
		}
		if pruneErr != nil {
			pruneErr = fmt.Errorf("pruning worktrees: %w", pruneErr)
		}
		return errors.Join(removeErr, pruneErr)
	}
	baseOutput, err := e.runGit(ctx, options, state, path, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		outcome.err = fmt.Errorf("workflow %q worktree %q: resolving base commit: %w", definition.Name, block.ID, err)
		return outcome
	}
	baseCommit := strings.TrimSpace(baseOutput)
	childOptions := options
	childOptions.RunDir = path
	childOptions.depth++
	childOptions.deferContextValidation = false
	childOptions.defers = newDeferStack(block.Worktree.Steps)
	if err := e.validateSteps(ctx, definition, block.Worktree.Steps, childOptions, state); err != nil {
		outcome.err = fmt.Errorf("workflow %q worktree %q: validating scoped steps: %w", definition.Name, block.ID, err)
		return outcome
	}
	mainErr := e.executeSequence(ctx, definition, block.Worktree.Steps, childOptions, state, &bodyStats, 1, bodyTotal)
	returning := state.returning
	state.returning = false
	cleanupErrors := e.executeCleanupScope(context.WithoutCancel(ctx), definition, childOptions.defers, nil, childOptions, state, &bodyStats, mainErr, bodyStats.Steps, bodyMainTotal+1, bodyTotal)
	state.returning = returning
	if mainErr != nil || len(cleanupErrors) > 0 {
		outcome.err = errors.Join(append([]error{mainErr}, cleanupErrors...)...)
		return outcome
	}
	finalOutput, err := e.runGit(ctx, options, state, path, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		outcome.err = fmt.Errorf("workflow %q worktree %q: resolving final commit: %w", definition.Name, block.ID, err)
		return outcome
	}
	finalCommit := strings.TrimSpace(finalOutput)
	branch := ""
	published := false
	if block.Worktree.Publish != nil {
		branch, err = options.renderer.Render(block.Worktree.Publish.Branch, templateData(definition, path, state))
		if err != nil {
			outcome.err = fmt.Errorf("workflow %q worktree %q: rendering publish branch: %w", definition.Name, block.ID, err)
			return outcome
		}
		branch = strings.TrimSpace(branch)
		if _, err := e.runGit(ctx, options, state, repoRoot, "check-ref-format", "--branch", branch); err != nil {
			outcome.err = fmt.Errorf("workflow %q worktree %q: invalid publish branch %q: %w", definition.Name, block.ID, branch, err)
			return outcome
		}
		if finalCommit == baseCommit {
			outcome.err = fmt.Errorf("workflow %q worktree %q: publish requested but nested steps created no commit", definition.Name, block.ID)
			return outcome
		}
		if _, err := e.runGit(ctx, options, state, repoRoot, "show-ref", "--verify", "--quiet", "--", "refs/heads/"+branch); err == nil {
			outcome.err = fmt.Errorf("workflow %q worktree %q: publish branch %q already exists", definition.Name, block.ID, branch)
			return outcome
		}
		if _, err := e.runGit(ctx, options, state, repoRoot, "branch", "--", branch, finalCommit); err != nil {
			outcome.err = fmt.Errorf("workflow %q worktree %q: publishing branch %q: %w", definition.Name, block.ID, branch, err)
			return outcome
		}
		published = true
	}
	outcome.result = step.Result{Outputs: map[string]any{
		"path": path, "revision": strings.TrimSpace(renderedRevision), "base_commit": baseCommit,
		"commit": finalCommit, "branch": branch, "published": published,
	}}
	return outcome
}

func prepareWorktreePath(base, configured string) (string, error) {
	if strings.TrimSpace(configured) == "" || strings.TrimSpace(configured) == "auto" {
		if strings.TrimSpace(base) == "" {
			base = os.TempDir()
		}
		path, err := os.MkdirTemp(base, ".wuko-worktree-")
		if err != nil {
			return "", fmt.Errorf("allocating temporary worktree path: %w", err)
		}
		if err := os.Remove(path); err != nil {
			return "", fmt.Errorf("reserving temporary worktree path %s: %w", path, err)
		}
		return path, nil
	}
	path := configured
	if !filepath.IsAbs(path) {
		path = filepath.Join(base, path)
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolving worktree path: %w", err)
	}
	path = filepath.Clean(path)
	if _, err := os.Lstat(path); err == nil {
		return "", fmt.Errorf("worktree path %s already exists", path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("inspecting worktree path %s: %w", path, err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", fmt.Errorf("creating worktree parent directory: %w", err)
	}
	return path, nil
}

func (e *Engine) runGit(ctx context.Context, options Options, state *State, dir string, args ...string) (string, error) {
	runner := options.Executor
	if runner == nil {
		runner = process.LocalExecutor{}
	}
	var stdout, stderr bytes.Buffer
	result, err := runner.Run(ctx, process.Options{
		Command: "git", Args: args, Dir: dir, Env: maps.Clone(state.Env),
		Stdout: &stdout, Stderr: &stderr,
	})
	if err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = strings.TrimSpace(result.Stderr)
		}
		if detail != "" {
			return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, detail)
		}
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	output := strings.TrimSpace(stdout.String())
	if output == "" {
		output = strings.TrimSpace(result.Stdout)
	}
	return output, nil
}
