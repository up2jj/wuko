// Package logwait follows a growing log file until a regular expression matches.
package logwait

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/fsnotify/fsnotify"
	"github.com/up2jj/wuko/step"
)

const defaultMaxBytes = "1MiB"

const readBufferSize = 32 * 1024

type Config struct {
	Path     string `yaml:"path"`
	Pattern  string `yaml:"pattern"`
	MaxBytes string `yaml:"max_bytes,omitempty"`
}

type namedCapture struct {
	name  string
	index int
}

type Runner struct {
	config     Config
	regexp     *regexp.Regexp
	maxBytes   int64
	captures   []namedCapture
	newWatcher func() (eventWatcher, error)
}

type eventWatcher interface {
	Add(string) error
	Close() error
	Events() <-chan fsnotify.Event
	Errors() <-chan error
}

type nativeWatcher struct{ *fsnotify.Watcher }

func (w nativeWatcher) Events() <-chan fsnotify.Event { return w.Watcher.Events }
func (w nativeWatcher) Errors() <-chan error          { return w.Watcher.Errors }

func Register(registry *step.Registry) error { return registry.Register("log_wait", New) }

func New(raw map[string]any) (step.Runner, error) {
	config := Config{MaxBytes: defaultMaxBytes}
	if err := step.DecodeConfig(raw, &config); err != nil {
		return nil, err
	}
	runner := &Runner{
		config: config,
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

func (r *Runner) Run(ctx context.Context, request step.Request) (result step.Result, runErr error) {
	if err := r.validate(true); err != nil {
		return step.Result{}, err
	}
	if err := ctx.Err(); err != nil {
		return step.Result{}, err
	}

	path := resolvePath(request.RunDir, r.config.Path)
	parent := filepath.Dir(path)
	info, err := os.Stat(parent)
	if err != nil {
		return step.Result{}, fmt.Errorf("inspecting log_wait parent %s: %w", parent, err)
	}
	if !info.IsDir() {
		return step.Result{}, fmt.Errorf("log_wait parent %s is not a directory", parent)
	}

	watcher, err := r.newWatcher()
	if err != nil {
		return step.Result{}, fmt.Errorf("creating log_wait watcher: %w", err)
	}
	defer func() {
		if closeErr := watcher.Close(); closeErr != nil {
			runErr = errors.Join(runErr, fmt.Errorf("closing log_wait watcher: %w", closeErr))
		}
	}()
	if err := watcher.Add(parent); err != nil {
		return step.Result{}, fmt.Errorf("watching log_wait parent %s: %w", parent, err)
	}

	follower := fileFollower{path: path, pattern: r.regexp, maxBytes: r.maxBytes, captures: r.captures}
	defer follower.close()
	forceRescan := false
	for {
		matched, matchResult, err := follower.readAvailable(ctx, forceRescan)
		forceRescan = false
		if err != nil {
			return step.Result{}, err
		}
		if matched {
			return matchResult, nil
		}

		select {
		case <-ctx.Done():
			return step.Result{}, ctx.Err()
		case event, ok := <-watcher.Events():
			if !ok {
				return step.Result{}, fmt.Errorf("log_wait watcher event channel closed unexpectedly")
			}
			forceRescan = eventRequiresRescan(event, path)
		case watchErr, ok := <-watcher.Errors():
			if !ok {
				return step.Result{}, fmt.Errorf("log_wait watcher error channel closed unexpectedly")
			}
			if watchErr != nil {
				if !errors.Is(watchErr, fsnotify.ErrEventOverflow) {
					return step.Result{}, fmt.Errorf("watching log_wait file: %w", watchErr)
				}
				forceRescan = true
			}
		}
	}
}

func (r *Runner) validate(resolved bool) error {
	if strings.TrimSpace(r.config.Path) == "" {
		return fmt.Errorf("path is required")
	}
	if resolved && templated(r.config.Path) {
		return fmt.Errorf("log_wait configuration contains an unresolved template")
	}
	if strings.TrimSpace(r.config.Pattern) == "" {
		return fmt.Errorf("pattern is required")
	}
	if !resolved && templated(r.config.Pattern) {
		return r.validateMaxBytes(resolved)
	}
	if resolved && templated(r.config.Pattern) {
		return fmt.Errorf("log_wait configuration contains an unresolved template")
	}
	compiled, captures, err := compilePattern(r.config.Pattern)
	if err != nil {
		return err
	}
	r.regexp = compiled
	r.captures = captures
	return r.validateMaxBytes(resolved)
}

func (r *Runner) validateMaxBytes(resolved bool) error {
	if strings.TrimSpace(r.config.MaxBytes) == "" {
		return fmt.Errorf("max_bytes must be a positive size")
	}
	if !resolved && templated(r.config.MaxBytes) {
		return nil
	}
	if resolved && templated(r.config.MaxBytes) {
		return fmt.Errorf("log_wait configuration contains an unresolved template")
	}
	maxBytes, err := parseSize(r.config.MaxBytes)
	if err != nil {
		return fmt.Errorf("max_bytes: %w", err)
	}
	if maxBytes <= 0 {
		return fmt.Errorf("max_bytes must be a positive size")
	}
	r.maxBytes = maxBytes
	return nil
}

func compilePattern(pattern string) (*regexp.Regexp, []namedCapture, error) {
	compiled, err := regexp.Compile(pattern)
	if err != nil {
		return nil, nil, fmt.Errorf("compiling pattern: %w", err)
	}
	seen := make(map[string]struct{})
	captures := make([]namedCapture, 0)
	for index, name := range compiled.SubexpNames() {
		if index == 0 || name == "" {
			continue
		}
		if _, exists := seen[name]; exists {
			return nil, nil, fmt.Errorf("pattern contains duplicate named capture %q", name)
		}
		seen[name] = struct{}{}
		captures = append(captures, namedCapture{name: name, index: index})
	}
	return compiled, captures, nil
}

type fileFollower struct {
	path     string
	pattern  *regexp.Regexp
	maxBytes int64
	captures []namedCapture
	file     *os.File
	identity os.FileInfo
	offset   int64
	content  []byte
	buffer   []byte
}

func (f *fileFollower) readAvailable(ctx context.Context, forceRescan bool) (bool, step.Result, error) {
	if err := ctx.Err(); err != nil {
		return false, step.Result{}, err
	}
	current, err := os.Stat(f.path)
	if errors.Is(err, os.ErrNotExist) {
		f.reset()
		return false, step.Result{}, nil
	}
	if err != nil {
		return false, step.Result{}, fmt.Errorf("inspecting log_wait file %s: %w", f.path, err)
	}
	if !current.Mode().IsRegular() {
		return false, step.Result{}, fmt.Errorf("log_wait path %s must be a regular file", f.path)
	}
	if forceRescan {
		f.reset()
	}
	if f.file == nil || !os.SameFile(f.identity, current) || current.Size() < f.offset {
		if err := f.open(current); err != nil {
			return false, step.Result{}, err
		}
	}
	if f.file == nil {
		return false, step.Result{}, nil
	}

	if f.buffer == nil {
		f.buffer = make([]byte, readBufferSize)
	}
	buffer := f.buffer
	for {
		if err := ctx.Err(); err != nil {
			return false, step.Result{}, err
		}
		remaining := f.maxBytes - int64(len(f.content))
		if remaining < 0 {
			return false, step.Result{}, fmt.Errorf("log_wait file %s exceeded max_bytes before matching", f.path)
		}
		readSize := int64(len(buffer))
		if remaining < readSize {
			readSize = remaining + 1
		}
		if readSize <= 0 {
			return false, step.Result{}, fmt.Errorf("log_wait file %s exceeded max_bytes before matching", f.path)
		}

		count, readErr := f.file.ReadAt(buffer[:readSize], f.offset)
		if count > 0 {
			f.offset += int64(count)
			f.content = append(f.content, buffer[:count]...)
			if matched, result := f.match(); matched {
				return true, result, nil
			}
			if int64(len(f.content)) > f.maxBytes {
				return false, step.Result{}, fmt.Errorf("log_wait file %s exceeded max_bytes before matching", f.path)
			}
		}
		if errors.Is(readErr, io.EOF) {
			return false, step.Result{}, nil
		}
		if readErr != nil {
			return false, step.Result{}, fmt.Errorf("reading log_wait file %s: %w", f.path, readErr)
		}
		if count == 0 {
			return false, step.Result{}, nil
		}
	}
}

// eventRequiresRescan reports whether the event replaced the file underneath us.
// Appends are read incrementally, and a truncate in place is caught by the size
// check in readAvailable, so only lifecycle events discard the buffered content.
func eventRequiresRescan(event fsnotify.Event, path string) bool {
	if filepath.Clean(event.Name) != path {
		return false
	}
	return event.Has(fsnotify.Create) || event.Has(fsnotify.Rename) || event.Has(fsnotify.Remove)
}

func (f *fileFollower) open(info os.FileInfo) error {
	f.close()
	input, err := os.Open(f.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("opening log_wait file %s: %w", f.path, err)
	}
	f.file = input
	f.identity = info
	f.offset = 0
	f.content = nil
	return nil
}

func (f *fileFollower) match() (bool, step.Result) {
	values := f.pattern.FindSubmatch(f.content)
	if values == nil {
		return false, step.Result{}
	}
	captures := make(map[string]string, len(f.captures))
	for _, capture := range f.captures {
		if capture.index < len(values) {
			captures[capture.name] = string(values[capture.index])
		}
	}
	return true, step.Result{Outputs: map[string]any{
		"path": f.path, "match": string(values[0]), "captures": captures,
	}}
}

func (f *fileFollower) reset() {
	f.close()
	f.identity = nil
	f.offset = 0
	f.content = nil
}

func (f *fileFollower) close() {
	if f.file != nil {
		_ = f.file.Close()
		f.file = nil
	}
}

func resolvePath(runDir, configured string) string {
	if filepath.IsAbs(configured) {
		return filepath.Clean(configured)
	}
	return filepath.Clean(filepath.Join(runDir, configured))
}

func parseSize(value string) (int64, error) {
	trimmed := strings.TrimSpace(value)
	for _, unit := range []struct {
		suffix     string
		multiplier int64
	}{
		{"TiB", 1 << 40}, {"GiB", 1 << 30}, {"MiB", 1 << 20}, {"KiB", 1 << 10}, {"B", 1},
	} {
		if !strings.HasSuffix(trimmed, unit.suffix) {
			continue
		}
		number := strings.TrimSpace(strings.TrimSuffix(trimmed, unit.suffix))
		parsed, err := strconv.ParseInt(number, 10, 64)
		if err != nil || parsed < 0 {
			break
		}
		if parsed > math.MaxInt64/unit.multiplier {
			break
		}
		return parsed * unit.multiplier, nil
	}
	return 0, fmt.Errorf("must be a non-negative size such as 64KiB")
}

func templated(value string) bool { return strings.Contains(value, "{{") }

var _ step.Runner = (*Runner)(nil)
