package cmd

import (
	"context"
	"errors"
	"sync"

	"github.com/up2jj/wuko/process"
	"github.com/up2jj/wuko/workflow"
)

type processReplacementKey struct{}

// processReplacement records the program a run asked to replace Wuko with. Execute owns it and
// performs the replacement only after the command has returned, so every deferred cleanup in
// the command tree - plugin teardown, worktree restoration, reporters - has already run.
type processReplacement struct {
	mu      sync.Mutex
	pending *process.Replacement
}

func withProcessReplacement(ctx context.Context, replacement *processReplacement) context.Context {
	return context.WithValue(ctx, processReplacementKey{}, replacement)
}

// processReplacementFrom returns the replacement owned by the surrounding Execute, or nil when
// the command runs embedded, as in tests, and so cannot replace the process.
func processReplacementFrom(ctx context.Context) *processReplacement {
	replacement, _ := ctx.Value(processReplacementKey{}).(*processReplacement)
	return replacement
}

func (replacement *processReplacement) request(program process.Replacement) error {
	replacement.mu.Lock()
	defer replacement.mu.Unlock()
	if replacement.pending != nil {
		return errors.New("a process replacement is already pending")
	}
	replacement.pending = &program
	return nil
}

func (replacement *processReplacement) requested() bool {
	if replacement == nil {
		return false
	}
	replacement.mu.Lock()
	defer replacement.mu.Unlock()
	return replacement.pending != nil
}

func (replacement *processReplacement) take() *process.Replacement {
	replacement.mu.Lock()
	defer replacement.mu.Unlock()
	pending := replacement.pending
	replacement.pending = nil
	return pending
}

// previewProcessReplacement stands in for the hook when a workflow is only validated or
// previewed, so a replacing final step validates exactly as it would for wuko run.
func previewProcessReplacement(process.Replacement) error {
	return errors.New("process replacement is unavailable while previewing a workflow")
}

// replacementFor returns hook for the selected workflow of a plan when that workflow may end the
// invocation by replacing the process. A schedule keeps running, so it never may.
func replacementFor(definition, root *workflow.Definition, scheduled bool, hook func(process.Replacement) error) func(process.Replacement) error {
	if definition != root || scheduled {
		return nil
	}
	return hook
}

// replaceAfter runs execute and then, if it succeeded with a pending replacement, replaces the
// process through replace. A failed command never replaces the process.
func replaceAfter(execute func(context.Context) error, replace func(process.Replacement) error) func(context.Context) error {
	return func(ctx context.Context) error {
		replacement := &processReplacement{}
		if err := execute(withProcessReplacement(ctx, replacement)); err != nil {
			return err
		}
		if program := replacement.take(); program != nil {
			return replace(*program)
		}
		return nil
	}
}
