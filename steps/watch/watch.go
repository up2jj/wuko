// Package watch implements cancellation-aware one-shot filesystem waits.
package watch

import (
	"context"
	"errors"
	"fmt"

	"github.com/fsnotify/fsnotify"
	"github.com/up2jj/wuko/fswatch"
	"github.com/up2jj/wuko/step"
)

// Config selects the filesystem notifications that complete one watch.
type Config struct {
	Root     string   `yaml:"root,omitempty"`
	Patterns []string `yaml:"patterns"`
	Events   []string `yaml:"events,omitempty"`
}

// Runner waits for the first selected filesystem notification.
type Runner struct {
	config     Config
	hasRoot    bool
	hasEvents  bool
	newWatcher fswatch.Factory
}

type eventWatcher = fswatch.Source

type nativeWatcher struct{ *fsnotify.Watcher }

func (watcher nativeWatcher) EventChannel() <-chan fsnotify.Event { return watcher.Events }
func (watcher nativeWatcher) ErrorChannel() <-chan error          { return watcher.Errors }

// Register adds the watch step to a registry.
func Register(registry *step.Registry) error { return registry.Register("watch", New) }

// New decodes and validates a watch step configuration.
func New(raw map[string]any) (step.Runner, error) {
	var config Config
	if err := step.DecodeConfig(raw, &config); err != nil {
		return nil, err
	}
	_, hasRoot := raw["root"]
	_, hasEvents := raw["events"]
	normalized, err := fswatch.Normalize(fswatch.Config{
		Root: config.Root, Patterns: config.Patterns, Events: config.Events,
	}, hasRoot, hasEvents, false)
	if err != nil {
		return nil, err
	}
	return &Runner{
		config:  Config{Root: normalized.Root, Patterns: normalized.Patterns, Events: normalized.Events},
		hasRoot: hasRoot, hasEvents: hasEvents, newWatcher: fswatch.NativeFactory,
	}, nil
}

// Run blocks until the first matching notification or context cancellation.
func (runner *Runner) Run(ctx context.Context, request step.Request) (result step.Result, runErr error) {
	normalized, err := fswatch.Normalize(fswatch.Config{
		Root: runner.config.Root, Patterns: runner.config.Patterns, Events: runner.config.Events,
	}, runner.hasRoot, runner.hasEvents, true)
	if err != nil {
		return step.Result{}, err
	}
	observer, err := fswatch.Open(ctx, request.RunDir, normalized, runner.newWatcher)
	if err != nil {
		return step.Result{}, err
	}
	defer func() {
		if closeErr := observer.Close(); closeErr != nil {
			result = step.Result{}
			runErr = errors.Join(runErr, fmt.Errorf("closing filesystem watcher: %w", closeErr))
		}
	}()
	change, err := observer.Next(ctx)
	if err != nil {
		return step.Result{}, err
	}
	operations := make([]any, len(change.Operations))
	for index, operation := range change.Operations {
		operations[index] = operation
	}
	return step.Result{Outputs: map[string]any{
		"root": observer.Root(), "path": change.Path, "operations": operations,
	}}, nil
}
