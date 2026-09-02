// Package fswatch provides cancellation-aware native filesystem observations.
package fswatch

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
	"sync"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/fsnotify/fsnotify"
)

const (
	EventCreate = "create"
	EventModify = "modify"
	EventRename = "rename"
	EventRemove = "remove"
)

type eventDefinition struct {
	name string
	op   fsnotify.Op
}

var supportedEvents = []eventDefinition{
	{name: EventCreate, op: fsnotify.Create},
	{name: EventModify, op: fsnotify.Write},
	{name: EventRename, op: fsnotify.Rename},
	{name: EventRemove, op: fsnotify.Remove},
}

type Config struct {
	Root     string
	Patterns []string
	// Ignore excludes paths that Patterns would otherwise reach. A directory matching an
	// ignore pattern is never registered, which is the point: pattern-derived pruning can
	// only drop what no pattern can match, and "**/*.go" genuinely can match inside
	// node_modules. Excluding it needs the user to say so.
	Ignore []string
	Events []string
}

type Change struct {
	Path       string
	Operations []string
}

// Source is the native event capability consumed by Observer.
type Source interface {
	Add(string) error
	Close() error
	EventChannel() <-chan fsnotify.Event
	ErrorChannel() <-chan error
}

type nativeSource struct{ *fsnotify.Watcher }

func (source nativeSource) EventChannel() <-chan fsnotify.Event { return source.Events }
func (source nativeSource) ErrorChannel() <-chan error          { return source.Errors }

type Factory func() (Source, error)

func NativeFactory() (Source, error) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	return nativeSource{Watcher: watcher}, nil
}

// Normalize validates a declaration and applies root and event defaults.
func Normalize(config Config, hasRoot, hasEvents, resolved bool) (Config, error) {
	if !hasRoot {
		config.Root = "."
	}
	if hasRoot && strings.TrimSpace(config.Root) == "" {
		return Config{}, fmt.Errorf("root must not be empty")
	}
	if resolved && templated(config.Root) {
		return Config{}, fmt.Errorf("watch configuration contains an unresolved template")
	}
	if len(config.Patterns) == 0 {
		return Config{}, fmt.Errorf("patterns must contain at least one pattern")
	}
	for index, pattern := range config.Patterns {
		if resolved && templated(pattern) {
			return Config{}, fmt.Errorf("watch configuration contains an unresolved template")
		}
		if err := validatePattern(pattern); err != nil {
			return Config{}, fmt.Errorf("patterns[%d]: %w", index, err)
		}
	}
	for index, pattern := range config.Ignore {
		if resolved && templated(pattern) {
			return Config{}, fmt.Errorf("watch configuration contains an unresolved template")
		}
		if err := validatePattern(pattern); err != nil {
			return Config{}, fmt.Errorf("ignore[%d]: %w", index, err)
		}
	}
	if hasEvents && len(config.Events) == 0 {
		return Config{}, fmt.Errorf("events must contain at least one event")
	}
	if !hasEvents {
		config.Events = EventNames()
	}
	events, err := normalizeEvents(config.Events, resolved)
	if err != nil {
		return Config{}, err
	}
	config.Events = events
	return config, nil
}

func EventNames() []string {
	names := make([]string, len(supportedEvents))
	for index, event := range supportedEvents {
		names[index] = event.name
	}
	return names
}

type Observer struct {
	root   string
	config Config
	// patterns is config.Patterns pre-split into components, the form both matching and watch
	// registration consume. Splitting once keeps the two in step and keeps the per-event path
	// out of the allocator.
	patterns  [][]string
	ignore    [][]string
	selected  map[string]bool
	source    Source
	closeOnce sync.Once
	closeErr  error
}

// Open validates the root and registers its complete existing directory tree.
func Open(ctx context.Context, runDir string, config Config, factory Factory) (*Observer, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	root, err := resolveRoot(runDir, config.Root)
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(root)
	if err != nil {
		return nil, fmt.Errorf("inspecting watch root %s: %w", root, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("watch root %s is not a directory", root)
	}
	if factory == nil {
		factory = NativeFactory
	}
	source, err := factory()
	if err != nil {
		return nil, fmt.Errorf("creating filesystem watcher: %w", err)
	}
	patterns := patternComponents(config.Patterns)
	ignore := patternComponents(config.Ignore)
	if err := addTree(ctx, source, patterns, ignore, root, root, false); err != nil {
		_ = source.Close()
		return nil, fmt.Errorf("registering watch root %s: %w", root, err)
	}
	selected := make(map[string]bool, len(config.Events))
	for _, event := range config.Events {
		selected[event] = true
	}
	return &Observer{root: root, config: config, patterns: patterns, ignore: ignore, selected: selected, source: source}, nil
}

func (observer *Observer) Root() string { return observer.root }

func (observer *Observer) Next(ctx context.Context) (Change, error) {
	for {
		select {
		case <-ctx.Done():
			return Change{}, ctx.Err()
		case event, ok := <-observer.source.EventChannel():
			if !ok {
				return Change{}, fmt.Errorf("filesystem watch event channel closed unexpectedly")
			}
			if event.Has(fsnotify.Create) {
				if err := addCreatedTree(ctx, observer.source, observer.patterns, observer.ignore, observer.root, event.Name); err != nil {
					return Change{}, fmt.Errorf("registering created directory %s: %w", event.Name, err)
				}
			}
			relative, matches := matchingPath(observer.root, observer.patterns, observer.ignore, event.Name)
			if !matches {
				continue
			}
			operations := matchingOperations(event, observer.selected)
			if len(operations) > 0 {
				return Change{Path: relative, Operations: operations}, nil
			}
		case watchErr, ok := <-observer.source.ErrorChannel():
			if !ok {
				return Change{}, fmt.Errorf("filesystem watch error channel closed unexpectedly")
			}
			if watchErr != nil {
				return Change{}, fmt.Errorf("watching filesystem: %w", watchErr)
			}
		}
	}
}

func (observer *Observer) Close() error {
	observer.closeOnce.Do(func() {
		observer.closeErr = observer.source.Close()
	})
	return observer.closeErr
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
		if !slices.Contains(EventNames(), event) {
			return nil, fmt.Errorf("events[%d] must be create, modify, rename, or remove", index)
		}
		present[event] = true
	}
	normalized := make([]string, 0, len(present)+len(templates))
	for _, event := range EventNames() {
		if present[event] {
			normalized = append(normalized, event)
		}
	}
	return append(normalized, templates...), nil
}

