// Package cache implements content-addressed directory caching for workflows.
package cache

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/up2jj/wuko/step"
)

const keyVersion = "wuko-cache-v1"

type Config struct {
	Operation string   `yaml:"operation"`
	CacheDir  string   `yaml:"cache_dir"`
	KeyFiles  []string `yaml:"key_files"`
	Paths     []string `yaml:"paths"`
}

type Runner struct {
	config Config
}

type resolvedConfig struct {
	operation string
	cacheDir  string
	keyFiles  []resolvedPath
	targets   []resolvedPath
}

type resolvedPath struct {
	configured string
	absolute   string
}

type stagedTarget struct {
	target string
	stage  string
	backup string
}

type directoryMetadata struct {
	path    string
	mode    os.FileMode
	modTime time.Time
}

func Register(registry *step.Registry) error { return registry.Register("cache", New) }

func New(raw map[string]any) (step.Runner, error) {
	var config Config
	if err := step.DecodeConfig(raw, &config); err != nil {
		return nil, err
	}
	if err := validateConfig(config); err != nil {
		return nil, err
	}
	return &Runner{config: config}, nil
}

func (r *Runner) Run(ctx context.Context, request step.Request) (step.Result, error) {
	if err := ctx.Err(); err != nil {
		return step.Result{}, err
	}
	config, err := resolveConfig(request.RunDir, r.config)
	if err != nil {
		return step.Result{}, err
	}
	key, err := deriveKey(ctx, config)
	if err != nil {
		return step.Result{}, err
	}
	archivePath := filepath.Join(config.cacheDir, key+".tar.gz")
	switch config.operation {
	case "restore":
		hit, err := restore(ctx, archivePath, config.targets)
		return step.Result{Outputs: map[string]any{"key": key, "hit": hit}}, err
	case "save":
		stored, size, err := save(ctx, archivePath, config.targets)
		return step.Result{Outputs: map[string]any{"key": key, "stored": stored, "size": size}}, err
	default:
		panic("validated cache operation")
	}
}

func validateConfig(config Config) error {
	if config.Operation == "" {
		return fmt.Errorf("operation is required")
	}
	if !templated(config.Operation) && config.Operation != "restore" && config.Operation != "save" {
		return fmt.Errorf("operation must be restore or save")
	}
	if config.CacheDir == "" {
		return fmt.Errorf("cache_dir is required")
	}
	if len(config.KeyFiles) == 0 {
		return fmt.Errorf("key_files must contain at least one path")
	}
	for index, value := range config.KeyFiles {
		if value == "" {
			return fmt.Errorf("key_files[%d] must not be empty", index)
		}
	}
	if len(config.Paths) == 0 {
		return fmt.Errorf("paths must contain at least one path")
	}
	for index, value := range config.Paths {
		if value == "" {
			return fmt.Errorf("paths[%d] must not be empty", index)
		}
	}
	return nil
}

func resolveConfig(runDir string, config Config) (resolvedConfig, error) {
	if templated(config.Operation) || templated(config.CacheDir) {
		return resolvedConfig{}, fmt.Errorf("cache configuration contains an unresolved template")
	}
	if config.Operation != "restore" && config.Operation != "save" {
		return resolvedConfig{}, fmt.Errorf("operation must be restore or save")
	}
	if runDir == "" {
		var err error
		runDir, err = os.Getwd()
		if err != nil {
			return resolvedConfig{}, fmt.Errorf("finding run directory: %w", err)
		}
	}
	cacheDir, err := resolvePath(runDir, config.CacheDir)
	if err != nil {
		return resolvedConfig{}, fmt.Errorf("resolving cache_dir: %w", err)
	}
	result := resolvedConfig{operation: config.Operation, cacheDir: cacheDir}
	for index, value := range config.KeyFiles {
		if templated(value) {
			return resolvedConfig{}, fmt.Errorf("key_files[%d] contains an unresolved template", index)
		}
		absolute, err := resolvePath(runDir, value)
		if err != nil {
			return resolvedConfig{}, fmt.Errorf("resolving key_files[%d]: %w", index, err)
		}
		result.keyFiles = append(result.keyFiles, resolvedPath{configured: normalizeConfiguredPath(value), absolute: absolute})
	}
	for index, value := range config.Paths {
		if templated(value) {
			return resolvedConfig{}, fmt.Errorf("paths[%d] contains an unresolved template", index)
		}
		absolute, err := resolvePath(runDir, value)
		if err != nil {
			return resolvedConfig{}, fmt.Errorf("resolving paths[%d]: %w", index, err)
		}
		result.targets = append(result.targets, resolvedPath{configured: normalizeConfiguredPath(value), absolute: absolute})
	}
	slices.SortFunc(result.keyFiles, compareResolvedPath)
	slices.SortFunc(result.targets, compareResolvedPath)
	seenKeyFiles := make(map[string]bool, len(result.keyFiles))
	for _, keyFile := range result.keyFiles {
		if seenKeyFiles[keyFile.absolute] {
			return resolvedConfig{}, fmt.Errorf("key_files contains duplicate path %q", keyFile.configured)
		}
		seenKeyFiles[keyFile.absolute] = true
	}
	if err := validatePathRelationships(result.cacheDir, result.targets); err != nil {
		return resolvedConfig{}, err
	}
	return result, nil
}

