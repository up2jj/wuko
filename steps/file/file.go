// Package file implements declarative filesystem workflow operations.
package file

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/up2jj/wuko/step"
)

const (
	operationRead   = "read"
	operationWrite  = "write"
	operationCopy   = "copy"
	operationMove   = "move"
	operationRemove = "remove"
	operationMkdir  = "mkdir"
	operationList   = "list"
	operationStat   = "stat"
	operationChmod  = "chmod"
)

var modePattern = regexp.MustCompile(`^0[0-7]{3}$`)

type Config struct {
	Operation   string `yaml:"operation"`
	Path        string `yaml:"path"`
	Destination string `yaml:"destination,omitempty"`
	Content     string `yaml:"content,omitempty"`
	Recursive   bool   `yaml:"recursive,omitempty"`
	Overwrite   bool   `yaml:"overwrite,omitempty"`
	Mode        string `yaml:"mode,omitempty"`
}

type Runner struct {
	config  Config
	present map[string]bool
}

func Register(registry *step.Registry) error { return registry.Register("file", New) }

func New(raw map[string]any) (step.Runner, error) {
	if value, ok := raw["mode"]; ok {
		if _, ok := value.(string); !ok {
			return nil, fmt.Errorf("mode must be a quoted octal string")
		}
	}
	var config Config
	if err := step.DecodeConfig(raw, &config); err != nil {
		return nil, err
	}
	present := make(map[string]bool, len(raw))
	for key := range raw {
		present[key] = true
	}
	runner := &Runner{config: config, present: present}
	if err := runner.validate(false); err != nil {
		return nil, err
	}
	return runner, nil
}

func (r *Runner) Validate(_ context.Context, _ step.Request) error { return r.validate(false) }

func (r *Runner) Run(ctx context.Context, request step.Request) (step.Result, error) {
	if err := r.validate(true); err != nil {
		return step.Result{}, err
	}
	if err := ctx.Err(); err != nil {
		return step.Result{}, err
	}
	path, err := resolvePath(request.RunDir, r.config.Path)
	if err != nil {
		return step.Result{}, err
	}
	switch r.config.Operation {
	case operationRead:
		return r.read(ctx, path)
	case operationWrite:
		return r.write(ctx, path)
	case operationCopy:
		return r.copy(ctx, request.RunDir, path)
	case operationMove:
		return r.move(ctx, request.RunDir, path)
	case operationRemove:
		return r.remove(ctx, request.RunDir, path)
	case operationMkdir:
		return r.mkdir(ctx, path)
	case operationList:
		return r.list(ctx, path)
	case operationStat:
		return r.stat(ctx, path)
	case operationChmod:
		return r.chmod(ctx, path)
	default:
		panic("validated file operation")
	}
}

func (r *Runner) validate(resolved bool) error {
	if r.config.Operation == "" {
		return fmt.Errorf("operation is required")
	}
	if r.config.Path == "" {
		return fmt.Errorf("path is required")
	}
	if !resolved && templated(r.config.Operation) {
		return nil
	}
	allowed := map[string]bool{"operation": true, "path": true}
	require := func(field string) error {
		if !r.present[field] {
			return fmt.Errorf("%s is required for %s", field, r.config.Operation)
		}
		return nil
	}
	switch r.config.Operation {
	case operationRead, operationStat:
	case operationWrite:
		allowed["content"], allowed["overwrite"], allowed["mode"] = true, true, true
		if err := require("content"); err != nil {
			return err
		}
	case operationCopy, operationMove:
		allowed["destination"], allowed["overwrite"] = true, true
		if err := require("destination"); err != nil {
			return err
		}
		if r.config.Destination == "" {
			return fmt.Errorf("destination must not be empty")
		}
	case operationRemove:
		allowed["recursive"] = true
	case operationMkdir:
		allowed["recursive"], allowed["mode"] = true, true
	case operationList:
		allowed["recursive"] = true
	case operationChmod:
		allowed["mode"] = true
		if err := require("mode"); err != nil {
			return err
		}
	default:
		if !resolved && templated(r.config.Operation) {
			return nil
		}
		return fmt.Errorf("operation must be read, write, copy, move, remove, mkdir, list, stat, or chmod")
	}
	for field := range r.present {
		if !allowed[field] {
			return fmt.Errorf("%s is not allowed for %s", field, r.config.Operation)
		}
	}
	if r.config.Mode != "" && (resolved || !templated(r.config.Mode)) {
		if _, err := parseMode(r.config.Mode); err != nil {
			return err
		}
	}
	return nil
}