func matchingOperations(event fsnotify.Event, selected map[string]bool) []string {
	operations := make([]string, 0, len(supportedEvents))
	for _, definition := range supportedEvents {
		if selected[definition.name] && event.Has(definition.op) {
			operations = append(operations, definition.name)
		}
	}
	return operations
}

func matchingPath(root string, patterns, ignore [][]string, name string) (string, bool) {
	absolute, err := filepath.Abs(name)
	if err != nil {
		return "", false
	}
	relative, components, ok := relativeComponents(root, absolute)
	if !ok || len(components) == 0 || ignored(ignore, components) {
		return "", false
	}
	for _, pattern := range patterns {
		if matchComponents(pattern, components) {
			return relative, true
		}
	}
	return "", false
}

// relativeComponents splits a path into the components below root, reporting false when it
// escapes root. The root itself yields no components.
func relativeComponents(root, name string) (string, []string, bool) {
	relative, err := filepath.Rel(root, name)
	if err != nil {
		return "", nil, false
	}
	relative = filepath.ToSlash(relative)
	if relative == ".." || strings.HasPrefix(relative, "../") {
		return "", nil, false
	}
	if relative == "." {
		return relative, nil, true
	}
	return relative, strings.Split(path.Clean(relative), "/"), true
}

// ignored reports whether a path, or any directory above it, matches an ignore pattern. Testing
// the ancestors is what lets "node_modules" exclude the subtree rather than only the directory
// entry itself, which is how anyone writing an ignore list expects it to read.
func ignored(ignore [][]string, components []string) bool {
	for depth := 1; depth <= len(components); depth++ {
		for _, pattern := range ignore {
			if matchComponents(pattern, components[:depth]) {
				return true
			}
		}
	}
	return false
}

func patternComponents(patterns []string) [][]string {
	components := make([][]string, len(patterns))
	for index, pattern := range patterns {
		components[index] = strings.Split(path.Clean(pattern), "/")
	}
	return components
}

// descendable reports whether any path beneath directory could still match a pattern. Watch
// registration follows it so that the tree wuko watches is the tree wuko can match: a directory
// no pattern can reach produces only events matchingPath throws away, and it is not free to
// watch, because the kqueue backend opens a descriptor per file in every watched directory.
func descendable(patterns, ignore [][]string, root, directory string) bool {
	_, components, ok := relativeComponents(root, directory)
	if !ok || ignored(ignore, components) {
		return false
	}
	if len(components) == 0 {
		return true
	}
	for _, pattern := range patterns {
		if canDescend(pattern, components) {
			return true
		}
	}
	return false
}

// canDescend mirrors matchComponents over a directory prefix instead of a whole path: it asks
// whether the pattern is still alive once those components are consumed. Sharing the hidden-name
// rule is the point — a hidden component is refused only against a wildcard, so "**/*.go" prunes
// .git while ".github/**/*.yml" still descends into .github.
func canDescend(pattern, directory []string) bool {
	if len(directory) == 0 {
		return true
	}
	if len(pattern) == 0 {
		return false
	}
	if pattern[0] == "**" {
		if canDescend(pattern[1:], directory) {
			return true
		}
		return !strings.HasPrefix(directory[0], ".") && canDescend(pattern, directory[1:])
	}
	if strings.HasPrefix(directory[0], ".") && (strings.HasPrefix(pattern[0], "*") || strings.HasPrefix(pattern[0], "?")) {
		return false
	}
	matched, err := doublestar.Match(pattern[0], directory[0])
	return err == nil && matched && canDescend(pattern[1:], directory[1:])
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

func addTree(ctx context.Context, source Source, patterns, ignore [][]string, root, start string, allowMissing bool) error {
	err := filepath.WalkDir(start, func(current string, entry fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			if allowMissing && errors.Is(walkErr, os.ErrNotExist) {
				return fs.SkipDir
			}
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 || !entry.IsDir() {
			return nil
		}
		if !descendable(patterns, ignore, root, current) {
			return fs.SkipDir
		}
		if err := source.Add(current); err != nil {
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

func addCreatedTree(ctx context.Context, source Source, patterns, ignore [][]string, root, created string) error {
	// Checked before the Lstat: a build writing into a pruned directory creates faster than
	// it is worth stat-ing, and nothing under one can be registered anyway.
	if !descendable(patterns, ignore, root, created) {
		return nil
	}
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
	return addTree(ctx, source, patterns, ignore, root, created, true)
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