func compareResolvedPath(left, right resolvedPath) int {
	if result := strings.Compare(left.configured, right.configured); result != 0 {
		return result
	}
	return strings.Compare(left.absolute, right.absolute)
}

func validatePathRelationships(cacheDir string, targets []resolvedPath) error {
	canonicalCacheDir, err := canonicalPath(cacheDir)
	if err != nil {
		return fmt.Errorf("resolving cache_dir symbolic links: %w", err)
	}
	canonicalTargets := make([]string, 0, len(targets))
	for i, target := range targets {
		canonicalTarget, err := canonicalPath(target.absolute)
		if err != nil {
			return fmt.Errorf("resolving path %q symbolic links: %w", target.configured, err)
		}
		for j := 0; j < i; j++ {
			if pathsOverlap(canonicalTarget, canonicalTargets[j]) {
				return fmt.Errorf("paths %q and %q overlap", targets[j].configured, target.configured)
			}
		}
		if pathsOverlap(canonicalCacheDir, canonicalTarget) {
			return fmt.Errorf("cache_dir and path %q overlap", target.configured)
		}
		canonicalTargets = append(canonicalTargets, canonicalTarget)
	}
	return nil
}

func canonicalPath(value string) (string, error) {
	current := filepath.Clean(value)
	remaining := make([]string, 0)
	for {
		resolved, err := filepath.EvalSymlinks(current)
		if err == nil {
			parts := append([]string{resolved}, remaining...)
			return filepath.Join(parts...), nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", err
		}
		remaining = append([]string{filepath.Base(current)}, remaining...)
		current = parent
	}
}

func pathsOverlap(left, right string) bool {
	return containsPath(left, right) || containsPath(right, left)
}

func containsPath(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	if err != nil {
		return false
	}
	return relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)))
}

func deriveKey(ctx context.Context, config resolvedConfig) (string, error) {
	digest := sha256.New()
	writeHashField(digest, keyVersion)
	for _, target := range config.targets {
		writeHashField(digest, "target")
		writeHashField(digest, target.configured)
	}
	for _, keyFile := range config.keyFiles {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		info, err := os.Lstat(keyFile.absolute)
		if err != nil {
			return "", fmt.Errorf("inspecting key file %s: %w", keyFile.absolute, err)
		}
		if !info.Mode().IsRegular() {
			return "", fmt.Errorf("key file %s must be a regular file", keyFile.absolute)
		}
		input, err := os.Open(keyFile.absolute)
		if err != nil {
			return "", fmt.Errorf("opening key file %s: %w", keyFile.absolute, err)
		}
		writeHashField(digest, "key-file")
		writeHashField(digest, keyFile.configured)
		if err := copyContext(ctx, digest, input); err != nil {
			_ = input.Close()
			return "", fmt.Errorf("hashing key file %s: %w", keyFile.absolute, err)
		}
		if err := input.Close(); err != nil {
			return "", fmt.Errorf("closing key file %s: %w", keyFile.absolute, err)
		}
		writeHashField(digest, "key-file-end")
	}
	return fmt.Sprintf("%x", digest.Sum(nil)), nil
}

