package file

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"

	"github.com/up2jj/wuko/executor"
	"github.com/up2jj/wuko/step"
)

// remoteOperations are the operations expressible through executor.FileSystem, and so the
// only ones allowed inside an executor scope. The rest depend on host semantics the
// interface deliberately omits -- symbolic and hard links, renames across filesystems,
// directory walks, disk usage -- and are rejected during validation rather than run
// against the wrong machine.
var remoteOperations = []string{operationRead, operationWrite, operationMkdir, operationStat}

// executorAwareRunner is the runner returned for a remote-capable operation. The marker
// lives on a wrapper rather than on Runner itself so that a file step configured for a
// host-only operation still fails validation inside an executor scope.
type executorAwareRunner struct{ *Runner }

func (*executorAwareRunner) ExecutorAware()      {}
func (*executorAwareRunner) ExecutorFileSystem() {}

var _ step.ExecutorAware = (*executorAwareRunner)(nil)

// runRemote performs one operation against the execution target's filesystem, reporting
// the same outputs as its local counterpart.
func (r *Runner) runRemote(ctx context.Context, target executor.FileSystem, path string) (step.Result, error) {
	switch r.config.Operation {
	case operationRead:
		return r.readRemote(ctx, target, path)
	case operationWrite:
		return r.writeRemote(ctx, target, path)
	case operationMkdir:
		return r.mkdirRemote(ctx, target, path)
	case operationStat:
		return r.statRemote(ctx, target, path)
	default:
		return step.Result{}, fmt.Errorf("file operation %q is not supported inside executor blocks", r.config.Operation)
	}
}

func (r *Runner) readRemote(ctx context.Context, target executor.FileSystem, path string) (step.Result, error) {
	content, err := target.ReadFile(ctx, path, 0)
	if err != nil {
		return step.Result{}, fmt.Errorf("reading file %s: %w", path, err)
	}
	return step.Result{Outputs: map[string]any{"path": path, "content": string(content), "size": int64(len(content))}}, nil
}

func (r *Runner) writeRemote(ctx context.Context, target executor.FileSystem, path string) (step.Result, error) {
	mode := os.FileMode(0o644)
	info, err := target.Stat(ctx, path)
	exists := err == nil
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return step.Result{}, fmt.Errorf("inspecting destination %s: %w", path, err)
	}
	if exists {
		if !r.config.Overwrite {
			return step.Result{}, fmt.Errorf("destination %s already exists; set overwrite to true", path)
		}
		if info.Mode.IsDir() {
			return step.Result{}, fmt.Errorf("destination %s is a directory", path)
		}
		if info.Mode.IsRegular() {
			mode = info.Mode.Perm()
		}
	}
	if r.config.Mode != "" {
		mode, err = parseMode(r.config.Mode)
		if err != nil {
			return step.Result{}, err
		}
	}
	if err := target.WriteFile(ctx, path, []byte(r.config.Content), executor.WriteOptions{Mode: mode, Replace: r.config.Overwrite}); err != nil {
		if !r.config.Overwrite && errors.Is(err, fs.ErrExist) {
			return step.Result{}, fmt.Errorf("destination %s already exists; set overwrite to true", path)
		}
		return step.Result{}, err
	}
	return step.Result{Outputs: map[string]any{
		"path": path, "size": int64(len(r.config.Content)), "mode": formatMode(mode), "created": !exists,
	}}, nil
}

func (r *Runner) mkdirRemote(ctx context.Context, target executor.FileSystem, path string) (step.Result, error) {
	mode := os.FileMode(0o755)
	var err error
	if r.config.Mode != "" {
		mode, err = parseMode(r.config.Mode)
		if err != nil {
			return step.Result{}, err
		}
	}
	info, err := target.Stat(ctx, path)
	if err == nil {
		if !info.Mode.IsDir() {
			return step.Result{}, fmt.Errorf("path %s exists and is not a directory", path)
		}
		return step.Result{Outputs: map[string]any{"path": path, "created": false, "mode": formatMode(info.Mode)}}, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return step.Result{}, fmt.Errorf("inspecting directory %s: %w", path, err)
	}
	// A remote filesystem creates missing parents as one archive, so a non-recursive
	// request checks the parent itself to keep the local error.
	if !r.config.Recursive {
		parent := filepath.Dir(path)
		if _, err := target.Stat(ctx, parent); err != nil {
			return step.Result{}, fmt.Errorf("creating directory %s: %w", path, err)
		}
	}
	if err := target.MkdirAll(ctx, path, mode); err != nil {
		return step.Result{}, fmt.Errorf("creating directory %s: %w", path, err)
	}
	return step.Result{Outputs: map[string]any{"path": path, "created": true, "mode": formatMode(mode)}}, nil
}

func (r *Runner) statRemote(ctx context.Context, target executor.FileSystem, path string) (step.Result, error) {
	info, err := target.Stat(ctx, path)
	if errors.Is(err, fs.ErrNotExist) {
		return step.Result{Outputs: map[string]any{"path": path, "exists": false}}, nil
	}
	if err != nil {
		return step.Result{}, fmt.Errorf("inspecting path %s: %w", path, err)
	}
	outputs := entryOutputs(path, info.Size, info.Mode, info.ModTime)
	outputs["path"] = path
	outputs["exists"] = true
	return step.Result{Outputs: outputs}, nil
}

func remoteCapable(operation string) bool { return slices.Contains(remoteOperations, operation) }
