package file

import (
	"container/heap"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/up2jj/wuko/step"
)

const defaultTailMaxBytes int64 = 1 << 20

var sizeUnits = []struct {
	suffix     string
	multiplier int64
}{
	{"TiB", 1 << 40}, {"GiB", 1 << 30}, {"MiB", 1 << 20}, {"KiB", 1 << 10}, {"B", 1},
}

func (r *Runner) validateAdvanced(resolved bool) error {
	validateSize := func(field, value string) error {
		if value == "" || (!resolved && templated(value)) {
			return nil
		}
		if _, err := parseSize(value); err != nil {
			return fmt.Errorf("%s: %w", field, err)
		}
		return nil
	}
	validateDuration := func(field, value string) error {
		if value == "" || (!resolved && templated(value)) {
			return nil
		}
		duration, err := time.ParseDuration(value)
		if err != nil || duration < 0 {
			return fmt.Errorf("%s must be a non-negative duration", field)
		}
		return nil
	}
	validateTimestamp := func(field, value string) error {
		if value == "" || value == "now" || (!resolved && templated(value)) {
			return nil
		}
		if _, err := time.Parse(time.RFC3339, value); err != nil {
			return fmt.Errorf("%s must be now or an RFC3339 timestamp", field)
		}
		return nil
	}

	switch r.config.Operation {
	case operationFind:
		if len(r.config.Patterns) == 0 {
			return fmt.Errorf("patterns must contain at least one pattern")
		}
		for index, pattern := range r.config.Patterns {
			if !resolved && templated(pattern) {
				continue
			}
			if err := validateFindPattern(pattern); err != nil {
				return fmt.Errorf("patterns[%d]: %w", index, err)
			}
		}
		seenTypes := make(map[string]bool, len(r.config.Types))
		for _, entryType := range r.config.Types {
			if !resolved && templated(entryType) {
				continue
			}
			if entryType != "file" && entryType != "directory" && entryType != "symlink" {
				return fmt.Errorf("types entries must be file, directory, or symlink")
			}
			if seenTypes[entryType] {
				return fmt.Errorf("types must not contain duplicate %q", entryType)
			}
			seenTypes[entryType] = true
		}
		for field, value := range map[string]string{"min_size": r.config.MinSize, "max_size": r.config.MaxSize} {
			if err := validateSize(field, value); err != nil {
				return err
			}
		}
		for field, value := range map[string]string{"min_age": r.config.MinAge, "max_age": r.config.MaxAge} {
			if err := validateDuration(field, value); err != nil {
				return err
			}
		}
		if resolved || (!templated(r.config.MinSize) && !templated(r.config.MaxSize)) {
			if err := validateSizeRange(r.config.MinSize, r.config.MaxSize); err != nil {
				return err
			}
		}
		if resolved || (!templated(r.config.MinAge) && !templated(r.config.MaxAge)) {
			if err := validateAgeRange(r.config.MinAge, r.config.MaxAge); err != nil {
				return err
			}
		}
	case operationLink:
		if r.config.Destination == "" {
			return fmt.Errorf("destination must not be empty")
		}
		if resolved || !templated(r.config.LinkType) {
			if r.config.LinkType != "symbolic" && r.config.LinkType != "hard" {
				return fmt.Errorf("link_type must be symbolic or hard")
			}
		}
		return validateReplace(r.config.Replace, resolved)
	case operationTruncate:
		return validateSize("size", r.config.Size)
	case operationTail:
		if r.present["lines"] && r.present["bytes"] {
			return fmt.Errorf("lines and bytes are mutually exclusive")
		}
		if r.config.Lines < 0 {
			return fmt.Errorf("lines must not be negative")
		}
		if r.present["max_bytes"] && r.present["bytes"] {
			return fmt.Errorf("max_bytes is only allowed with lines")
		}
		if err := validateSize("bytes", r.config.Bytes); err != nil {
			return err
		}
		return validateSize("max_bytes", r.config.MaxBytes)
	case operationDiskUsage:
		if r.config.Largest < 0 {
			return fmt.Errorf("largest must not be negative")
		}
	case operationAtomicSwap:
		if r.config.Destination == "" {
			return fmt.Errorf("destination must not be empty")
		}
		return validateReplace(r.config.Replace, resolved)
	case operationTouch:
		if err := validateTimestamp("accessed_at", r.config.AccessedAt); err != nil {
			return err
		}
		return validateTimestamp("modified_at", r.config.ModifiedAt)
	}
	return nil
}

