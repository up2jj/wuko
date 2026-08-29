// Package scaffold renders a packaged template directory into a workflow run directory.
package scaffold

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/up2jj/wuko/step"
)

const (
	conflictFail      = "fail"
	conflictSkip      = "skip"
	conflictOverwrite = "overwrite"
)

type Config struct {
	From       string `yaml:"from"`
	Into       string `yaml:"into"`
	OnConflict string `yaml:"on_conflict,omitempty"`
}

type Runner struct {
	config Config
}

type plannedEntry struct {
	relative    string
	staged      string
	destination string
	mode        os.FileMode
	directory   bool
	exists      bool
	outcome     string
}

// plannedPath records one rendered destination and the source entry that produced it.
type plannedPath struct {
	relative string
	source   string
}

type treePlan struct {
	staging string
	dirs    []*plannedEntry
	files   []*plannedEntry
}

func Register(registry *step.Registry) error { return registry.Register("scaffold", New) }

func New(raw map[string]any) (step.Runner, error) {
	var config Config
	if err := step.DecodeConfig(raw, &config); err != nil {
		return nil, err
	}
	if config.OnConflict == "" {
		config.OnConflict = conflictFail
	}
	runner := &Runner{config: config}
	if err := runner.validate(false); err != nil {
		return nil, err
	}
	return runner, nil
}

func (runner *Runner) Validate(ctx context.Context, request step.Request) error {
	if templated(runner.config.From) {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if request.TemplateRenderer == nil {
		return fmt.Errorf("template renderer is unavailable")
	}
	source, err := resolveSource(request, runner.config.From)
	if err != nil {
		return err
	}
	return validateTree(ctx, source, request.TemplateRenderer)
}

func (runner *Runner) Run(ctx context.Context, request step.Request) (step.Result, error) {
	if err := runner.validate(true); err != nil {
		return step.Result{}, err
	}
	if request.TemplateRenderer == nil {
		return step.Result{}, fmt.Errorf("template renderer is unavailable")
	}
	if err := ctx.Err(); err != nil {
		return step.Result{}, err
	}
	source, err := resolveSource(request, runner.config.From)
	if err != nil {
		return step.Result{}, err
	}
	destination, err := resolveDestination(request.RunDir, runner.config.Into)
	if err != nil {
		return step.Result{}, err
	}
	plan, err := renderTree(ctx, source, destination, request.TemplateRenderer)
	if err != nil {
		return step.Result{}, err
	}
	defer os.RemoveAll(plan.staging)

	created, skipped, overwritten, err := preflight(plan, runner.config.OnConflict)
	if err != nil {
		return step.Result{}, err
	}
	if err := apply(ctx, plan); err != nil {
		return step.Result{}, err
	}
	files := make([]any, len(plan.files))
	for index, file := range plan.files {
		files[index] = file.destination
	}
	return step.Result{Outputs: map[string]any{
		"from": source, "into": destination,
		"created": created, "skipped": skipped, "overwritten": overwritten,
		"files": files,
	}}, nil
}

func (runner *Runner) validate(resolved bool) error {
	if strings.TrimSpace(runner.config.From) == "" {
		return fmt.Errorf("from is required")
	}
	if strings.TrimSpace(runner.config.Into) == "" {
		return fmt.Errorf("into is required")
	}
	if resolved && (templated(runner.config.From) || templated(runner.config.Into) || templated(runner.config.OnConflict)) {
		return fmt.Errorf("configuration contains an unresolved template")
	}
	if !templated(runner.config.From) {
		if err := validateRelativeSource(runner.config.From); err != nil {
			return err
		}
	}
	// A templated policy is only knowable once rendered; every other field still validates.
	if !resolved && templated(runner.config.OnConflict) {
		return nil
	}
	switch runner.config.OnConflict {
	case conflictFail, conflictSkip, conflictOverwrite:
	default:
		return fmt.Errorf("on_conflict must be fail, skip, or overwrite")
	}
	return nil
}

func resolveSource(request step.Request, value string) (string, error) {
	workflowDir := request.WorkflowDir
	if workflowDir == "" {
		return "", fmt.Errorf("workflow directory is unavailable")
	}
	if request.WorkflowDirBorrowed {
		return "", fmt.Errorf("from %q requires a packaged action: this action carries no files of its own, and %s belongs to the calling workflow", value, workflowDir)
	}
	if err := validateRelativeSource(value); err != nil {
		return "", err
	}
	root, err := filepath.Abs(workflowDir)
	if err != nil {
		return "", fmt.Errorf("resolving workflow directory: %w", err)
	}
	source := filepath.Join(root, filepath.FromSlash(value))
	rootPhysical, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("resolving workflow directory %s: %w", root, err)
	}
	sourceInfo, err := os.Lstat(source)
	if err != nil {
		return "", fmt.Errorf("inspecting scaffold source %s: %w", source, err)
	}
	if sourceInfo.Mode()&os.ModeSymlink != 0 || !sourceInfo.IsDir() {
		return "", fmt.Errorf("scaffold source %s must be a directory and not a symbolic link", source)
	}
	sourcePhysical, err := filepath.EvalSymlinks(source)
	if err != nil {
		return "", fmt.Errorf("resolving scaffold source %s: %w", source, err)
	}
	if !pathWithin(rootPhysical, sourcePhysical) {
		return "", fmt.Errorf("scaffold source %q escapes the workflow package", value)
	}
	return filepath.Clean(sourcePhysical), nil
}