func writeHashField(digest hash.Hash, value string) {
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(value)))
	_, _ = digest.Write(size[:])
	_, _ = io.WriteString(digest, value)
}

func save(ctx context.Context, archivePath string, targets []resolvedPath) (bool, int64, error) {
	for _, target := range targets {
		info, err := os.Lstat(target.absolute)
		if err != nil {
			return false, 0, fmt.Errorf("inspecting cache path %s: %w", target.absolute, err)
		}
		if !info.IsDir() {
			return false, 0, fmt.Errorf("cache path %s must be a directory", target.absolute)
		}
	}
	if err := os.MkdirAll(filepath.Dir(archivePath), 0o755); err != nil {
		return false, 0, fmt.Errorf("creating cache directory: %w", err)
	}
	if info, err := os.Lstat(archivePath); err == nil {
		if !info.Mode().IsRegular() {
			return false, 0, fmt.Errorf("cache entry %s must be a regular file", archivePath)
		}
		return false, info.Size(), nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, 0, fmt.Errorf("inspecting cache entry %s: %w", archivePath, err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(archivePath), ".wuko-cache-*.tar.gz")
	if err != nil {
		return false, 0, fmt.Errorf("creating temporary cache entry: %w", err)
	}
	temporaryPath := temporary.Name()
	open := true
	defer func() {
		_ = os.Remove(temporaryPath)
		if open {
			_ = temporary.Close()
		}
	}()
	gzipWriter := gzip.NewWriter(temporary)
	tarWriter := tar.NewWriter(gzipWriter)
	for index, target := range targets {
		if err := archiveDirectory(ctx, tarWriter, index, target.absolute); err != nil {
			_ = tarWriter.Close()
			_ = gzipWriter.Close()
			return false, 0, err
		}
	}
	if err := tarWriter.Close(); err != nil {
		return false, 0, fmt.Errorf("closing cache archive: %w", err)
	}
	if err := gzipWriter.Close(); err != nil {
		return false, 0, fmt.Errorf("closing compressed cache entry: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return false, 0, fmt.Errorf("syncing cache entry: %w", err)
	}
	info, err := temporary.Stat()
	if err != nil {
		return false, 0, fmt.Errorf("inspecting temporary cache entry: %w", err)
	}
	size := info.Size()
	if err := temporary.Close(); err != nil {
		return false, 0, fmt.Errorf("closing temporary cache entry: %w", err)
	}
	open = false
	if err := ctx.Err(); err != nil {
		return false, 0, err
	}
	if err := os.Link(temporaryPath, archivePath); err != nil {
		if errors.Is(err, os.ErrExist) {
			info, statErr := os.Lstat(archivePath)
			if statErr != nil {
				return false, 0, fmt.Errorf("inspecting concurrently saved cache entry: %w", statErr)
			}
			if !info.Mode().IsRegular() {
				return false, 0, fmt.Errorf("cache entry %s must be a regular file", archivePath)
			}
			return false, info.Size(), nil
		}
		return false, 0, fmt.Errorf("installing cache entry %s: %w", archivePath, err)
	}
	if err := syncDirectory(filepath.Dir(archivePath)); err != nil {
		return false, 0, err
	}
	return true, size, nil
}

func archiveDirectory(ctx context.Context, writer *tar.Writer, index int, root string) error {
	return filepath.WalkDir(root, func(current string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return fmt.Errorf("walking cache path %s: %w", current, walkErr)
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("inspecting cache path entry %s: %w", current, err)
		}
		relative, err := filepath.Rel(root, current)
		if err != nil {
			return fmt.Errorf("resolving cache path entry %s: %w", current, err)
		}
		name := strconv.Itoa(index)
		if relative != "." {
			name = path.Join(name, filepath.ToSlash(relative))
		}
		linkTarget := ""
		switch {
		case info.IsDir(), info.Mode().IsRegular():
		case info.Mode()&os.ModeSymlink != 0:
			linkTarget, err = os.Readlink(current)
			if err != nil {
				return fmt.Errorf("reading cached symbolic link %s: %w", current, err)
			}
			if err := validateFilesystemSymlink(root, current, linkTarget); err != nil {
				return err
			}
		default:
			return fmt.Errorf("cache path entry %s must be a directory, regular file, or symbolic link", current)
		}
		header, err := tar.FileInfoHeader(info, linkTarget)
		if err != nil {
			return fmt.Errorf("creating archive header for %s: %w", current, err)
		}
		header.Name = name
		header.Uid, header.Gid, header.Uname, header.Gname = 0, 0, "", ""
		if err := writer.WriteHeader(header); err != nil {
			return fmt.Errorf("writing archive header for %s: %w", current, err)
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		input, err := os.Open(current)
		if err != nil {
			return fmt.Errorf("opening cache path entry %s: %w", current, err)
		}
		copyErr := copyContext(ctx, writer, input)
		closeErr := input.Close()
		if copyErr != nil {
			return fmt.Errorf("archiving cache path entry %s: %w", current, copyErr)
		}
		if closeErr != nil {
			return fmt.Errorf("closing cache path entry %s: %w", current, closeErr)
		}
		return nil
	})
}

func restore(ctx context.Context, archivePath string, targets []resolvedPath) (bool, error) {
	for _, target := range targets {
		info, err := os.Lstat(target.absolute)
		if err != nil {
			return false, fmt.Errorf("inspecting restore path %s: %w", target.absolute, err)
		}
		if !info.IsDir() {
			return false, fmt.Errorf("restore path %s must be a directory", target.absolute)
		}
	}
	archiveInfo, err := os.Lstat(archivePath)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspecting cache entry %s: %w", archivePath, err)
	}
	if !archiveInfo.Mode().IsRegular() {
		return false, fmt.Errorf("cache entry %s must be a regular file", archivePath)
	}
	archive, err := os.Open(archivePath)
	if err != nil {
		return false, fmt.Errorf("opening cache entry %s: %w", archivePath, err)
	}
	defer archive.Close()
	staged := make([]stagedTarget, len(targets))
	for index, target := range targets {
		stage, err := os.MkdirTemp(filepath.Dir(target.absolute), ".wuko-cache-restore-*")
		if err != nil {
			cleanupStaged(staged)
			return false, fmt.Errorf("creating restore staging directory for %s: %w", target.absolute, err)
		}
		staged[index] = stagedTarget{target: target.absolute, stage: stage}
	}
	defer cleanupStaged(staged)
	if err := extractArchive(ctx, archive, staged); err != nil {
		return false, err
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if err := commitRestore(staged); err != nil {
		return false, err
	}
	return true, nil
}

func extractArchive(ctx context.Context, input io.Reader, staged []stagedTarget) error {
	gzipReader, err := gzip.NewReader(input)
	if err != nil {
		return fmt.Errorf("opening compressed cache entry: %w", err)
	}
	defer gzipReader.Close()
	reader := tar.NewReader(gzipReader)
	seen := make(map[string]bool)
	roots := make([]bool, len(staged))
	directories := make([]directoryMetadata, 0)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("reading cache archive: %w", err)
		}
		index, relative, cleanName, err := validateArchiveName(header.Name, len(staged))
		if err != nil {
			return err
		}
		if seen[cleanName] {
			return fmt.Errorf("cache archive contains duplicate path %q", cleanName)
		}
		seen[cleanName] = true
		destination := staged[index].stage
		if relative == "" {
			if header.Typeflag != tar.TypeDir {
				return fmt.Errorf("cache archive root %q must be a directory", cleanName)
			}
			roots[index] = true
		} else {
			destination = filepath.Join(destination, filepath.FromSlash(relative))
		}
		if !containsPath(staged[index].stage, destination) {
			return fmt.Errorf("cache archive path %q escapes its target", header.Name)
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(destination, 0o700); err != nil {
				return fmt.Errorf("creating restored directory %s: %w", destination, err)
			}
			directories = append(directories, directoryMetadata{path: destination, mode: os.FileMode(header.Mode).Perm(), modTime: header.ModTime})
		case tar.TypeReg, tar.TypeRegA:
			if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
				return fmt.Errorf("creating restored parent directory: %w", err)
			}
			if err := extractRegularFile(ctx, reader, destination, header); err != nil {
				return err
			}
		case tar.TypeSymlink:
			if relative == "" {
				return fmt.Errorf("cache archive root %q cannot be a symbolic link", cleanName)
			}
			if err := validateArchiveSymlink(relative, header.Linkname); err != nil {
				return fmt.Errorf("cache archive symbolic link %q: %w", cleanName, err)
			}
			if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
				return fmt.Errorf("creating restored parent directory: %w", err)
			}
			if err := os.Symlink(header.Linkname, destination); err != nil {
				return fmt.Errorf("restoring symbolic link %s: %w", destination, err)
			}
		default:
			return fmt.Errorf("cache archive path %q has unsupported type %d", cleanName, header.Typeflag)
		}
	}
	if err := copyContext(ctx, io.Discard, gzipReader); err != nil {
		return fmt.Errorf("validating compressed cache entry: %w", err)
	}
	for index, present := range roots {
		if !present {
			return fmt.Errorf("cache archive is missing target root %d", index)
		}
	}
	for index := len(directories) - 1; index >= 0; index-- {
		metadata := directories[index]
		if err := os.Chmod(metadata.path, metadata.mode); err != nil {
			return fmt.Errorf("preserving restored directory mode for %s: %w", metadata.path, err)
		}
		if !metadata.modTime.IsZero() {
			if err := os.Chtimes(metadata.path, metadata.modTime, metadata.modTime); err != nil {
				return fmt.Errorf("preserving restored directory time for %s: %w", metadata.path, err)
			}
		}
	}
	return nil
}

