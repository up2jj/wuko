// Package path implements an interactive filesystem path-selection step.
package path

import (
	"context"
	"fmt"
	"os"
	pathpkg "path"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/tui"
)

const (
	kindFile      = "file"
	kindDirectory = "directory"
	kindEither    = "either"
)

type Config struct {
	Variable   string   `yaml:"variable"`
	Message    string   `yaml:"message"`
	Root       string   `yaml:"root,omitempty"`
	Kind       string   `yaml:"kind,omitempty"`
	Multiple   bool     `yaml:"multiple,omitempty"`
	Required   *bool    `yaml:"required,omitempty"`
	Patterns   []string `yaml:"patterns,omitempty"`
	ShowHidden bool     `yaml:"show_hidden,omitempty"`
}

type Runner struct{ config Config }

func Register(registry *step.Registry) error { return registry.Register("path", New) }

func New(raw map[string]any) (step.Runner, error) {
	var config Config
	if err := step.DecodeConfig(raw, &config); err != nil {
		return nil, err
	}
	if config.Variable == "" {
		return nil, fmt.Errorf("variable is required")
	}
	if strings.TrimSpace(config.Message) == "" {
		return nil, fmt.Errorf("message is required")
	}
	if config.Root == "" {
		config.Root = "."
	}
	if config.Kind == "" {
		config.Kind = kindFile
	}
	if config.Kind != kindFile && config.Kind != kindDirectory && config.Kind != kindEither {
		return nil, fmt.Errorf("kind must be file, directory, or either")
	}
	if config.Kind == kindDirectory && len(config.Patterns) > 0 {
		return nil, fmt.Errorf("patterns cannot be used with kind directory")
	}
	for index, pattern := range config.Patterns {
		if err := validatePattern(pattern); err != nil {
			return nil, fmt.Errorf("patterns[%d]: %w", index, err)
		}
	}
	return &Runner{config: config}, nil
}

func (r *Runner) Run(ctx context.Context, request step.Request) (step.Result, error) {
	root, err := resolveRoot(request.RunDir, r.config.Root)
	if err != nil {
		return step.Result{}, err
	}
	if supplied, exists := request.Vars[r.config.Variable]; exists {
		paths, err := r.supplied(supplied, root)
		if err != nil {
			return step.Result{}, fmt.Errorf("pre-supplied variable %q: %w", r.config.Variable, err)
		}
		return r.result(root, paths), nil
	}
	if !request.Interactive {
		return step.Result{}, fmt.Errorf("variable %q is required when stdin is non-interactive; supply it with --var", r.config.Variable)
	}

	paths, err := tui.PickPaths(ctx, request.Stdin, request.Stdout, tui.PathPickerConfig{
		Message: r.config.Message, Root: root, Kind: r.config.Kind, Multiple: r.config.Multiple,
		Required: r.required(), Patterns: r.config.Patterns, ShowHidden: r.config.ShowHidden,
	})
	if err != nil {
		return step.Result{}, fmt.Errorf("selecting path: %w", err)
	}
	paths, err = r.validatePaths(paths, root)
	if err != nil {
		return step.Result{}, fmt.Errorf("validating selected path: %w", err)
	}
	return r.result(root, paths), nil
}

func (r *Runner) supplied(value any, root string) ([]string, error) {
	if !r.config.Multiple {
		pathValue, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("must be a string")
		}
		return r.validatePaths([]string{pathValue}, root)
	}
	values, ok := stringSlice(value)
	if !ok {
		return nil, fmt.Errorf("must be a list of strings")
	}
	return r.validatePaths(values, root)
}

func (r *Runner) validatePaths(values []string, root string) ([]string, error) {
	if r.required() && len(values) == 0 {
		return nil, fmt.Errorf("must contain at least one path")
	}
	if !r.config.Multiple && len(values) != 1 {
		return nil, fmt.Errorf("must contain exactly one path")
	}

	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value == "" {
			if !r.required() && !r.config.Multiple {
				return []string{""}, nil
			}
			return nil, fmt.Errorf("path must not be empty")
		}
		normalized, info, err := inspectPath(root, value)
		if err != nil {
			return nil, err
		}
		if err := r.validateKind(normalized, info); err != nil {
			return nil, err
		}
		if !info.IsDir() && len(r.config.Patterns) > 0 && !matchesAny(r.config.Patterns, normalized) {
			return nil, fmt.Errorf("path %q does not match an allowed pattern", normalized)
		}
		if _, exists := seen[normalized]; exists {
			continue
		}
		seen[normalized] = struct{}{}
		result = append(result, normalized)
	}
	if r.required() && len(result) == 0 {
		return nil, fmt.Errorf("must contain at least one path")
	}
	return result, nil
}