func validateReplace(value string, resolved bool) error {
	if value == "" || (!resolved && templated(value)) {
		return nil
	}
	if value != "never" && value != "file" && value != "any" {
		return fmt.Errorf("replace must be never, file, or any")
	}
	return nil
}

func validateFindPattern(pattern string) error {
	if pattern == "" {
		return fmt.Errorf("pattern must not be empty")
	}
	if path.IsAbs(pattern) || windowsAbsolutePath(pattern) {
		return fmt.Errorf("pattern must be relative to path")
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

func windowsAbsolutePath(value string) bool {
	return strings.HasPrefix(value, `\\`) || (len(value) >= 3 && value[1] == ':' && (value[2] == '/' || value[2] == '\\'))
}

func parseSize(value string) (int64, error) {
	if value == "" {
		return 0, nil
	}
	for _, unit := range sizeUnits {
		if !strings.HasSuffix(value, unit.suffix) {
			continue
		}
		number := strings.TrimSuffix(value, unit.suffix)
		if number == "" || strings.HasPrefix(number, "+") || strings.HasPrefix(number, "-") {
			break
		}
		parsed, err := strconv.ParseInt(number, 10, 64)
		if err != nil || parsed < 0 || (parsed > 0 && parsed > (1<<63-1)/unit.multiplier) {
			break
		}
		return parsed * unit.multiplier, nil
	}
	return 0, fmt.Errorf("size must be a non-negative integer followed by B, KiB, MiB, GiB, or TiB")
}

func validateSizeRange(minimum, maximum string) error {
	if minimum == "" || maximum == "" {
		return nil
	}
	minSize, minErr := parseSize(minimum)
	maxSize, maxErr := parseSize(maximum)
	if minErr == nil && maxErr == nil && minSize > maxSize {
		return fmt.Errorf("min_size must not exceed max_size")
	}
	return nil
}

func validateAgeRange(minimum, maximum string) error {
	if minimum == "" || maximum == "" {
		return nil
	}
	minAge, minErr := time.ParseDuration(minimum)
	maxAge, maxErr := time.ParseDuration(maximum)
	if minErr == nil && maxErr == nil && minAge > maxAge {
		return fmt.Errorf("min_age must not exceed max_age")
	}
	return nil
}

func (r *Runner) find(ctx context.Context, root string) (step.Result, error) {
	info, err := os.Stat(root)
	if err != nil {
		return step.Result{}, fmt.Errorf("inspecting find path %s: %w", root, err)
	}
	if !info.IsDir() {
		return step.Result{}, fmt.Errorf("find path %s is not a directory", root)
	}
	minSize, _ := parseSize(r.config.MinSize)
	maxSize, _ := parseSize(r.config.MaxSize)
	minAge, _ := time.ParseDuration(r.config.MinAge)
	maxAge, _ := time.ParseDuration(r.config.MaxAge)
	mode, _ := parseMode(r.config.Mode)
	types := make(map[string]bool, len(r.config.Types))
	for _, entryType := range r.config.Types {
		types[entryType] = true
	}
	now := time.Now()
	entries := make([]map[string]any, 0)
	err = filepath.WalkDir(root, func(entryPath string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entryPath == root {
			return nil
		}
		relative, err := filepath.Rel(root, entryPath)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		matched := false
		for _, pattern := range r.config.Patterns {
			ok, matchErr := doublestar.Match(pattern, relative)
			if matchErr != nil {
				return matchErr
			}
			if ok {
				matched = true
				break
			}
		}
		if !matched {
			return nil
		}
		entryInfo, err := entry.Info()
		if err != nil {
			return err
		}
		entryType := fileType(entryInfo.Mode())
		if entryType == "other" || (len(types) > 0 && !types[entryType]) {
			return nil
		}
		if r.config.MinSize != "" && entryInfo.Size() < minSize || r.config.MaxSize != "" && entryInfo.Size() > maxSize {
			return nil
		}
		age := max(time.Duration(0), now.Sub(entryInfo.ModTime()))
		if r.config.MinAge != "" && age < minAge || r.config.MaxAge != "" && age > maxAge {
			return nil
		}
		if r.config.Mode != "" && entryInfo.Mode().Perm()&mode != mode {
			return nil
		}
		entries = append(entries, fileEntry(relative, entryInfo))
		return nil
	})
	if err != nil {
		return step.Result{}, fmt.Errorf("finding entries beneath %s: %w", root, err)
	}
	slices.SortFunc(entries, func(a, b map[string]any) int { return strings.Compare(a["path"].(string), b["path"].(string)) })
	values := make([]any, len(entries))
	for index := range entries {
		values[index] = entries[index]
	}
	return step.Result{Outputs: map[string]any{"root": root, "count": len(values), "entries": values}}, nil
}

func (r *Runner) link(ctx context.Context, runDir, target string) (step.Result, error) {
	if err := ctx.Err(); err != nil {
		return step.Result{}, err
	}
	destination, err := resolvePath(runDir, r.config.Destination)
	if err != nil {
		return step.Result{}, err
	}
	if target == destination {
		return step.Result{}, fmt.Errorf("link target and destination must differ")
	}
	if r.config.LinkType == "hard" {
		info, err := os.Lstat(target)
		if err != nil {
			return step.Result{}, fmt.Errorf("inspecting hard-link target %s: %w", target, err)
		}
		if !info.Mode().IsRegular() {
			return step.Result{}, fmt.Errorf("hard-link target %s must be a regular file", target)
		}
	}
	replaced, err := prepareReplacement(ctx, runDir, destination, r.config.Replace)
	if err != nil {
		return step.Result{}, err
	}
	if r.config.LinkType == "symbolic" {
		err = os.Symlink(target, destination)
	} else {
		err = os.Link(target, destination)
	}
	if err != nil {
		return step.Result{}, fmt.Errorf("creating %s link %s: %w", r.config.LinkType, destination, err)
	}
	if err := syncDirectory(filepath.Dir(destination)); err != nil {
		return step.Result{}, err
	}
	return step.Result{Outputs: map[string]any{
		"path": target, "destination": destination, "link_type": r.config.LinkType, "replaced": replaced,
	}}, nil
}

func prepareReplacement(ctx context.Context, runDir, destination, policy string) (bool, error) {
	if policy == "" {
		policy = "never"
	}
	info, err := os.Lstat(destination)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspecting destination %s: %w", destination, err)
	}
	if policy == "never" {
		return false, fmt.Errorf("destination %s already exists; set replace to file or any", destination)
	}
	if info.IsDir() && policy != "any" {
		return false, fmt.Errorf("destination %s is a directory; set replace to any", destination)
	}
	if info.IsDir() {
		if err := validateDestructivePath(runDir, destination); err != nil {
			return false, err
		}
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if info.IsDir() {
		err = removeAll(ctx, destination)
	} else {
		err = os.Remove(destination)
	}
	if err != nil {
		return false, fmt.Errorf("replacing destination %s: %w", destination, err)
	}
	return true, nil
}

func (r *Runner) truncate(ctx context.Context, filePath string) (step.Result, error) {
	info, err := os.Lstat(filePath)
	if err != nil {
		return step.Result{}, fmt.Errorf("inspecting file %s: %w", filePath, err)
	}
	if !info.Mode().IsRegular() {
		return step.Result{}, fmt.Errorf("truncate path %s must be a regular file", filePath)
	}
	if err := ctx.Err(); err != nil {
		return step.Result{}, err
	}
	size, _ := parseSize(r.config.Size)
	if err := os.Truncate(filePath, size); err != nil {
		return step.Result{}, fmt.Errorf("truncating file %s: %w", filePath, err)
	}
	return step.Result{Outputs: map[string]any{"path": filePath, "previous_size": info.Size(), "size": size}}, nil
}

func (r *Runner) tail(ctx context.Context, filePath string) (step.Result, error) {
	input, err := os.Open(filePath)
	if err != nil {
		return step.Result{}, fmt.Errorf("opening file %s: %w", filePath, err)
	}
	defer input.Close()
	info, err := input.Stat()
	if err != nil {
		return step.Result{}, fmt.Errorf("inspecting file %s: %w", filePath, err)
	}
	if !info.Mode().IsRegular() {
		return step.Result{}, fmt.Errorf("tail path %s must be a regular file", filePath)
	}
	limit := defaultTailMaxBytes
	if r.config.Bytes != "" {
		limit, _ = parseSize(r.config.Bytes)
	} else if r.config.MaxBytes != "" {
		limit, _ = parseSize(r.config.MaxBytes)
	}
	readSize := min(info.Size(), limit)
	content := make([]byte, readSize)
	for offset := int64(0); offset < readSize; {
		if err := ctx.Err(); err != nil {
			return step.Result{}, err
		}
		count, readErr := input.ReadAt(content[offset:], info.Size()-readSize+offset)
		offset += int64(count)
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return step.Result{}, fmt.Errorf("reading file %s: %w", filePath, readErr)
		}
		if count == 0 {
			break
		}
	}
	startInBuffer := 0
	if r.config.Bytes == "" {
		lines := 10
		if r.present["lines"] {
			lines = r.config.Lines
		}
		startInBuffer = tailLineStart(content, lines)
	}
	content = content[startInBuffer:]
	absoluteStart := info.Size() - readSize + int64(startInBuffer)
	return step.Result{Outputs: map[string]any{
		"path": filePath, "content": string(content), "size": int64(len(content)), "truncated": absoluteStart > 0,
	}}, nil
}

func tailLineStart(content []byte, lines int) int {
	if lines == 0 {
		return len(content)
	}
	end := len(content)
	if end > 0 && content[end-1] == '\n' {
		end--
	}
	for index, found := end-1, 0; index >= 0; index-- {
		if content[index] != '\n' {
			continue
		}
		found++
		if found == lines {
			return index + 1
		}
	}
	return 0
}

type sizedEntry struct {
	path     string
	typeName string
	size     int64
}

type sizedEntryHeap []sizedEntry

func (h sizedEntryHeap) Len() int { return len(h) }

func (h sizedEntryHeap) Less(i, j int) bool {
	return compareSizedEntry(h[i], h[j]) > 0
}

func (h sizedEntryHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }

func (h *sizedEntryHeap) Push(value any) {
	*h = append(*h, value.(sizedEntry))
}

func (h *sizedEntryHeap) Pop() any {
	old := *h
	last := len(old) - 1
	value := old[last]
	*h = old[:last]
	return value
}

func (r *Runner) diskUsage(ctx context.Context, root string) (step.Result, error) {
	rootInfo, err := os.Lstat(root)
	if err != nil {
		return step.Result{}, fmt.Errorf("inspecting disk usage path %s: %w", root, err)
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 {
		return step.Result{}, fmt.Errorf("disk usage path %s must not be a symbolic link", root)
	}
	largestLimit := 10
	if r.present["largest"] {
		largestLimit = r.config.Largest
	}
	var total int64
	var fileCount, directoryCount, symlinkCount int
	directorySizes := make(map[string]int64)
	largest := make(sizedEntryHeap, 0, min(largestLimit, 10))
	addLargest := func(candidate sizedEntry) {
		if largestLimit == 0 {
			return
		}
		if len(largest) < largestLimit {
			heap.Push(&largest, candidate)
			return
		}
		if compareSizedEntry(candidate, largest[0]) < 0 {
			largest[0] = candidate
			heap.Fix(&largest, 0)
		}
	}
	err = filepath.WalkDir(root, func(entryPath string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			symlinkCount++
		case info.IsDir():
			directoryCount++
			directorySizes[entryPath] += 0
		case info.Mode().IsRegular():
			fileCount++
			total += info.Size()
			relative, _ := filepath.Rel(root, entryPath)
			addLargest(sizedEntry{path: filepath.ToSlash(relative), typeName: "file", size: info.Size()})
			for directory := filepath.Dir(entryPath); pathWithin(root, directory); directory = filepath.Dir(directory) {
				directorySizes[directory] += info.Size()
				if directory == root {
					break
				}
			}
		}
		return nil
	})
	if err != nil {
		return step.Result{}, fmt.Errorf("calculating disk usage for %s: %w", root, err)
	}
	for directory, size := range directorySizes {
		if directory == root {
			continue
		}
		relative, _ := filepath.Rel(root, directory)
		addLargest(sizedEntry{path: filepath.ToSlash(relative), typeName: "directory", size: size})
	}
	slices.SortFunc(largest, compareSizedEntry)
	values := make([]any, len(largest))
	for index, entry := range largest {
		values[index] = map[string]any{"path": entry.path, "type": entry.typeName, "size": entry.size}
	}
	return step.Result{Outputs: map[string]any{
		"path": root, "size": total, "file_count": fileCount, "directory_count": directoryCount,
		"symlink_count": symlinkCount, "largest_entries": values,
	}}, nil
}

func compareSizedEntry(a, b sizedEntry) int {
	if a.size > b.size {
		return -1
	}
	if a.size < b.size {
		return 1
	}
	return strings.Compare(a.path, b.path)
}

func pathWithin(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func (r *Runner) atomicSwap(ctx context.Context, runDir, staging string) (step.Result, error) {
	destination, err := resolvePath(runDir, r.config.Destination)
	if err != nil {
		return step.Result{}, err
	}
	if rootPath(staging) || rootPath(destination) {
		return step.Result{}, fmt.Errorf("atomic swap paths must not be filesystem roots")
	}
	physicalStaging, err := physicalPath(staging)
	if err != nil {
		return step.Result{}, fmt.Errorf("resolving atomic swap path %s: %w", staging, err)
	}
	physicalDestination, err := physicalPath(destination)
	if err != nil {
		return step.Result{}, fmt.Errorf("resolving atomic swap destination %s: %w", destination, err)
	}
	if pathWithin(physicalStaging, physicalDestination) || pathWithin(physicalDestination, physicalStaging) {
		return step.Result{}, fmt.Errorf("atomic swap paths must not overlap")
	}
	info, err := os.Lstat(staging)
	if err != nil {
		return step.Result{}, fmt.Errorf("inspecting staging directory %s: %w", staging, err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return step.Result{}, fmt.Errorf("atomic swap path %s must be a directory", staging)
	}
	if err := validateDestructivePath(runDir, staging); err != nil {
		return step.Result{}, err
	}
	if err := ctx.Err(); err != nil {
		return step.Result{}, err
	}
	destinationInfo, err := os.Lstat(destination)
	if errors.Is(err, os.ErrNotExist) {
		if err := renameNoReplace(staging, destination); err != nil {
			return step.Result{}, fmt.Errorf("installing staging directory %s at %s: %w", staging, destination, err)
		}
		if err := syncDirectory(filepath.Dir(destination)); err != nil {
			return step.Result{}, err
		}
		if filepath.Dir(staging) != filepath.Dir(destination) {
			if err := syncDirectory(filepath.Dir(staging)); err != nil {
				return step.Result{}, err
			}
		}
		return step.Result{Outputs: map[string]any{"path": staging, "destination": destination, "replaced": false}}, nil
	}
	if err != nil {
		return step.Result{}, fmt.Errorf("inspecting atomic swap destination %s: %w", destination, err)
	}
	if !destinationInfo.IsDir() || destinationInfo.Mode()&os.ModeSymlink != 0 {
		return step.Result{}, fmt.Errorf("atomic swap destination %s must be a directory", destination)
	}
	if err := validateDestructivePath(runDir, destination); err != nil {
		return step.Result{}, err
	}
	if r.config.Replace != "any" {
		return step.Result{}, fmt.Errorf("destination %s exists; set replace to any", destination)
	}
	if err := exchangePaths(staging, destination); err != nil {
		return step.Result{}, fmt.Errorf("atomically exchanging %s and %s: %w", staging, destination, err)
	}
	if err := syncDirectory(filepath.Dir(destination)); err != nil {
		return step.Result{}, fmt.Errorf("new destination installed but syncing its parent failed: %w", err)
	}
	if filepath.Dir(staging) != filepath.Dir(destination) {
		if err := syncDirectory(filepath.Dir(staging)); err != nil {
			return step.Result{}, fmt.Errorf("new destination installed but syncing the staging parent failed: %w", err)
		}
	}
	if err := removeAll(ctx, staging); err != nil {
		return step.Result{}, fmt.Errorf("new destination installed but removing displaced tree at %s failed: %w", staging, err)
	}
	if err := syncDirectory(filepath.Dir(staging)); err != nil {
		return step.Result{}, fmt.Errorf("new destination installed and displaced tree removed but syncing the staging parent failed: %w", err)
	}
	return step.Result{Outputs: map[string]any{"path": staging, "destination": destination, "replaced": true}}, nil
}

func (r *Runner) permissions(ctx context.Context, root string) (step.Result, error) {
	mode, _ := parseMode(r.config.Mode)
	info, err := os.Lstat(root)
	if err != nil {
		return step.Result{}, fmt.Errorf("inspecting permissions path %s: %w", root, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return step.Result{}, fmt.Errorf("permissions path %s must not be a symbolic link", root)
	}
	changed, skipped := 0, 0
	apply := func(entryPath string, entryInfo os.FileInfo) error {
		if entryInfo.Mode().Perm() == mode {
			return nil
		}
		if err := os.Chmod(entryPath, mode); err != nil {
			return err
		}
		changed++
		return nil
	}
	if !r.config.Recursive {
		if err := ctx.Err(); err != nil {
			return step.Result{}, err
		}
		if err := apply(root, info); err != nil {
			return step.Result{}, fmt.Errorf("changing permissions of %s: %w", root, err)
		}
	} else {
		type permissionEntry struct {
			path string
			info os.FileInfo
		}
		entries := make([]permissionEntry, 0)
		err = filepath.WalkDir(root, func(entryPath string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			entryInfo, err := entry.Info()
			if err != nil {
				return err
			}
			if entryInfo.Mode()&os.ModeSymlink != 0 {
				skipped++
				return nil
			}
			entries = append(entries, permissionEntry{path: entryPath, info: entryInfo})
			return nil
		})
		if err != nil {
			return step.Result{}, fmt.Errorf("inspecting permissions beneath %s: %w", root, err)
		}
		for index := len(entries) - 1; index >= 0; index-- {
			if err := ctx.Err(); err != nil {
				return step.Result{}, fmt.Errorf("changing permissions beneath %s after %d changes: %w", root, changed, err)
			}
			if err := apply(entries[index].path, entries[index].info); err != nil {
				return step.Result{}, fmt.Errorf("changing permissions beneath %s after %d changes: %w", root, changed, err)
			}
		}
	}
	return step.Result{Outputs: map[string]any{"path": root, "mode": formatMode(mode), "changed": changed, "skipped_links": skipped}}, nil
}

func (r *Runner) touch(ctx context.Context, filePath string) (step.Result, error) {
	mode := os.FileMode(0o644)
	if r.config.Mode != "" {
		mode, _ = parseMode(r.config.Mode)
	}
	info, err := os.Lstat(filePath)
	created := false
	if errors.Is(err, os.ErrNotExist) {
		create := true
		if r.present["create"] {
			create = r.config.Create
		}
		if !create {
			return step.Result{}, fmt.Errorf("touch path %s does not exist and create is false", filePath)
		}
		if err := ctx.Err(); err != nil {
			return step.Result{}, err
		}
		file, createErr := os.OpenFile(filePath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
		if createErr != nil {
			return step.Result{}, fmt.Errorf("creating touch file %s: %w", filePath, createErr)
		}
		if closeErr := file.Close(); closeErr != nil {
			return step.Result{}, fmt.Errorf("closing touch file %s: %w", filePath, closeErr)
		}
		if chmodErr := os.Chmod(filePath, mode); chmodErr != nil {
			return step.Result{}, fmt.Errorf("setting touch file mode: %w", chmodErr)
		}
		created = true
		info, err = os.Lstat(filePath)
	}
	if err != nil {
		return step.Result{}, fmt.Errorf("inspecting touch path %s: %w", filePath, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return step.Result{}, fmt.Errorf("touch path %s must not be a symbolic link", filePath)
	}
	if err := ctx.Err(); err != nil {
		return step.Result{}, err
	}
	now := time.Now()
	accessed, modified := accessTime(info), info.ModTime()
	if r.config.AccessedAt == "" && r.config.ModifiedAt == "" {
		accessed, modified = now, now
	} else {
		if r.config.AccessedAt != "" {
			accessed, _ = parseTimestamp(r.config.AccessedAt, now)
		}
		if r.config.ModifiedAt != "" {
			modified, _ = parseTimestamp(r.config.ModifiedAt, now)
		}
	}
	if err := os.Chtimes(filePath, accessed, modified); err != nil {
		return step.Result{}, fmt.Errorf("updating timestamps of %s: %w", filePath, err)
	}
	resultInfo, err := os.Stat(filePath)
	if err != nil {
		return step.Result{}, fmt.Errorf("inspecting touched path %s: %w", filePath, err)
	}
	return step.Result{Outputs: map[string]any{
		"path": filePath, "created": created, "mode": formatMode(resultInfo.Mode()),
		"accessed_at": accessTime(resultInfo).UTC().Format(time.RFC3339Nano), "modified_at": resultInfo.ModTime().UTC().Format(time.RFC3339Nano),
	}}, nil
}

func parseTimestamp(value string, now time.Time) (time.Time, error) {
	if value == "now" {
		return now, nil
	}
	return time.Parse(time.RFC3339, value)
}

func validateDestructivePath(runDir, candidate string) error {
	if rootPath(candidate) {
		return fmt.Errorf("refusing to replace filesystem root %s", candidate)
	}
	resolvedRunDir, err := resolvePath("", runDir)
	if err != nil {
		return fmt.Errorf("resolving run directory: %w", err)
	}
	same, err := sameFile(candidate, resolvedRunDir)
	if err != nil {
		return fmt.Errorf("comparing replacement path %s with run directory: %w", candidate, err)
	}
	if same {
		return fmt.Errorf("refusing to replace run directory %s", candidate)
	}
	return nil
}

func sameFile(first, second string) (bool, error) {
	firstInfo, err := os.Stat(first)
	if err != nil {
		return false, err
	}
	secondInfo, err := os.Stat(second)
	if err != nil {
		return false, err
	}
	return os.SameFile(firstInfo, secondInfo), nil
}

func physicalPath(value string) (string, error) {
	resolved, err := filepath.EvalSymlinks(value)
	if err == nil {
		return filepath.Clean(resolved), nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	parent := filepath.Dir(value)
	if parent == value {
		return filepath.Clean(value), nil
	}
	physicalParent, err := physicalPath(parent)
	if err != nil {
		return "", err
	}
	return filepath.Join(physicalParent, filepath.Base(value)), nil
}