func extractRegularFile(ctx context.Context, reader io.Reader, destination string, header *tar.Header) (resultErr error) {
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, os.FileMode(header.Mode).Perm())
	if err != nil {
		return fmt.Errorf("creating restored file %s: %w", destination, err)
	}
	defer func() {
		closeErr := output.Close()
		if resultErr == nil && closeErr != nil {
			resultErr = fmt.Errorf("closing restored file %s: %w", destination, closeErr)
		}
	}()
	if err := copyContext(ctx, output, reader); err != nil {
		return fmt.Errorf("restoring file %s: %w", destination, err)
	}
	if err := output.Sync(); err != nil {
		return fmt.Errorf("syncing restored file %s: %w", destination, err)
	}
	if err := output.Chmod(os.FileMode(header.Mode).Perm()); err != nil {
		return fmt.Errorf("preserving restored file mode for %s: %w", destination, err)
	}
	if !header.ModTime.IsZero() {
		if err := os.Chtimes(destination, header.ModTime, header.ModTime); err != nil {
			return fmt.Errorf("preserving restored file time for %s: %w", destination, err)
		}
	}
	return nil
}

func validateArchiveName(name string, targetCount int) (int, string, string, error) {
	if name == "" || strings.Contains(name, "\\") || path.IsAbs(name) {
		return 0, "", "", fmt.Errorf("cache archive contains invalid path %q", name)
	}
	clean := path.Clean(name)
	if clean != name || clean == ".." || strings.HasPrefix(clean, "../") {
		return 0, "", "", fmt.Errorf("cache archive contains invalid path %q", name)
	}
	root, relative, _ := strings.Cut(clean, "/")
	index, err := strconv.Atoi(root)
	if err != nil || index < 0 || index >= targetCount || strconv.Itoa(index) != root {
		return 0, "", "", fmt.Errorf("cache archive path %q has invalid target root", name)
	}
	return index, relative, clean, nil
}