func (r *Runner) validateKind(value string, info os.FileInfo) error {
	switch r.config.Kind {
	case kindFile:
		if !info.Mode().IsRegular() {
			return fmt.Errorf("path %q is not a regular file", value)
		}
	case kindDirectory:
		if !info.IsDir() {
			return fmt.Errorf("path %q is not a directory", value)
		}
	case kindEither:
		if !info.IsDir() && !info.Mode().IsRegular() {
			return fmt.Errorf("path %q is not a regular file or directory", value)
		}
	}
	return nil
}

func (r *Runner) result(root string, paths []string) step.Result {
	if !r.config.Multiple {
		value := paths[0]
		return step.Result{
			Outputs:   map[string]any{"value": value, "root": root},
			Variables: map[string]any{r.config.Variable: value},
		}
	}
	values := make([]any, len(paths))
	for index, value := range paths {
		values[index] = value
	}
	return step.Result{
		Outputs:   map[string]any{"values": values, "count": len(values), "root": root},
		Variables: map[string]any{r.config.Variable: values},
	}
}

func (r *Runner) required() bool { return r.config.Required == nil || *r.config.Required }

func resolveRoot(runDir, configured string) (string, error) {
	if runDir == "" {
		var err error
		runDir, err = os.Getwd()
		if err != nil {
			return "", fmt.Errorf("finding run directory: %w", err)
		}
	}
	root := configured
	if !filepath.IsAbs(root) {
		root = filepath.Join(runDir, root)
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolving path root %s: %w", configured, err)
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", fmt.Errorf("resolving path root %s: %w", absolute, err)
	}
	info, err := os.Stat(canonical)
	if err != nil {
		return "", fmt.Errorf("inspecting path root %s: %w", canonical, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("path root %s is not a directory", canonical)
	}
	return filepath.Clean(canonical), nil
}

func inspectPath(root, value string) (string, os.FileInfo, error) {
	if filepath.IsAbs(value) || windowsAbsolute(value) {
		return "", nil, fmt.Errorf("path %q must be relative to root", value)
	}
	normalized := pathpkg.Clean(filepath.ToSlash(value))
	if normalized == ".." || strings.HasPrefix(normalized, "../") {
		return "", nil, fmt.Errorf("path %q escapes root", value)
	}
	candidate := filepath.Join(root, filepath.FromSlash(normalized))
	canonical, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", nil, fmt.Errorf("resolving path %q: %w", normalized, err)
	}
	contained, err := within(root, canonical)
	if err != nil {
		return "", nil, fmt.Errorf("checking path %q: %w", normalized, err)
	}
	if !contained {
		return "", nil, fmt.Errorf("path %q resolves outside root", normalized)
	}
	info, err := os.Stat(canonical)
	if err != nil {
		return "", nil, fmt.Errorf("inspecting path %q: %w", normalized, err)
	}
	return normalized, info, nil
}

func within(root, candidate string) (bool, error) {
	relative, err := filepath.Rel(root, candidate)
	if err != nil {
		return false, err
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)), nil
}

func validatePattern(pattern string) error {
	if pattern == "" {
		return fmt.Errorf("pattern must not be empty")
	}
	if pathpkg.IsAbs(pattern) || windowsAbsolute(pattern) {
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

func matchesAny(patterns []string, value string) bool {
	for _, pattern := range patterns {
		if doublestar.MatchUnvalidated(pattern, value) {
			return true
		}
	}
	return false
}

func stringSlice(value any) ([]string, bool) {
	reflected := reflect.ValueOf(value)
	if !reflected.IsValid() || (reflected.Kind() != reflect.Slice && reflected.Kind() != reflect.Array) {
		return nil, false
	}
	values := make([]string, reflected.Len())
	for index := range reflected.Len() {
		item := reflected.Index(index)
		if item.Kind() == reflect.Interface {
			if item.IsNil() {
				return nil, false
			}
			item = item.Elem()
		}
		if item.Kind() != reflect.String {
			return nil, false
		}
		values[index] = item.String()
	}
	return values, true
}

func windowsAbsolute(value string) bool {
	if strings.HasPrefix(value, `\\`) {
		return true
	}
	return len(value) >= 3 && ((value[0] >= 'a' && value[0] <= 'z') || (value[0] >= 'A' && value[0] <= 'Z')) && value[1] == ':' && (value[2] == '/' || value[2] == '\\')
}