func validateRelativeSource(value string) error {
	converted := filepath.FromSlash(value)
	if filepath.IsAbs(converted) || filepath.VolumeName(converted) != "" {
		return fmt.Errorf("from must be a relative path")
	}
	cleaned := filepath.Clean(converted)
	if cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return fmt.Errorf("from must not escape the workflow package")
	}
	return nil
}

func resolveDestination(runDir, value string) (string, error) {
	if filepath.IsAbs(value) {
		return filepath.Clean(value), nil
	}
	if runDir == "" {
		var err error
		runDir, err = os.Getwd()
		if err != nil {
			return "", fmt.Errorf("finding run directory: %w", err)
		}
	}
	destination, err := filepath.Abs(filepath.Join(runDir, value))
	if err != nil {
		return "", fmt.Errorf("resolving scaffold destination %s: %w", value, err)
	}
	return filepath.Clean(destination), nil
}

func validateTree(ctx context.Context, source string, renderer step.TemplateRenderer) error {
	return filepath.WalkDir(source, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if path == source {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("inspecting scaffold source %s: %w", path, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("scaffold source contains symbolic link %s", path)
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			return fmt.Errorf("scaffold source entry %s must be a regular file or directory", path)
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return fmt.Errorf("relating scaffold source %s: %w", path, err)
		}
		for _, component := range strings.Split(filepath.ToSlash(relative), "/") {
			if err := renderer.Validate(component); err != nil {
				return fmt.Errorf("validating scaffold path component %q: %w", component, err)
			}
		}
		if info.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("reading scaffold template %s: %w", path, err)
		}
		if !utf8.Valid(data) {
			return fmt.Errorf("scaffold template %s is not valid UTF-8", path)
		}
		if err := renderer.ValidateContent(string(data)); err != nil {
			return fmt.Errorf("validating scaffold template %s: %w", path, err)
		}
		return nil
	})
}