func validateFilesystemSymlink(root, linkPath, target string) error {
	if target == "" || filepath.IsAbs(target) || windowsAbsolute(target) {
		return fmt.Errorf("cached symbolic link %s must have a relative in-tree target", linkPath)
	}
	candidate := filepath.Clean(filepath.Join(filepath.Dir(linkPath), target))
	if !containsPath(root, candidate) {
		return fmt.Errorf("cached symbolic link %s escapes its cache path", linkPath)
	}
	return nil
}

func validateArchiveSymlink(relative, target string) error {
	if target == "" || path.IsAbs(target) || windowsAbsolute(target) || strings.Contains(target, "\\") {
		return fmt.Errorf("target must be a relative in-tree path")
	}
	candidate := path.Clean(path.Join(path.Dir(relative), target))
	if candidate == ".." || strings.HasPrefix(candidate, "../") {
		return fmt.Errorf("target escapes its cache path")
	}
	return nil
}

func commitRestore(staged []stagedTarget) error {
	committed := 0
	for index := range staged {
		parent := filepath.Dir(staged[index].target)
		backup, err := os.MkdirTemp(parent, ".wuko-cache-backup-*")
		if err != nil {
			return rollbackRestore(staged, committed, fmt.Errorf("creating restore backup for %s: %w", staged[index].target, err))
		}
		if err := os.Remove(backup); err != nil {
			return rollbackRestore(staged, committed, fmt.Errorf("preparing restore backup for %s: %w", staged[index].target, err))
		}
		staged[index].backup = backup
		if err := os.Rename(staged[index].target, backup); err != nil {
			return rollbackRestore(staged, committed, fmt.Errorf("backing up restore path %s: %w", staged[index].target, err))
		}
		if err := os.Rename(staged[index].stage, staged[index].target); err != nil {
			cause := fmt.Errorf("installing restored path %s: %w", staged[index].target, err)
			if restoreErr := os.Rename(backup, staged[index].target); restoreErr != nil {
				cause = errors.Join(cause, fmt.Errorf("restoring backup %s for %s: %w", backup, staged[index].target, restoreErr))
			} else {
				staged[index].backup = ""
			}
			return rollbackRestore(staged, committed, cause)
		}
		staged[index].stage = ""
		committed++
	}
	for index := range staged {
		if err := os.RemoveAll(staged[index].backup); err != nil {
			return fmt.Errorf("removing restore backup %s: %w", staged[index].backup, err)
		}
		staged[index].backup = ""
	}
	return nil
}

