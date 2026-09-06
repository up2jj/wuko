package executor

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"time"

	"github.com/up2jj/wuko/process"
)

// FileInfo describes one path inside an execution target's filesystem. Mode carries the
// same bits as os.FileInfo.Mode, so a symbolic link reports fs.ModeSymlink rather than the
// mode of whatever it points at.
type FileInfo struct {
	Size    int64
	Mode    fs.FileMode
	ModTime time.Time
}

// WriteOptions controls how a file is installed on an execution target.
type WriteOptions struct {
	Mode    fs.FileMode
	Replace bool
}

// FileSystem is an optional executor capability for sessions that can read and write files
// on their own target. Steps that touch files inside an executor scope require it: a
// session that does not implement it cannot say whether its filesystem is the host's, and
// quietly falling back to the host would edit the wrong machine.
//
// Paths arrive already resolved against the host run directory, so an implementation
// translates them exactly as it translates a command's working directory. Stat and
// ReadFile report a missing path with an error satisfying errors.Is(err, fs.ErrNotExist).
// A positive maximum bounds the returned data to maximum+1 bytes, so callers can reject an
// oversized file without first allocating all of it. Zero means unlimited.
type FileSystem interface {
	Stat(context.Context, string) (FileInfo, error)
	ReadFile(context.Context, string, int64) ([]byte, error)
	WriteFile(context.Context, string, []byte, WriteOptions) error
	MkdirAll(context.Context, string, fs.FileMode) error
}

// FileSystemFor returns the filesystem a step must use for one execution target. A nil
// target means local execution. A target that runs commands but exposes no filesystem is
// an error rather than a silent fallback to the host.
func FileSystemFor(target process.Executor) (FileSystem, error) {
	// Every run carries an executor, defaulting to the local one outside executor scopes,
	// so a host target is recognized rather than assumed from a missing value.
	if target == nil {
		return LocalFileSystem{}, nil
	}
	if _, ok := target.(process.LocalExecutor); ok {
		return LocalFileSystem{}, nil
	}
	if filesystem, ok := target.(FileSystem); ok {
		return filesystem, nil
	}
	return nil, fmt.Errorf("this executor does not expose a filesystem")
}

// IsLocal reports whether a filesystem is the host's own, letting a step keep a host-only
// implementation for work the interface cannot express while still going through the
// interface for every other target.
func IsLocal(filesystem FileSystem) bool {
	_, ok := filesystem.(LocalFileSystem)
	return ok
}

// LocalFileSystem implements FileSystem against the host filesystem. Executors whose
// sessions run commands on the host embed it to declare that fact, so a file step inside
// their scope keeps working instead of being rejected for want of a filesystem.
type LocalFileSystem struct{}

func (LocalFileSystem) Stat(ctx context.Context, path string) (FileInfo, error) {
	if err := ctx.Err(); err != nil {
		return FileInfo{}, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return FileInfo{}, err
	}
	return FileInfo{Size: info.Size(), Mode: info.Mode(), ModTime: info.ModTime()}, nil
}

func (LocalFileSystem) ReadFile(ctx context.Context, path string, maximum int64) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var reader io.Reader = file
	if maximum > 0 {
		limit := maximum
		if maximum < math.MaxInt64 {
			limit++
		}
		reader = io.LimitReader(file, limit)
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return data, nil
}

// WriteFile installs data durably: a temporary file beside the destination is written
// and synced, atomically installed according to Replace, and followed by a directory
// sync, so an interrupted write cannot leave a truncated file where a complete one was.
func (LocalFileSystem) WriteFile(ctx context.Context, path string, data []byte, options WriteOptions) (resultErr error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".wuko-write-*")
	if err != nil {
		return fmt.Errorf("creating temporary file for %s: %w", path, err)
	}
	name := temporary.Name()
	open := true
	defer func() {
		_ = os.Remove(name)
		if open {
			_ = temporary.Close()
		}
	}()
	if err := temporary.Chmod(options.Mode); err != nil {
		return fmt.Errorf("setting temporary file mode: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		return fmt.Errorf("writing temporary file: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("syncing temporary file: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("closing temporary file: %w", err)
	}
	open = false
	if err := ctx.Err(); err != nil {
		return err
	}
	if options.Replace {
		if err := os.Rename(name, path); err != nil {
			return fmt.Errorf("installing %s: %w", path, err)
		}
	} else {
		// Linking a same-directory temporary file installs it only when the destination
		// does not exist, without the Stat/rename race of a preflight existence check.
		if err := os.Link(name, path); err != nil {
			return fmt.Errorf("installing %s: %w", path, err)
		}
		if err := os.Remove(name); err != nil {
			return fmt.Errorf("removing temporary file for %s: %w", path, err)
		}
	}
	handle, err := os.Open(directory)
	if err != nil {
		return fmt.Errorf("opening %s: %w", directory, err)
	}
	defer handle.Close()
	if err := handle.Sync(); err != nil {
		return fmt.Errorf("syncing %s: %w", directory, err)
	}
	return nil
}

// MkdirAll chmods the leaf after creating it, because MkdirAll applies the process umask
// to the mode it is given.
func (LocalFileSystem) MkdirAll(ctx context.Context, path string, mode fs.FileMode) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.MkdirAll(path, mode); err != nil {
		return err
	}
	return os.Chmod(path, mode)
}

var _ FileSystem = LocalFileSystem{}
