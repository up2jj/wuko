// Package changed implements persistent workflow input change detection.
package changed

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	storepkg "github.com/up2jj/wuko/keyvalue"
	"github.com/up2jj/wuko/step"
)

const (
	fingerprintVersion = "wuko-changed-v1"
	identityVersion    = "wuko-changed-identity-v1"
	storeName          = "changed"
)

// Config declares the filesystem and structured values included in one snapshot.
type Config struct {
	Key    string         `yaml:"key,omitempty"`
	Root   string         `yaml:"root,omitempty"`
	Files  []string       `yaml:"files,omitempty"`
	Values map[string]any `yaml:"values,omitempty"`
}

// Runner compares the current input fingerprint with a workflow-local snapshot.
type Runner struct {
	config  Config
	hasKey  bool
	hasRoot bool
}

// Register adds the changed step to a registry.
func Register(registry *step.Registry) error { return registry.Register("changed", New) }

// New decodes and validates a changed step configuration.
func New(raw map[string]any) (step.Runner, error) {
	var config Config
	if err := step.DecodeConfig(raw, &config); err != nil {
		return nil, err
	}
	_, hasKey := raw["key"]
	_, hasRoot := raw["root"]
	runner := &Runner{config: config, hasKey: hasKey, hasRoot: hasRoot}
	if err := runner.validateConfig(); err != nil {
		return nil, err
	}
	return runner, nil
}

// Validate checks local persistence availability without reading inputs or creating a store.
func (r *Runner) Validate(_ context.Context, request step.Request) error {
	_, err := storepkg.Open(request.LocalValueDir, storeName)
	if err != nil {
		return fmt.Errorf("changed snapshots require local workflow storage: %w", err)
	}
	return nil
}

// Run fingerprints the configured inputs and atomically advances the detector snapshot.
func (r *Runner) Run(ctx context.Context, request step.Request) (step.Result, error) {
	if err := ctx.Err(); err != nil {
		return step.Result{}, err
	}
	if err := r.validateResolvedConfig(); err != nil {
		return step.Result{}, err
	}
	store, err := storepkg.Open(request.LocalValueDir, storeName)
	if err != nil {
		return step.Result{}, fmt.Errorf("opening changed snapshot store: %w", err)
	}
	fingerprint, err := r.fingerprint(ctx, request)
	if err != nil {
		return step.Result{}, err
	}
	key := r.config.Key
	if !r.hasKey {
		key = request.StepID
	}
	changed, err := store.SetIfDifferent(ctx, snapshotKey(request, key), fingerprint)
	if err != nil {
		return step.Result{}, fmt.Errorf("updating changed snapshot: %w", err)
	}
	return step.Result{Outputs: map[string]any{"changed": changed}}, nil
}

func (r *Runner) validateConfig() error {
	if r.hasKey && strings.TrimSpace(r.config.Key) == "" {
		return fmt.Errorf("key must not be empty")
	}
	if r.hasRoot && strings.TrimSpace(r.config.Root) == "" {
		return fmt.Errorf("root must not be empty")
	}
	if r.config.Root == "" {
		r.config.Root = "."
	}
	if len(r.config.Files) == 0 && len(r.config.Values) == 0 {
		return fmt.Errorf("files or values must contain at least one input")
	}
	for index, pattern := range r.config.Files {
		if err := validatePattern(pattern); err != nil {
			return fmt.Errorf("files[%d]: %w", index, err)
		}
	}
	if _, err := storepkg.Normalize(r.config.Values); err != nil {
		return fmt.Errorf("values are not JSON-compatible: %w", err)
	}
	return nil
}

func (r *Runner) validateResolvedConfig() error {
	if unresolved(r.config.Key) || unresolved(r.config.Root) {
		return fmt.Errorf("changed configuration contains an unresolved template")
	}
	for _, pattern := range r.config.Files {
		if unresolved(pattern) {
			return fmt.Errorf("changed configuration contains an unresolved template")
		}
	}
	return r.validateConfig()
}

