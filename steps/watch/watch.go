// Package watch implements cancellation-aware filesystem event waits.
package watch

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/fsnotify/fsnotify"
	"github.com/up2jj/wuko/step"
)

const (
	eventCreate = "create"
	eventModify = "modify"
	eventRename = "rename"
	eventRemove = "remove"
)

var supportedEvents = []eventDefinition{
	{name: eventCreate, op: fsnotify.Create},
	{name: eventModify, op: fsnotify.Write},
	{name: eventRename, op: fsnotify.Rename},
	{name: eventRemove, op: fsnotify.Remove},
}

type eventDefinition struct {
	name string
	op   fsnotify.Op
}

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
	newWatcher func() (eventWatcher, error)
}

type eventWatcher interface {
	Add(string) error
	Close() error
	EventChannel() <-chan fsnotify.Event
	ErrorChannel() <-chan error
}

type nativeWatcher struct{ *fsnotify.Watcher }

func (w nativeWatcher) EventChannel() <-chan fsnotify.Event { return w.Events }
func (w nativeWatcher) ErrorChannel() <-chan error          { return w.Errors }

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
	if !hasRoot {
		config.Root = "."
	}
	runner := &Runner{
		config:    config,
		hasRoot:   hasRoot,
		hasEvents: hasEvents,
		newWatcher: func() (eventWatcher, error) {
			watcher, err := fsnotify.NewWatcher()
			if err != nil {
				return nil, err
			}
			return nativeWatcher{Watcher: watcher}, nil
		},
	}
	if err := runner.validate(false); err != nil {
		return nil, err
	}
	return runner, nil
}

// Run blocks until the first matching notification or context cancellation.
func (r *Runner) Run(ctx context.Context, request step.Request) (result step.Result, runErr error) {
	if err := r.validate(true); err != nil {
		return step.Result{}, err
	}
	if err := ctx.Err(); err != nil {
		return step.Result{}, err
	}
	root, err := resolveRoot(request.RunDir, r.config.Root)
	if err != nil {
		return step.Result{}, err
	}
	info, err := os.Lstat(root)
	if err != nil {
		return step.Result{}, fmt.Errorf("inspecting watch root %s: %w", root, err)
	}
	if !info.IsDir() {
		return step.Result{}, fmt.Errorf("watch root %s is not a directory", root)
	}

	watcher, err := r.newWatcher()
	if err != nil {
		return step.Result{}, fmt.Errorf("creating filesystem watcher: %w", err)
	}
	defer closeWatcher(watcher, &result, &runErr)
	if err := addTree(ctx, watcher, root, false); err != nil {
		return step.Result{}, fmt.Errorf("registering watch root %s: %w", root, err)
	}

	selected := selectedOperations(r.config.Events)
	for {
		select {
		case <-ctx.Done():
			return step.Result{}, ctx.Err()
		case event, ok := <-watcher.EventChannel():
			if !ok {
				return step.Result{}, fmt.Errorf("filesystem watch event channel closed unexpectedly")
			}
			if event.Has(fsnotify.Create) {
				if err := addCreatedTree(ctx, watcher, event.Name); err != nil {
					return step.Result{}, fmt.Errorf("registering created directory %s: %w", event.Name, err)
				}
			}
			relative, matches := r.matchingPath(root, event.Name)
			if !matches {
				continue
			}
			operations := matchingOperations(event, selected)
			if len(operations) == 0 {
				continue
			}
			return step.Result{Outputs: map[string]any{
				"root": root, "path": relative, "operations": operations,
			}}, nil
		case watchErr, ok := <-watcher.ErrorChannel():
			if !ok {
				return step.Result{}, fmt.Errorf("filesystem watch error channel closed unexpectedly")
			}
			if watchErr != nil {
				return step.Result{}, fmt.Errorf("watching filesystem: %w", watchErr)
			}
		}
	}
}

func closeWatcher(watcher eventWatcher, result *step.Result, runErr *error) {
	if err := watcher.Close(); err != nil {
		*result = step.Result{}
		*runErr = errors.Join(*runErr, fmt.Errorf("closing filesystem watcher: %w", err))
	}
}

func (r *Runner) validate(resolved bool) error {
	if r.hasRoot && strings.TrimSpace(r.config.Root) == "" {
		return fmt.Errorf("root must not be empty")
	}
	if resolved && templated(r.config.Root) {
		return fmt.Errorf("watch configuration contains an unresolved template")
	}
	if len(r.config.Patterns) == 0 {
		return fmt.Errorf("patterns must contain at least one pattern")
	}
	for index, pattern := range r.config.Patterns {
		if resolved && templated(pattern) {
			return fmt.Errorf("watch configuration contains an unresolved template")
		}
		if err := validatePattern(pattern); err != nil {
			return fmt.Errorf("patterns[%d]: %w", index, err)
		}
	}
	if r.hasEvents && len(r.config.Events) == 0 {
		return fmt.Errorf("events must contain at least one event")
	}
	if !r.hasEvents {
		r.config.Events = eventNames()
	}
	normalized, err := normalizeEvents(r.config.Events, resolved)
	if err != nil {
		return err
	}
	r.config.Events = normalized
	return nil
}

