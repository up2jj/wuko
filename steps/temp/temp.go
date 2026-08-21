// Package temp creates files and directories that the workflow engine removes after a run.
package temp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/up2jj/wuko/step"
)

const (
	kindFile       = "file"
	kindDirectory  = "directory"
	defaultPattern = "wuko-*"
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

func (runner *Runner) Validate(_ context.Context, _ step.Request) error {
	return runner.validate(false)
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
	default:
		panic("validated temp kind")
	}

	if err := ctx.Err(); err != nil {
		if runner.config.Kind == kindDirectory {
			_ = os.RemoveAll(path)
		} else {
			_ = os.Remove(path)
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
	if runner.config.Kind != kindFile && runner.config.Kind != kindDirectory {
		return fmt.Errorf("kind must be file or directory")
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
