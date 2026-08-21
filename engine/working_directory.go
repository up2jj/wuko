package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/up2jj/wuko/diagnostic"
	"github.com/up2jj/wuko/workflow"
)

func (e *Engine) validateWorkingDirectoryBlock(ctx context.Context, definition *workflow.Definition, block workflow.Step, options Options, state *State, insideConcurrent bool) error {
	started := time.Now()
	trace(options, diagnostic.Event{
		Phase: diagnostic.PhaseValidation, Status: diagnostic.StatusStarted, Time: started,
		WorkflowName: definition.Name, Location: block.Location, Message: "validating working_directory block",
	})
	fail := func(err error) error {
		trace(options, diagnostic.Event{
			Phase: diagnostic.PhaseValidation, Status: diagnostic.StatusFailed, Time: time.Now(), Duration: time.Since(started),
			WorkflowName: definition.Name, Location: block.Location, Error: err,
		})
		return err
	}
	if block.ID != "" || block.Type != "" || !block.Uses.Empty() || block.Require != nil || block.Concurrent != nil || block.Foreach != nil || block.Matrix != nil || block.SHA256 != "" || block.If != "" || block.Timeout != nil || block.Retry != nil || block.With != nil {
		return fail(fmt.Errorf("working_directory block cannot be combined with other step fields"))
	}
	if strings.TrimSpace(block.WorkingDirectory) == "" {
		return fail(fmt.Errorf("working_directory must be a non-empty path"))
	}
	if len(block.Steps) == 0 {
		return fail(fmt.Errorf("working_directory block must contain at least one step"))
	}
	if err := validateTemplates(options.renderer, block.WorkingDirectory, false); err != nil {
		return fail(fmt.Errorf("working_directory template: %w", err))
	}
	childOptions := options
	rendered, renderErr := options.renderer.Render(block.WorkingDirectory, templateData(definition, options.RunDir, state))
	if renderErr == nil && strings.TrimSpace(rendered) != "" && !strings.Contains(rendered, "<no value>") {
		childOptions.RunDir = cleanWorkingDirectory(options.RunDir, rendered)
	} else {
		childOptions.deferContextValidation = true
	}
	if err := e.validateSteps(ctx, definition, block.Steps, childOptions, state, insideConcurrent); err != nil {
		return fail(fmt.Errorf("working_directory block: %w", err))
	}
	trace(options, diagnostic.Event{
		Phase: diagnostic.PhaseValidation, Status: diagnostic.StatusSucceeded, Time: time.Now(), Duration: time.Since(started),
		WorkflowName: definition.Name, Location: block.Location,
	})
	return nil
}

func (e *Engine) executeWorkingDirectoryBlock(ctx context.Context, definition *workflow.Definition, block workflow.Step, options Options, state *State, stats *RunStats, firstIndex, total int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	started := time.Now()
	trace(options, diagnostic.Event{
		Phase: diagnostic.PhaseRender, Status: diagnostic.StatusStarted, Time: started,
		WorkflowName: definition.Name, Location: block.Location, Message: "rendering working_directory",
	})
	rendered, err := options.renderer.Render(block.WorkingDirectory, templateData(definition, options.RunDir, state))
	if err != nil {
		trace(options, diagnostic.Event{
			Phase: diagnostic.PhaseRender, Status: diagnostic.StatusFailed, Time: time.Now(), Duration: time.Since(started),
			WorkflowName: definition.Name, Location: block.Location, Error: err,
		})
		return fmt.Errorf("workflow %q working_directory block: rendering path: %w", definition.Name, err)
	}
	dir, err := resolveWorkingDirectory(options.RunDir, rendered)
	if err != nil {
		trace(options, diagnostic.Event{
			Phase: diagnostic.PhaseRender, Status: diagnostic.StatusFailed, Time: time.Now(), Duration: time.Since(started),
			WorkflowName: definition.Name, Location: block.Location, Error: err,
		})
		return fmt.Errorf("workflow %q working_directory block: %w", definition.Name, err)
	}
	trace(options, diagnostic.Event{
		Phase: diagnostic.PhaseRender, Status: diagnostic.StatusSucceeded, Time: time.Now(), Duration: time.Since(started),
		WorkflowName: definition.Name, Location: block.Location, Message: dir,
	})
	childOptions := options
	childOptions.RunDir = dir
	childOptions.deferContextValidation = false
	if err := e.validateSteps(ctx, definition, block.Steps, childOptions, state, false); err != nil {
		return fmt.Errorf("workflow %q working_directory block: validating scoped steps: %w", definition.Name, err)
	}
	return e.executeSequence(ctx, definition, block.Steps, childOptions, state, stats, firstIndex, total)
}

func resolveWorkingDirectory(base, value string) (string, error) {
	if strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("resolved path must not be empty")
	}
	dir := cleanWorkingDirectory(base, value)
	info, err := os.Stat(dir)
	if err != nil {
		return "", fmt.Errorf("inspecting directory %s: %w", dir, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("working_directory %s is not a directory", dir)
	}
	return dir, nil
}

func cleanWorkingDirectory(base, value string) string {
	dir := value
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(base, dir)
	}
	return filepath.Clean(dir)
}
