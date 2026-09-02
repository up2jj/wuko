package http

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

type downloadTarget struct {
	destination   string
	temporary     *os.File
	temporaryPath string
	overwrite     bool
}

func prepareDownload(ctx context.Context, runDir string, config DownloadConfig) (*downloadTarget, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	destination, err := resolvePath(runDir, config.Path)
	if err != nil {
		return nil, fmt.Errorf("resolving download path: %w", err)
	}
	mode := os.FileMode(0o644)
	info, err := os.Lstat(destination)
	if err == nil {
		if info.IsDir() {
			return nil, fmt.Errorf("download destination %s is a directory", destination)
		}
		if !config.Overwrite {
			return nil, fmt.Errorf("download destination %s already exists; set overwrite to true", destination)
		}
		if info.Mode().IsRegular() {
			mode = info.Mode().Perm()
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("inspecting download destination %s: %w", destination, err)
	}
	directory := filepath.Dir(destination)
	temporary, err := os.CreateTemp(directory, ".wuko-download-*")
	if err != nil {
		return nil, fmt.Errorf("creating temporary download for %s: %w", destination, err)
	}
	target := &downloadTarget{
		destination: destination, temporary: temporary,
		temporaryPath: temporary.Name(), overwrite: config.Overwrite,
	}
	if err := temporary.Chmod(mode); err != nil {
		cleanupErr := target.Cleanup()
		return nil, errors.Join(fmt.Errorf("setting download file mode: %w", err), cleanupErr)
	}
	return target, nil
}

func (target *downloadTarget) Path() string { return target.destination }

func (target *downloadTarget) Write(ctx context.Context, source io.Reader) (int64, error) {
	if target.temporary == nil {
		return 0, fmt.Errorf("download target is not open")
	}
	buffer := make([]byte, 32*1024)
	var size int64
	for {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		count, readErr := source.Read(buffer)
		if count > 0 {
			written, err := target.temporary.Write(buffer[:count])
			size += int64(written)
			if err != nil {
				return 0, fmt.Errorf("writing temporary download: %w", err)
			}
			if written != count {
				return 0, io.ErrShortWrite
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return 0, fmt.Errorf("reading response body: %w", readErr)
		}
	}
	if err := target.temporary.Sync(); err != nil {
		return 0, fmt.Errorf("syncing temporary download: %w", err)
	}
	if err := target.temporary.Close(); err != nil {
		target.temporary = nil
		return 0, fmt.Errorf("closing temporary download: %w", err)
	}
	target.temporary = nil
	if err := installDownload(target.temporaryPath, target.destination, target.overwrite); err != nil {
		if !target.overwrite && errors.Is(err, os.ErrExist) {
			return 0, fmt.Errorf("download destination %s already exists; set overwrite to true", target.destination)
		}
		return 0, fmt.Errorf("installing download at %s: %w", target.destination, err)
	}
	target.temporaryPath = ""
	if err := syncDirectory(filepath.Dir(target.destination)); err != nil {
		return 0, err
	}
	return size, nil
}

func (target *downloadTarget) Cleanup() error {
	var errs []error
	if target.temporary != nil {
		if err := target.temporary.Close(); err != nil {
			errs = append(errs, fmt.Errorf("closing temporary download: %w", err))
		}
		target.temporary = nil
	}
	if target.temporaryPath != "" {
		if err := os.Remove(target.temporaryPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, fmt.Errorf("removing temporary download: %w", err))
		}
		target.temporaryPath = ""
	}
	return errors.Join(errs...)
}

func installDownload(source, destination string, overwrite bool) error {
	if overwrite {
		return os.Rename(source, destination)
	}
	return renameDownloadNoReplace(source, destination)
}

// syncDirectory flushes a directory entry so a rename into it survives a crash.
// Filesystems that cannot do this -- several network ones -- report it rather than
// silently skipping it, and refusing something the filesystem never offered is not
// a durability failure. Treating it as one would fail a step whose file is already
// installed, which a retry cannot then repeat: the destination now exists.
func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("opening directory %s: %w", path, err)
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil && !directorySyncUnsupported(err) {
		return fmt.Errorf("syncing directory %s: %w", path, err)
	}
	return nil
}

func directorySyncUnsupported(err error) bool {
	return errors.Is(err, syscall.EINVAL) || errors.Is(err, syscall.ENOTSUP) || errors.Is(err, syscall.ENOSYS)
}