func renderTree(ctx context.Context, source, destination string, renderer step.TemplateRenderer) (*treePlan, error) {
	staging, err := os.MkdirTemp("", "wuko-scaffold-")
	if err != nil {
		return nil, fmt.Errorf("creating scaffold staging directory: %w", err)
	}
	plan := &treePlan{staging: staging}
	failed := true
	defer func() {
		if failed {
			_ = os.RemoveAll(staging)
		}
	}()
	rootInfo, err := os.Stat(source)
	if err != nil {
		return nil, fmt.Errorf("inspecting scaffold source %s: %w", source, err)
	}
	plan.dirs = append(plan.dirs, &plannedEntry{relative: ".", staged: staging, destination: destination, mode: rootInfo.Mode().Perm(), directory: true})
	// Destinations are compared case-insensitively so a tree that collides on a
	// case-insensitive filesystem is rejected before anything is staged or written.
	seen := map[string]plannedPath{".": {relative: ".", source: source}}
	err = filepath.WalkDir(source, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if path == source {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("inspecting scaffold source %s: %w", path, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("scaffold source contains symbolic link %s", path)
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			return fmt.Errorf("scaffold source entry %s must be a regular file or directory", path)
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return fmt.Errorf("relating scaffold source %s: %w", path, err)
		}
		renderedRelative, err := renderRelativePath(relative, renderer)
		if err != nil {
			return fmt.Errorf("rendering scaffold path %s: %w", relative, err)
		}
		key := filepath.Clean(renderedRelative)
		folded := strings.ToLower(key)
		if previous, exists := seen[folded]; exists {
			if previous.relative == key {
				return fmt.Errorf("scaffold entries %s and %s render to duplicate path %q", previous.source, path, filepath.ToSlash(key))
			}
			return fmt.Errorf("scaffold entries %s and %s render to paths %q and %q that differ only in case", previous.source, path, filepath.ToSlash(previous.relative), filepath.ToSlash(key))
		}
		seen[folded] = plannedPath{relative: key, source: path}
		staged := filepath.Join(staging, key)
		planned := &plannedEntry{
			relative: key, staged: staged, destination: filepath.Join(destination, key),
			mode: info.Mode().Perm(), directory: info.IsDir(),
		}
		if info.IsDir() {
			if err := os.Mkdir(staged, 0o700); err != nil {
				return fmt.Errorf("creating staged scaffold directory %s: %w", staged, err)
			}
			plan.dirs = append(plan.dirs, planned)
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("reading scaffold template %s: %w", path, err)
		}
		if !utf8.Valid(data) {
			return fmt.Errorf("scaffold template %s is not valid UTF-8", path)
		}
		rendered, err := renderer.RenderContent(string(data))
		if err != nil {
			return fmt.Errorf("rendering scaffold template %s: %w", path, err)
		}
		if !utf8.ValidString(rendered) {
			return fmt.Errorf("rendered scaffold template %s is not valid UTF-8", path)
		}
		if err := os.WriteFile(staged, []byte(rendered), 0o600); err != nil {
			return fmt.Errorf("writing staged scaffold file %s: %w", staged, err)
		}
		plan.files = append(plan.files, planned)
		return nil
	})
	if err != nil {
		return nil, err
	}
	slices.SortFunc(plan.files, func(left, right *plannedEntry) int {
		return strings.Compare(left.destination, right.destination)
	})
	failed = false
	return plan, nil
}

func renderRelativePath(relative string, renderer step.TemplateRenderer) (string, error) {
	components := strings.Split(filepath.ToSlash(relative), "/")
	rendered := make([]string, len(components))
	for index, component := range components {
		value, err := renderer.Render(component)
		if err != nil {
			return "", fmt.Errorf("component %q: %w", component, err)
		}
		if value == "" || value == "." || value == ".." || strings.ContainsRune(value, 0) || strings.ContainsAny(value, `/\\`) || filepath.IsAbs(value) || filepath.VolumeName(value) != "" {
			return "", fmt.Errorf("component %q renders to invalid path component %q", component, value)
		}
		rendered[index] = value
	}
	return filepath.Join(rendered...), nil
}

func preflight(plan *treePlan, onConflict string) (created, skipped, overwritten int, err error) {
	for _, directory := range plan.dirs {
		info, err := os.Lstat(directory.destination)
		switch {
		case errors.Is(err, os.ErrNotExist):
			directory.exists = false
		case err != nil:
			return 0, 0, 0, fmt.Errorf("inspecting scaffold destination %s: %w", directory.destination, err)
		case info.Mode()&os.ModeSymlink != 0:
			return 0, 0, 0, fmt.Errorf("scaffold destination directory %s is a symbolic link", directory.destination)
		case !info.IsDir():
			return 0, 0, 0, fmt.Errorf("scaffold destination %s must be a directory", directory.destination)
		default:
			directory.exists = true
		}
	}
	for _, file := range plan.files {
		info, err := os.Lstat(file.destination)
		switch {
		case errors.Is(err, os.ErrNotExist):
			file.outcome = "create"
			created++
		case err != nil:
			return 0, 0, 0, fmt.Errorf("inspecting scaffold destination %s: %w", file.destination, err)
		case info.IsDir():
			return 0, 0, 0, fmt.Errorf("scaffold file destination %s is a directory", file.destination)
		case !info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0:
			return 0, 0, 0, fmt.Errorf("scaffold file destination %s is not a regular file or symbolic link", file.destination)
		case onConflict == conflictFail:
			return 0, 0, 0, fmt.Errorf("scaffold destination file %s already exists", file.destination)
		case onConflict == conflictSkip:
			file.outcome = conflictSkip
			skipped++
		case onConflict == conflictOverwrite:
			file.outcome = conflictOverwrite
			overwritten++
		}
	}
	return created, skipped, overwritten, nil
}

