// Package glob implements portable filesystem pattern discovery for workflows.
package glob

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
	"time"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/up2jj/wuko/step"
)

type Config struct {
	Root     string   `yaml:"root,omitempty"`
	Patterns []string `yaml:"patterns"`
}

type Runner struct {
	config Config
	fsys   fs.FS
}

func Register(registry *step.Registry) error { return registry.Register("glob", New) }

func New(raw map[string]any) (step.Runner, error) {
	var config Config
	if err := step.DecodeConfig(raw, &config); err != nil {
		return nil, err
	}
	if config.Root == "" {
		config.Root = "."
	}
	if len(config.Patterns) == 0 {
		return nil, fmt.Errorf("patterns must contain at least one pattern")
	}
	for index, pattern := range config.Patterns {
		if err := validatePattern(pattern); err != nil {
			return nil, fmt.Errorf("patterns[%d]: %w", index, err)
		}
	}
	return &Runner{config: config}, nil
}

func (r *Runner) Run(ctx context.Context, request step.Request) (step.Result, error) {
	if err := ctx.Err(); err != nil {
		return step.Result{}, err
	}
	root, err := resolveRoot(request.RunDir, r.config.Root)
	if err != nil {
		return step.Result{}, err
	}
	info, err := os.Stat(root)
	if err != nil {
		return step.Result{}, fmt.Errorf("inspecting glob root %s: %w", root, err)
	}
	if !info.IsDir() {
		return step.Result{}, fmt.Errorf("glob root %s is not a directory", root)
	}

	rootFS := r.fsys
	if rootFS == nil {
		rootFS = os.DirFS(root)
	}
	rootFS = cancelableFS{ctx: ctx, FS: rootFS}
	matches := make(map[string]map[string]any)
	options := []doublestar.GlobOption{
		doublestar.WithFilesOnly(),
		doublestar.WithNoFollow(),
		doublestar.WithNoHidden(),
		doublestar.WithFailOnIOErrors(),
	}
	for index, pattern := range r.config.Patterns {
		if err := ctx.Err(); err != nil {
			return step.Result{}, err
		}
		blocked, err := literalPrefixContainsSymlink(root, pattern)
		if err != nil {
			return step.Result{}, fmt.Errorf("inspecting patterns[%d] prefix: %w", index, err)
		}
		if blocked {
			continue
		}
		err = doublestar.GlobWalk(rootFS, pattern, func(match string, _ fs.DirEntry) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			info, err := os.Lstat(filepath.Join(root, filepath.FromSlash(match)))
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() {
				return nil
			}
			match = path.Clean(match)
			matches[match] = fileMetadata(match, info)
			return nil
		}, options...)
		if err != nil {
			return step.Result{}, fmt.Errorf("matching patterns[%d] %q: %w", index, pattern, err)
		}
	}

	paths := make([]string, 0, len(matches))
	for match := range matches {
		paths = append(paths, match)
	}
	slices.Sort(paths)
	files := make([]any, len(paths))
	for index, match := range paths {
		files[index] = matches[match]
	}
	return step.Result{Outputs: map[string]any{
		"root":  root,
		"count": len(files),
		"files": files,
	}}, nil
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

func windowsAbsolute(value string) bool {
	if strings.HasPrefix(value, `\\`) {
		return true
	}
	return len(value) >= 3 && ((value[0] >= 'a' && value[0] <= 'z') || (value[0] >= 'A' && value[0] <= 'Z')) && value[1] == ':' && (value[2] == '/' || value[2] == '\\')
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
		return "", fmt.Errorf("resolving glob root %s: %w", root, err)
	}
	return filepath.Clean(value), nil
}

func literalPrefixContainsSymlink(root, pattern string) (bool, error) {
	base, _ := doublestar.SplitPattern(pattern)
	if base == "." {
		return false, nil
	}
	current := root
	for _, component := range strings.Split(base, "/") {
		if component == "" || component == "." {
			continue
		}
		current = filepath.Join(current, filepath.FromSlash(component))
		info, err := os.Lstat(current)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return false, nil
			}
			return false, err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return true, nil
		}
	}
	return false, nil
}

func fileMetadata(filePath string, info fs.FileInfo) map[string]any {
	return map[string]any{
		"name":        filepath.Base(filePath),
		"path":        filepath.ToSlash(filePath),
		"type":        "file",
		"size":        info.Size(),
		"mode":        fmt.Sprintf("%04o", info.Mode().Perm()),
		"modified_at": info.ModTime().UTC().Format(time.RFC3339Nano),
	}
}

type cancelableFS struct {
	ctx context.Context
	fs.FS
}

func (f cancelableFS) Open(name string) (fs.File, error) {
	if err := f.ctx.Err(); err != nil {
		return nil, err
	}
	file, err := f.FS.Open(name)
	if err != nil {
		return nil, err
	}
	return cancelableFile{ctx: f.ctx, File: file}, nil
}

type cancelableFile struct {
	ctx context.Context
	fs.File
}

func (f cancelableFile) Read(buffer []byte) (int, error) {
	if err := f.ctx.Err(); err != nil {
		return 0, err
	}
	return f.File.Read(buffer)
}

func (f cancelableFile) Stat() (fs.FileInfo, error) {
	if err := f.ctx.Err(); err != nil {
		return nil, err
	}
	return f.File.Stat()
}

func (f cancelableFile) ReadDir(count int) ([]fs.DirEntry, error) {
	if err := f.ctx.Err(); err != nil {
		return nil, err
	}
	directory, ok := f.File.(fs.ReadDirFile)
	if !ok {
		return nil, fmt.Errorf("file does not support reading directories: %w", fs.ErrInvalid)
	}
	return directory.ReadDir(count)
}

func templated(value string) bool { return strings.Contains(value, "{{") }