func (r *Runner) read(ctx context.Context, path string) (step.Result, error) {
	input, err := os.Open(path)
	if err != nil {
		return step.Result{}, fmt.Errorf("reading file %s: %w", path, err)
	}
	defer input.Close()
	var content bytes.Buffer
	buffer := make([]byte, 32*1024)
	for {
		if err := ctx.Err(); err != nil {
			return step.Result{}, err
		}
		count, readErr := input.Read(buffer)
		if count > 0 {
			content.Write(buffer[:count])
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return step.Result{}, fmt.Errorf("reading file %s: %w", path, readErr)
		}
	}
	return step.Result{Outputs: map[string]any{"path": path, "content": content.String(), "size": int64(content.Len())}}, nil
}

func (r *Runner) write(ctx context.Context, path string) (step.Result, error) {
	mode := os.FileMode(0o644)
	info, err := os.Lstat(path)
	exists := err == nil
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return step.Result{}, fmt.Errorf("inspecting destination %s: %w", path, err)
	}
	if exists {
		if !r.config.Overwrite {
			return step.Result{}, fmt.Errorf("destination %s already exists; set overwrite to true", path)
		}
		if info.IsDir() {
			return step.Result{}, fmt.Errorf("destination %s is a directory", path)
		}
		if info.Mode().IsRegular() {
			mode = info.Mode().Perm()
		}
	}
	if r.config.Mode != "" {
		mode, err = parseMode(r.config.Mode)
		if err != nil {
			return step.Result{}, err
		}
	}
	if err := atomicWrite(ctx, path, strings.NewReader(r.config.Content), mode, r.config.Overwrite); err != nil {
		return step.Result{}, err
	}
	return step.Result{Outputs: map[string]any{
		"path": path, "size": int64(len(r.config.Content)), "mode": formatMode(mode), "created": !exists,
	}}, nil
}

func (r *Runner) copy(ctx context.Context, runDir, source string) (step.Result, error) {
	destination, err := resolvePath(runDir, r.config.Destination)
	if err != nil {
		return step.Result{}, err
	}
	info, err := os.Stat(source)
	if err != nil {
		return step.Result{}, fmt.Errorf("inspecting source %s: %w", source, err)
	}
	if !info.Mode().IsRegular() {
		return step.Result{}, fmt.Errorf("source %s must be a regular file", source)
	}
	if err := ensureDestination(destination, r.config.Overwrite); err != nil {
		return step.Result{}, err
	}
	input, err := os.Open(source)
	if err != nil {
		return step.Result{}, fmt.Errorf("opening source %s: %w", source, err)
	}
	defer input.Close()
	if err := atomicWrite(ctx, destination, input, info.Mode().Perm(), r.config.Overwrite); err != nil {
		return step.Result{}, err
	}
	return step.Result{Outputs: map[string]any{
		"path": source, "destination": destination, "size": info.Size(), "mode": formatMode(info.Mode()),
	}}, nil
}

func (r *Runner) move(ctx context.Context, runDir, source string) (step.Result, error) {
	destination, err := resolvePath(runDir, r.config.Destination)
	if err != nil {
		return step.Result{}, err
	}
	if err := ctx.Err(); err != nil {
		return step.Result{}, err
	}
	if err := renamePath(source, destination, r.config.Overwrite); errors.Is(err, syscall.EXDEV) {
		if err := moveAcrossFilesystems(ctx, source, destination, r.config.Overwrite); err != nil {
			return step.Result{}, fmt.Errorf("moving %s to %s across filesystems: %w", source, destination, err)
		}
	} else if err != nil {
		if !r.config.Overwrite && errors.Is(err, os.ErrExist) {
			return step.Result{}, fmt.Errorf("destination %s already exists; set overwrite to true", destination)
		}
		return step.Result{}, fmt.Errorf("moving %s to %s: %w", source, destination, err)
	}
	info, err := os.Lstat(destination)
	if err != nil {
		return step.Result{}, fmt.Errorf("inspecting moved path %s: %w", destination, err)
	}
	return step.Result{Outputs: map[string]any{
		"path": source, "destination": destination, "size": info.Size(), "mode": formatMode(info.Mode()),
	}}, nil
}