func validatePattern(pattern string) error {
	if pattern == "" {
		return fmt.Errorf("pattern must not be empty")
	}
	if templated(pattern) {
		return nil
	}
	if path.IsAbs(pattern) || windowsAbsolute(pattern) {
		return fmt.Errorf("pattern must be relative to root")
	}
	for _, component := range strings.Split(pattern, "/") {
		if component == ".." {
			return fmt.Errorf("pattern must not contain a parent directory component")
		}
	}
	if !doublestar.ValidatePattern(pattern) {
		return fmt.Errorf("invalid pattern %q", pattern)
	}
	return nil
}

func normalizeEvents(events []string, resolved bool) ([]string, error) {
	present := make(map[string]bool, len(events))
	var templates []string
	for index, event := range events {
		if templated(event) {
			if resolved {
				return nil, fmt.Errorf("watch configuration contains an unresolved template")
			}
			templates = append(templates, event)
			continue
		}
		if !slices.Contains(eventNames(), event) {
			return nil, fmt.Errorf("events[%d] must be create, modify, rename, or remove", index)
		}
		present[event] = true
	}
	normalized := make([]string, 0, len(present)+len(templates))
	for _, event := range eventNames() {
		if present[event] {
			normalized = append(normalized, event)
		}
	}
	return append(normalized, templates...), nil
}

func eventNames() []string {
	names := make([]string, len(supportedEvents))
	for index, event := range supportedEvents {
		names[index] = event.name
	}
	return names
}

func selectedOperations(events []string) map[string]bool {
	selected := make(map[string]bool, len(events))
	for _, event := range events {
		selected[event] = true
	}
	return selected
}

func matchingOperations(event fsnotify.Event, selected map[string]bool) []any {
	operations := make([]any, 0, len(supportedEvents))
	for _, definition := range supportedEvents {
		if selected[definition.name] && event.Has(definition.op) {
			operations = append(operations, definition.name)
		}
	}
	return operations
}

func (r *Runner) matchingPath(root, name string) (string, bool) {
	absolute, err := filepath.Abs(name)
	if err != nil {
		return "", false
	}
	relative, err := filepath.Rel(root, absolute)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", false
	}
	relative = filepath.ToSlash(relative)
	for _, pattern := range r.config.Patterns {
		if matchNoHidden(pattern, relative) {
			return relative, true
		}
	}
	return "", false
}

func matchNoHidden(pattern, name string) bool {
	return matchComponents(strings.Split(path.Clean(pattern), "/"), strings.Split(path.Clean(name), "/"))
}

func matchComponents(pattern, name []string) bool {
	if len(pattern) == 0 {
		return len(name) == 0
	}
	if pattern[0] == "**" {
		if matchComponents(pattern[1:], name) {
			return true
		}
		return len(name) > 0 && !strings.HasPrefix(name[0], ".") && matchComponents(pattern, name[1:])
	}
	if len(name) == 0 {
		return false
	}
	if strings.HasPrefix(name[0], ".") && (strings.HasPrefix(pattern[0], "*") || strings.HasPrefix(pattern[0], "?")) {
		return false
	}
	matched, err := doublestar.Match(pattern[0], name[0])
	return err == nil && matched && matchComponents(pattern[1:], name[1:])
}

func addTree(ctx context.Context, watcher eventWatcher, root string, allowMissing bool) error {
	err := filepath.WalkDir(root, func(current string, entry fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			if allowMissing && errors.Is(walkErr, os.ErrNotExist) {
				return fs.SkipDir
			}
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if !entry.IsDir() {
			return nil
		}
		if err := watcher.Add(current); err != nil {
			if allowMissing && errors.Is(err, os.ErrNotExist) {
				return fs.SkipDir
			}
			return fmt.Errorf("adding directory %s: %w", current, err)
		}
		return nil
	})
	if allowMissing && errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func addCreatedTree(ctx context.Context, watcher eventWatcher, created string) error {
	info, err := os.Lstat(created)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil
	}
	return addTree(ctx, watcher, created, true)
}

func resolveRoot(runDir, root string) (string, error) {
	if filepath.IsAbs(root) {
		return filepath.Clean(root), nil
	}
	if runDir == "" {
		var err error
		runDir, err = os.Getwd()
		if err != nil {
			return "", fmt.Errorf("finding run directory: %w", err)
		}
	}
	value, err := filepath.Abs(filepath.Join(runDir, root))
	if err != nil {
		return "", fmt.Errorf("resolving watch root %s: %w", root, err)
	}
	return filepath.Clean(value), nil
}

func windowsAbsolute(value string) bool {
	if strings.HasPrefix(value, `\\`) {
		return true
	}
	return len(value) >= 3 && ((value[0] >= 'a' && value[0] <= 'z') || (value[0] >= 'A' && value[0] <= 'Z')) && value[1] == ':' && (value[2] == '/' || value[2] == '\\')
}

func templated(value string) bool { return strings.Contains(value, "{{") }