func (r *Runner) fingerprint(ctx context.Context, request step.Request) (string, error) {
	digest := sha256.New()
	writeHashField(digest, fingerprintVersion)
	patterns := slices.Sorted(uniqueStrings(r.config.Files))
	if len(patterns) > 0 {
		writeHashField(digest, filepath.ToSlash(filepath.Clean(r.config.Root)))
	}
	for _, pattern := range patterns {
		writeHashField(digest, "pattern")
		writeHashField(digest, pattern)
	}
	if len(patterns) > 0 {
		root, err := resolveRoot(request.RunDir, r.config.Root)
		if err != nil {
			return "", err
		}
		info, err := os.Stat(root)
		if err != nil {
			return "", fmt.Errorf("inspecting changed root %s: %w", root, err)
		}
		if !info.IsDir() {
			return "", fmt.Errorf("changed root %s is not a directory", root)
		}
		files, err := collectFiles(ctx, root, patterns, request.LocalValueDir)
		if err != nil {
			return "", err
		}
		for _, file := range files {
			if err := hashFile(ctx, digest, root, file); err != nil {
				return "", err
			}
		}
	}
	values, err := storepkg.Normalize(r.config.Values)
	if err != nil {
		return "", fmt.Errorf("normalizing changed values: %w", err)
	}
	encoded, err := json.Marshal(values)
	if err != nil {
		return "", fmt.Errorf("encoding changed values: %w", err)
	}
	writeHashField(digest, "values")
	writeHashField(digest, string(encoded))
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func collectFiles(ctx context.Context, root string, patterns []string, localValueDir string) ([]string, error) {
	matches := make(map[string]struct{})
	rootFS := cancelableFS{ctx: ctx, FS: os.DirFS(root)}
	options := []doublestar.GlobOption{
		doublestar.WithFilesOnly(), doublestar.WithNoFollow(), doublestar.WithNoHidden(), doublestar.WithFailOnIOErrors(),
	}
	for index, configured := range patterns {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		pattern := path.Clean(configured)
		blocked, err := literalPrefixContainsSymlink(root, pattern)
		if err != nil {
			return nil, fmt.Errorf("inspecting files[%d] %q prefix: %w", index, configured, err)
		}
		if blocked {
			continue
		}
		if !hasMeta(pattern) {
			info, err := os.Lstat(filepath.Join(root, filepath.FromSlash(pattern)))
			switch {
			case errors.Is(err, os.ErrNotExist):
				continue
			case err != nil:
				return nil, fmt.Errorf("inspecting files[%d] %q: %w", index, configured, err)
			case info.Mode().IsRegular():
				if !excludedStoreFile(root, pattern, localValueDir) {
					matches[pattern] = struct{}{}
				}
				continue
			case info.IsDir():
				pattern = path.Join(pattern, "**")
			default:
				continue
			}
		}
		err = doublestar.GlobWalk(rootFS, pattern, func(match string, entry fs.DirEntry) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if entry.Type()&fs.ModeSymlink != 0 {
				return nil
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() {
				return nil
			}
			match = path.Clean(match)
			if !excludedStoreFile(root, match, localValueDir) {
				matches[match] = struct{}{}
			}
			return nil
		}, options...)
		if err != nil {
			return nil, fmt.Errorf("matching files[%d] %q: %w", index, configured, err)
		}
	}
	result := slices.Sorted(func(yield func(string) bool) {
		for match := range matches {
			if !yield(match) {
				return
			}
		}
	})
	return result, nil
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

func hashFile(ctx context.Context, digest hash.Hash, root, relative string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	filename := filepath.Join(root, filepath.FromSlash(relative))
	input, err := os.Open(filename)
	if err != nil {
		return fmt.Errorf("opening changed file %s: %w", filename, err)
	}
	writeHashField(digest, "file")
	writeHashField(digest, relative)
	buffer := make([]byte, 32*1024)
	for {
		if err := ctx.Err(); err != nil {
			_ = input.Close()
			return err
		}
		count, readErr := input.Read(buffer)
		if count > 0 {
			_, _ = digest.Write(buffer[:count])
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			_ = input.Close()
			return fmt.Errorf("hashing changed file %s: %w", filename, readErr)
		}
	}
	if err := input.Close(); err != nil {
		return fmt.Errorf("closing changed file %s: %w", filename, err)
	}
	writeHashField(digest, "file-end")
	return nil
}

func snapshotKey(request step.Request, key string) string {
	digest := sha256.New()
	writeHashField(digest, identityVersion)
	source := request.WorkflowSource
	if source == "" {
		source = request.WorkflowName
	}
	writeHashField(digest, source)
	writeHashField(digest, request.WorkflowName)
	writeHashField(digest, key)
	return hex.EncodeToString(digest.Sum(nil))
}

func validatePattern(pattern string) error {
	if strings.TrimSpace(pattern) == "" {
		return fmt.Errorf("path or pattern must not be empty")
	}
	if unresolved(pattern) {
		return nil
	}
	if path.IsAbs(pattern) || windowsAbsolute(pattern) {
		return fmt.Errorf("path or pattern must be relative to root")
	}
	for _, component := range strings.Split(pattern, "/") {
		if component == ".." {
			return fmt.Errorf("path or pattern must not contain a parent directory component")
		}
	}
	if !doublestar.ValidatePattern(pattern) {
		return fmt.Errorf("invalid pattern %q", pattern)
	}
	return nil
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
	resolved, err := filepath.Abs(filepath.Join(runDir, root))
	if err != nil {
		return "", fmt.Errorf("resolving changed root %s: %w", root, err)
	}
	return filepath.Clean(resolved), nil
}

func excludedStoreFile(root, relative, localValueDir string) bool {
	if localValueDir == "" {
		return false
	}
	absolute, err := filepath.Abs(filepath.Join(root, filepath.FromSlash(relative)))
	if err != nil {
		return false
	}
	localValueDir, err = filepath.Abs(localValueDir)
	if err != nil || filepath.Clean(filepath.Dir(absolute)) != filepath.Clean(localValueDir) {
		return false
	}
	name := filepath.Base(absolute)
	return name == storeName+".json" || name == storeName+".lock" || strings.HasPrefix(name, ".wuko-values-")
}

func uniqueStrings(values []string) func(func(string) bool) {
	return func(yield func(string) bool) {
		seen := make(map[string]struct{}, len(values))
		for _, value := range values {
			value = path.Clean(value)
			if _, exists := seen[value]; exists {
				continue
			}
			seen[value] = struct{}{}
			if !yield(value) {
				return
			}
		}
	}
}

func hasMeta(value string) bool {
	escaped := false
	for _, character := range value {
		if escaped {
			escaped = false
			continue
		}
		if character == '\\' {
			escaped = true
			continue
		}
		if strings.ContainsRune("*?[{", character) {
			return true
		}
	}
	return false
}

func unresolved(value string) bool { return strings.Contains(value, "{{") }

func windowsAbsolute(value string) bool {
	if strings.HasPrefix(value, `\\`) {
		return true
	}
	return len(value) >= 3 && ((value[0] >= 'a' && value[0] <= 'z') || (value[0] >= 'A' && value[0] <= 'Z')) && value[1] == ':' && (value[2] == '/' || value[2] == '\\')
}

func writeHashField(digest hash.Hash, value string) {
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(value)))
	_, _ = digest.Write(size[:])
	_, _ = io.WriteString(digest, value)
}

type cancelableFS struct {
	ctx context.Context
	fs.FS
}

func (filesystem cancelableFS) Open(name string) (fs.File, error) {
	if err := filesystem.ctx.Err(); err != nil {
		return nil, err
	}
	file, err := filesystem.FS.Open(name)
	if err != nil {
		return nil, err
	}
	return cancelableFile{ctx: filesystem.ctx, File: file}, nil
}

type cancelableFile struct {
	ctx context.Context
	fs.File
}

func (file cancelableFile) Read(buffer []byte) (int, error) {
	if err := file.ctx.Err(); err != nil {
		return 0, err
	}
	return file.File.Read(buffer)
}

func (file cancelableFile) Stat() (fs.FileInfo, error) {
	if err := file.ctx.Err(); err != nil {
		return nil, err
	}
	return file.File.Stat()
}

func (file cancelableFile) ReadDir(count int) ([]fs.DirEntry, error) {
	if err := file.ctx.Err(); err != nil {
		return nil, err
	}
	directory, ok := file.File.(fs.ReadDirFile)
	if !ok {
		return nil, fmt.Errorf("file does not support reading directories: %w", fs.ErrInvalid)
	}
	return directory.ReadDir(count)
}