func (r *Runner) remove(ctx context.Context, runDir, path string) (step.Result, error) {
	if rootPath(path) {
		return step.Result{}, fmt.Errorf("refusing to remove filesystem root %s", path)
	}
	resolvedRunDir, err := resolvePath("", runDir)
	if err == nil && filepath.Clean(path) == resolvedRunDir {
		return step.Result{}, fmt.Errorf("refusing to remove run directory %s", path)
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return step.Result{Outputs: map[string]any{"path": path, "removed": false}}, nil
	}
	if err != nil {
		return step.Result{}, fmt.Errorf("inspecting path %s: %w", path, err)
	}
	if err := ctx.Err(); err != nil {
		return step.Result{}, err
	}
	if info.IsDir() && r.config.Recursive {
		err = removeAll(ctx, path)
	} else {
		err = os.Remove(path)
	}
	if err != nil {
		return step.Result{}, fmt.Errorf("removing %s: %w", path, err)
	}
	return step.Result{Outputs: map[string]any{"path": path, "removed": true}}, nil
}

func (r *Runner) mkdir(ctx context.Context, path string) (step.Result, error) {
	mode := os.FileMode(0o755)
	var err error
	if r.config.Mode != "" {
		mode, err = parseMode(r.config.Mode)
		if err != nil {
			return step.Result{}, err
		}
	}
	info, err := os.Stat(path)
	if err == nil {
		if !info.IsDir() {
			return step.Result{}, fmt.Errorf("path %s exists and is not a directory", path)
		}
		return step.Result{Outputs: map[string]any{"path": path, "created": false, "mode": formatMode(info.Mode())}}, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return step.Result{}, fmt.Errorf("inspecting directory %s: %w", path, err)
	}
	if err := ctx.Err(); err != nil {
		return step.Result{}, err
	}
	if r.config.Recursive {
		err = os.MkdirAll(path, mode)
	} else {
		err = os.Mkdir(path, mode)
	}
	if err != nil {
		return step.Result{}, fmt.Errorf("creating directory %s: %w", path, err)
	}
	if err := os.Chmod(path, mode); err != nil {
		return step.Result{}, fmt.Errorf("setting directory mode: %w", err)
	}
	return step.Result{Outputs: map[string]any{"path": path, "created": true, "mode": formatMode(mode)}}, nil
}