func rollbackRestore(staged []stagedTarget, committed int, cause error) error {
	errorsList := []error{cause}
	for index := committed - 1; index >= 0; index-- {
		if err := os.RemoveAll(staged[index].target); err != nil {
			errorsList = append(errorsList, fmt.Errorf("removing partial restore %s: %w", staged[index].target, err))
			continue
		}
		if err := os.Rename(staged[index].backup, staged[index].target); err != nil {
			errorsList = append(errorsList, fmt.Errorf("rolling back restore path %s from backup %s: %w", staged[index].target, staged[index].backup, err))
			continue
		}
		staged[index].backup = ""
	}
	return errors.Join(errorsList...)
}

func cleanupStaged(staged []stagedTarget) {
	for _, target := range staged {
		if target.stage != "" {
			_ = os.RemoveAll(target.stage)
		}
	}
}

func copyContext(ctx context.Context, destination io.Writer, source io.Reader) error {
	buffer := make([]byte, 32*1024)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		count, readErr := source.Read(buffer)
		if count > 0 {
			if _, err := destination.Write(buffer[:count]); err != nil {
				return err
			}
		}
		if errors.Is(readErr, io.EOF) {
			return nil
		}
		if readErr != nil {
			return readErr
		}
	}
}

func syncDirectory(directory string) error {
	opened, err := os.Open(directory)
	if err != nil {
		return fmt.Errorf("opening cache directory: %w", err)
	}
	defer opened.Close()
	if err := opened.Sync(); err != nil {
		return fmt.Errorf("syncing cache directory: %w", err)
	}
	return nil
}

func resolvePath(runDir, value string) (string, error) {
	if filepath.IsAbs(value) {
		return filepath.Clean(value), nil
	}
	absolute, err := filepath.Abs(filepath.Join(runDir, value))
	if err != nil {
		return "", err
	}
	return filepath.Clean(absolute), nil
}

func normalizeConfiguredPath(value string) string {
	return filepath.ToSlash(filepath.Clean(value))
}

func windowsAbsolute(value string) bool {
	if strings.HasPrefix(value, `\\`) {
		return true
	}
	return len(value) >= 3 && ((value[0] >= 'a' && value[0] <= 'z') || (value[0] >= 'A' && value[0] <= 'Z')) && value[1] == ':' && (value[2] == '/' || value[2] == '\\')
}

func templated(value string) bool { return strings.Contains(value, "{{") }