func apply(ctx context.Context, plan *treePlan) error {
	for index, directory := range plan.dirs {
		if err := ctx.Err(); err != nil {
			return err
		}
		if directory.exists {
			continue
		}
		if index == 0 {
			if err := os.MkdirAll(filepath.Dir(directory.destination), 0o755); err != nil {
				return fmt.Errorf("creating scaffold destination parent for %s: %w", directory.destination, err)
			}
		}
		if err := os.Mkdir(directory.destination, 0o700); err != nil {
			return fmt.Errorf("creating scaffold directory %s: %w", directory.destination, err)
		}
	}
	for _, file := range plan.files {
		if err := ctx.Err(); err != nil {
			return err
		}
		if file.outcome == conflictSkip {
			continue
		}
		if err := installFile(ctx, file.staged, file.destination, file.mode, file.outcome == conflictOverwrite); err != nil {
			return err
		}
	}
	for index := len(plan.dirs) - 1; index >= 0; index-- {
		directory := plan.dirs[index]
		if directory.exists {
			continue
		}
		if err := os.Chmod(directory.destination, directory.mode); err != nil {
			return fmt.Errorf("setting scaffold directory mode for %s: %w", directory.destination, err)
		}
	}
	return nil
}

func installFile(ctx context.Context, source, destination string, mode os.FileMode, overwrite bool) (resultErr error) {
	input, err := os.Open(source)
	if err != nil {
		return fmt.Errorf("opening staged scaffold file %s: %w", source, err)
	}
	defer input.Close()
	temporary, err := os.CreateTemp(filepath.Dir(destination), ".wuko-scaffold-*")
	if err != nil {
		return fmt.Errorf("creating temporary scaffold file for %s: %w", destination, err)
	}
	temporaryPath := temporary.Name()
	open := true
	defer func() {
		// Close first: an open handle blocks removal on Windows and would leave the
		// temporary file behind in the destination directory.
		if open {
			if closeErr := temporary.Close(); resultErr == nil && closeErr != nil {
				resultErr = fmt.Errorf("closing temporary scaffold file: %w", closeErr)
			}
		}
		_ = os.Remove(temporaryPath)
	}()
	if err := temporary.Chmod(mode); err != nil {
		return fmt.Errorf("setting temporary scaffold file mode: %w", err)
	}
	buffer := make([]byte, 32*1024)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		count, readErr := input.Read(buffer)
		if count > 0 {
			if _, err := temporary.Write(buffer[:count]); err != nil {
				return fmt.Errorf("writing temporary scaffold file: %w", err)
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return fmt.Errorf("reading staged scaffold file: %w", readErr)
		}
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("syncing temporary scaffold file: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("closing temporary scaffold file: %w", err)
	}
	open = false
	if overwrite {
		if err := os.Rename(temporaryPath, destination); err != nil {
			return fmt.Errorf("installing scaffold file %s: %w", destination, err)
		}
		return nil
	}
	if err := os.Link(temporaryPath, destination); err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("scaffold destination file %s appeared after conflict preflight", destination)
		}
		return fmt.Errorf("installing scaffold file %s: %w", destination, err)
	}
	if err := os.Remove(temporaryPath); err != nil {
		return fmt.Errorf("removing temporary scaffold link for %s: %w", destination, err)
	}
	return nil
}

func pathWithin(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func templated(value string) bool { return strings.Contains(value, "{{") }
