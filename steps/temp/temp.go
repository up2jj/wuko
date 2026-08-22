// Package temp creates files, directories, and FIFOs that the workflow engine removes after a run.
package temp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/up2jj/wuko/step"
	"golang.org/x/sys/unix"
)

const (
	kindFile        = "file"
	kindDirectory   = "directory"
	kindFIFO        = "fifo"
	defaultPattern  = "wuko-*"
	fifoRootPattern = "wuko-fifo-*"
)

type Config struct {
	Kind    string `yaml:"kind"`
	Pattern string `yaml:"pattern,omitempty"`
}

type Runner struct {
	config     Config
	hasPattern bool
}

func Register(registry *step.Registry) error { return registry.Register("temp", New) }

func New(raw map[string]any) (step.Runner, error) {
	config := Config{Pattern: defaultPattern}
	if err := step.DecodeConfig(raw, &config); err != nil {
		return nil, err
	}
	_, hasPattern := raw["pattern"]
	runner := &Runner{config: config, hasPattern: hasPattern}
	if err := runner.validate(false); err != nil {
		return nil, err
	}
	return runner, nil
}

func (runner *Runner) Run(ctx context.Context, _ step.Request) (step.Result, error) {
	if err := runner.validate(true); err != nil {
		return step.Result{}, err
	}
	if err := ctx.Err(); err != nil {
		return step.Result{}, err
	}
	tempRoot, err := filepath.Abs(os.TempDir())
	if err != nil {
		return step.Result{}, fmt.Errorf("resolving temporary directory: %w", err)
	}

	var path string
	switch runner.config.Kind {
	case kindFile:
		file, createErr := os.CreateTemp(tempRoot, runner.config.Pattern)
		if createErr != nil {
			return step.Result{}, fmt.Errorf("creating temporary file: %w", createErr)
		}
		path = file.Name()
		if closeErr := file.Close(); closeErr != nil {
			_ = os.Remove(path)
			return step.Result{}, fmt.Errorf("closing temporary file %s: %w", path, closeErr)
		}
	case kindDirectory:
		path, err = os.MkdirTemp(tempRoot, runner.config.Pattern)
		if err != nil {
			return step.Result{}, fmt.Errorf("creating temporary directory: %w", err)
		}
	case kindFIFO:
		path, err = createFIFO(tempRoot, runner.config.Pattern)
		if err != nil {
			return step.Result{}, err
		}
	default:
		panic("validated temp kind")
	}

	if err := ctx.Err(); err != nil {
		result := step.Result{Outputs: map[string]any{"path": path, "kind": runner.config.Kind}}
		if cleanupErr := runner.Cleanup(result); cleanupErr != nil {
			return step.Result{}, errors.Join(err, fmt.Errorf("rolling back temporary resource: %w", cleanupErr))
		}
		return step.Result{}, err
	}
	return step.Result{Outputs: map[string]any{"path": path, "kind": runner.config.Kind}}, nil
}

func (runner *Runner) Cleanup(result step.Result) error {
	path, ok := result.Outputs["path"].(string)
	if !ok || path == "" {
		return fmt.Errorf("temporary result path is missing")
	}
	kind, ok := result.Outputs["kind"].(string)
	if !ok {
		return fmt.Errorf("temporary result kind is missing")
	}
	switch kind {
	case kindFile:
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("inspecting temporary file %s: %w", path, err)
		}
		if info.IsDir() {
			return fmt.Errorf("removing temporary file %s: path is now a directory", path)
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("removing temporary file %s: %w", path, err)
		}
	case kindDirectory:
		if err := os.RemoveAll(path); err != nil {
			return fmt.Errorf("removing temporary directory %s: %w", path, err)
		}
	case kindFIFO:
		if err := cleanupFIFO(path); err != nil {
			return err
		}
	default:
		return fmt.Errorf("temporary result kind %q is invalid", kind)
	}
	return nil
}

func (runner *Runner) validate(resolved bool) error {
	if strings.TrimSpace(runner.config.Kind) == "" {
		return fmt.Errorf("kind is required")
	}
	if !resolved && templated(runner.config.Kind) {
		return runner.validatePattern(false)
	}
	if runner.config.Kind != kindFile && runner.config.Kind != kindDirectory && runner.config.Kind != kindFIFO {
		return fmt.Errorf("kind must be file, directory, or fifo")
	}
	return runner.validatePattern(resolved)
}

func (runner *Runner) validatePattern(resolved bool) error {
	if runner.hasPattern && runner.config.Pattern == "" {
		return fmt.Errorf("pattern must not be empty")
	}
	if !resolved && templated(runner.config.Pattern) {
		return nil
	}
	if strings.ContainsAny(runner.config.Pattern, `/\`) {
		return fmt.Errorf("pattern must not contain path separators")
	}
	return nil
}

func templated(value string) bool { return strings.Contains(value, "{{") }

func createFIFO(tempRoot, pattern string) (path string, err error) {
	root, err := os.MkdirTemp(tempRoot, fifoRootPattern)
	if err != nil {
		return "", fmt.Errorf("creating temporary FIFO directory: %w", err)
	}
	defer func() {
		if err != nil {
			_ = os.RemoveAll(root)
		}
	}()

	if err = os.Chmod(root, 0o700); err != nil {
		return "", fmt.Errorf("securing temporary FIFO directory %s: %w", root, err)
	}
	placeholder, err := os.CreateTemp(root, pattern)
	if err != nil {
		return "", fmt.Errorf("reserving temporary FIFO path: %w", err)
	}
	path = placeholder.Name()
	if closeErr := placeholder.Close(); closeErr != nil {
		return "", fmt.Errorf("closing temporary FIFO placeholder %s: %w", path, closeErr)
	}
	if err = os.Remove(path); err != nil {
		return "", fmt.Errorf("removing temporary FIFO placeholder %s: %w", path, err)
	}
	if err = unix.Mkfifo(path, 0o600); err != nil {
		return "", fmt.Errorf("creating temporary FIFO %s: %w", path, err)
	}
	if err = os.Chmod(path, 0o600); err != nil {
		return "", fmt.Errorf("securing temporary FIFO %s: %w", path, err)
	}
	return path, nil
}

func cleanupFIFO(path string) error {
	root, err := fifoRoot(path)
	if err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return removeFIFOroot(root)
	}
	if err != nil {
		return fmt.Errorf("inspecting temporary FIFO %s: %w", path, err)
	}
	if info.Mode()&os.ModeNamedPipe == 0 {
		return fmt.Errorf("removing temporary FIFO %s: path is no longer a FIFO", path)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("removing temporary FIFO %s: %w", path, err)
	}
	return removeFIFOroot(root)
}

func fifoRoot(path string) (string, error) {
	tempRoot, err := filepath.Abs(os.TempDir())
	if err != nil {
		return "", fmt.Errorf("resolving temporary directory: %w", err)
	}
	cleanPath := filepath.Clean(path)
	root := filepath.Dir(cleanPath)
	if filepath.Dir(root) != tempRoot || !strings.HasPrefix(filepath.Base(root), strings.TrimSuffix(fifoRootPattern, "*")) {
		return "", fmt.Errorf("temporary FIFO path %s is outside a managed directory", path)
	}
	return root, nil
}

func removeFIFOroot(root string) error {
	if err := os.Remove(root); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("removing temporary FIFO directory %s: %w", root, err)
	}
	return nil
}