func (r *Runner) list(ctx context.Context, path string) (step.Result, error) {
	entries := make([]map[string]any, 0)
	add := func(entryPath string, info os.FileInfo) {
		relative, _ := filepath.Rel(path, entryPath)
		entries = append(entries, fileEntry(relative, info))
	}
	if r.config.Recursive {
		err := filepath.WalkDir(path, func(entryPath string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if entryPath == path {
				return nil
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			add(entryPath, info)
			return nil
		})
		if err != nil {
			return step.Result{}, fmt.Errorf("listing directory %s: %w", path, err)
		}
	} else {
		items, err := os.ReadDir(path)
		if err != nil {
			return step.Result{}, fmt.Errorf("listing directory %s: %w", path, err)
		}
		for _, entry := range items {
			if err := ctx.Err(); err != nil {
				return step.Result{}, err
			}
			info, err := entry.Info()
			if err != nil {
				return step.Result{}, fmt.Errorf("inspecting %s: %w", entry.Name(), err)
			}
			add(filepath.Join(path, entry.Name()), info)
		}
	}
	slices.SortFunc(entries, func(a, b map[string]any) int {
		return strings.Compare(a["path"].(string), b["path"].(string))
	})
	values := make([]any, len(entries))
	for i, entry := range entries {
		values[i] = entry
	}
	return step.Result{Outputs: map[string]any{"path": path, "entries": values}}, nil
}

func (r *Runner) stat(ctx context.Context, path string) (step.Result, error) {
	if err := ctx.Err(); err != nil {
		return step.Result{}, err
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return step.Result{Outputs: map[string]any{"path": path, "exists": false}}, nil
	}
	if err != nil {
		return step.Result{}, fmt.Errorf("inspecting path %s: %w", path, err)
	}
	outputs := fileEntry(filepath.Base(path), info)
	outputs["path"] = path
	outputs["exists"] = true
	return step.Result{Outputs: outputs}, nil
}

func (r *Runner) chmod(ctx context.Context, path string) (step.Result, error) {
	mode, err := parseMode(r.config.Mode)
	if err != nil {
		return step.Result{}, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return step.Result{}, fmt.Errorf("inspecting path %s: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return step.Result{}, fmt.Errorf("refusing to chmod symbolic link %s", path)
	}
	if err := ctx.Err(); err != nil {
		return step.Result{}, err
	}
	if err := os.Chmod(path, mode); err != nil {
		return step.Result{}, fmt.Errorf("changing mode of %s: %w", path, err)
	}
	return step.Result{Outputs: map[string]any{"path": path, "mode": formatMode(mode)}}, nil
}

func atomicWrite(ctx context.Context, destination string, source io.Reader, mode os.FileMode, overwrite bool) (resultErr error) {
	directory := filepath.Dir(destination)
	temporary, err := os.CreateTemp(directory, ".wuko-file-*")
	if err != nil {
		return fmt.Errorf("creating temporary file for %s: %w", destination, err)
	}
	temporaryPath := temporary.Name()
	open := true
	defer func() {
		_ = os.Remove(temporaryPath)
		if open {
			closeErr := temporary.Close()
			if resultErr == nil && closeErr != nil {
				resultErr = fmt.Errorf("closing temporary file: %w", closeErr)
			}
		}
	}()
	if err := temporary.Chmod(mode); err != nil {
		return fmt.Errorf("setting temporary file mode: %w", err)
	}
	buffer := make([]byte, 32*1024)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		count, readErr := source.Read(buffer)
		if count > 0 {
			if _, err := temporary.Write(buffer[:count]); err != nil {
				return fmt.Errorf("writing temporary file: %w", err)
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return fmt.Errorf("reading source: %w", readErr)
		}
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("syncing temporary file: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("closing temporary file: %w", err)
	}
	open = false
	if err := renamePath(temporaryPath, destination, overwrite); err != nil {
		if !overwrite && errors.Is(err, os.ErrExist) {
			return fmt.Errorf("destination %s already exists; set overwrite to true", destination)
		}
		return fmt.Errorf("installing %s: %w", destination, err)
	}
	return syncDirectory(directory)
}

func syncDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("opening destination directory: %w", err)
	}
	defer dir.Close()
	if err := dir.Sync(); err != nil {
		return fmt.Errorf("syncing destination directory: %w", err)
	}
	return nil
}

func renamePath(source, destination string, overwrite bool) error {
	if overwrite {
		return os.Rename(source, destination)
	}
	return renameNoReplace(source, destination)
}

func moveAcrossFilesystems(ctx context.Context, source, destination string, overwrite bool) error {
	if err := ensureDestination(destination, overwrite); err != nil {
		return err
	}
	info, err := os.Lstat(source)
	if err != nil {
		return fmt.Errorf("inspecting source %s: %w", source, err)
	}
	switch {
	case info.Mode().IsRegular():
		if err := copyRegularFile(ctx, source, destination, info.Mode().Perm(), overwrite); err != nil {
			return err
		}
	case info.IsDir():
		if err := copyDirectory(ctx, source, destination, info.Mode().Perm(), overwrite); err != nil {
			return err
		}
	case info.Mode()&os.ModeSymlink != 0:
		if err := copySymlink(source, destination, overwrite); err != nil {
			return err
		}
	default:
		return fmt.Errorf("source %s must be a regular file, directory, or symbolic link", source)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if info.IsDir() {
		if err := removeAll(ctx, source); err != nil {
			return fmt.Errorf("removing source directory %s: %w", source, err)
		}
		return nil
	}
	if err := os.Remove(source); err != nil {
		return fmt.Errorf("removing source %s: %w", source, err)
	}
	return nil
}

func copyRegularFile(ctx context.Context, source, destination string, mode os.FileMode, overwrite bool) error {
	input, err := os.Open(source)
	if err != nil {
		return fmt.Errorf("opening source %s: %w", source, err)
	}
	defer input.Close()
	return atomicWrite(ctx, destination, input, mode, overwrite)
}

func copySymlink(source, destination string, overwrite bool) error {
	target, err := os.Readlink(source)
	if err != nil {
		return fmt.Errorf("reading symbolic link %s: %w", source, err)
	}
	directory := filepath.Dir(destination)
	stagingDirectory, err := os.MkdirTemp(directory, ".wuko-move-*")
	if err != nil {
		return fmt.Errorf("creating temporary directory for %s: %w", destination, err)
	}
	defer os.RemoveAll(stagingDirectory)
	temporaryPath := filepath.Join(stagingDirectory, "link")
	if err := os.Symlink(target, temporaryPath); err != nil {
		return fmt.Errorf("creating temporary symbolic link: %w", err)
	}
	if err := renamePath(temporaryPath, destination, overwrite); err != nil {
		if !overwrite && errors.Is(err, os.ErrExist) {
			return fmt.Errorf("destination %s already exists; set overwrite to true", destination)
		}
		return fmt.Errorf("installing symbolic link %s: %w", destination, err)
	}
	return syncDirectory(directory)
}

type directoryMode struct {
	path string
	mode os.FileMode
}

func copyDirectory(ctx context.Context, source, destination string, mode os.FileMode, overwrite bool) error {
	directory := filepath.Dir(destination)
	stagingPath, err := os.MkdirTemp(directory, ".wuko-move-*")
	if err != nil {
		return fmt.Errorf("creating temporary directory for %s: %w", destination, err)
	}
	defer os.RemoveAll(stagingPath)
	directories := []directoryMode{{path: stagingPath, mode: mode}}
	err = filepath.WalkDir(source, func(sourcePath string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if sourcePath == source {
			return nil
		}
		relative, err := filepath.Rel(source, sourcePath)
		if err != nil {
			return err
		}
		targetPath := filepath.Join(stagingPath, relative)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		switch {
		case info.IsDir():
			if err := os.Mkdir(targetPath, 0o700); err != nil {
				return err
			}
			directories = append(directories, directoryMode{path: targetPath, mode: info.Mode().Perm()})
			return nil
		case info.Mode().IsRegular():
			return copyRegularFile(ctx, sourcePath, targetPath, info.Mode().Perm(), false)
		case info.Mode()&os.ModeSymlink != 0:
			linkTarget, err := os.Readlink(sourcePath)
			if err != nil {
				return err
			}
			return os.Symlink(linkTarget, targetPath)
		default:
			return fmt.Errorf("source entry %s must be a regular file, directory, or symbolic link", sourcePath)
		}
	})
	if err != nil {
		return fmt.Errorf("copying directory %s: %w", source, err)
	}
	for i := len(directories) - 1; i >= 0; i-- {
		if err := os.Chmod(directories[i].path, directories[i].mode); err != nil {
			return fmt.Errorf("preserving directory mode for %s: %w", directories[i].path, err)
		}
	}
	if err := renamePath(stagingPath, destination, overwrite); err != nil {
		if !overwrite && errors.Is(err, os.ErrExist) {
			return fmt.Errorf("destination %s already exists; set overwrite to true", destination)
		}
		return fmt.Errorf("installing directory %s: %w", destination, err)
	}
	return syncDirectory(directory)
}

func ensureDestination(path string, overwrite bool) error {
	_, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspecting destination %s: %w", path, err)
	}
	if !overwrite {
		return fmt.Errorf("destination %s already exists; set overwrite to true", path)
	}
	return nil
}

func resolvePath(runDir, path string) (string, error) {
	if filepath.IsAbs(path) {
		return filepath.Clean(path), nil
	}
	if runDir == "" {
		var err error
		runDir, err = os.Getwd()
		if err != nil {
			return "", fmt.Errorf("finding run directory: %w", err)
		}
	}
	value, err := filepath.Abs(filepath.Join(runDir, path))
	if err != nil {
		return "", fmt.Errorf("resolving path %s: %w", path, err)
	}
	return filepath.Clean(value), nil
}

func parseMode(value string) (os.FileMode, error) {
	if !modePattern.MatchString(value) {
		return 0, fmt.Errorf("mode must be a quoted octal string between \"0000\" and \"0777\"")
	}
	parsed, err := strconv.ParseUint(value, 8, 32)
	if err != nil {
		return 0, fmt.Errorf("parsing mode %q: %w", value, err)
	}
	return os.FileMode(parsed), nil
}

func removeAll(ctx context.Context, root string) error {
	paths := make([]string, 0)
	if err := filepath.WalkDir(root, func(path string, _ os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		paths = append(paths, path)
		return nil
	}); err != nil {
		return err
	}
	for i := len(paths) - 1; i >= 0; i-- {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := os.Remove(paths[i]); err != nil {
			return err
		}
	}
	return nil
}

func formatMode(mode os.FileMode) string { return fmt.Sprintf("%04o", mode.Perm()) }

func rootPath(path string) bool { return filepath.Dir(filepath.Clean(path)) == filepath.Clean(path) }

func fileEntry(path string, info os.FileInfo) map[string]any {
	return map[string]any{
		"name": filepath.Base(path), "path": filepath.ToSlash(path), "type": fileType(info.Mode()),
		"size": info.Size(), "mode": formatMode(info.Mode()), "modified_at": info.ModTime().UTC().Format(time.RFC3339Nano),
	}
}

func fileType(mode os.FileMode) string {
	switch {
	case mode.IsRegular():
		return "file"
	case mode.IsDir():
		return "directory"
	case mode&os.ModeSymlink != 0:
		return "symlink"
	default:
		return "other"
	}
}

func templated(value string) bool { return strings.Contains(value, "{{") }
