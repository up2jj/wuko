// Package filelock provides the exclusive advisory file lock that guards wuko's
// on-disk state against concurrent writers.
package filelock

import (
	"context"
	"errors"
	"fmt"
	"os"
	"syscall"
	"time"
)

// retryInterval is how long Acquire waits between attempts. flock offers no
// cancellable blocking mode, so contention is polled rather than waited on.
const retryInterval = 10 * time.Millisecond

// Handle owns an exclusive advisory lock and the descriptor holding it.
type Handle struct {
	file *os.File
	path string
}

// Acquire creates path if needed and blocks until it holds an exclusive lock on it
// or ctx is done. Callers wrap the returned error with the subject being locked. Pass a
// path that exists only to carry the lock: Release deletes it, so it must never be the
// resource being guarded.
//
// The lock is released only by Release: flock is held by the open file description,
// so it survives until this handle is closed. That also means two goroutines in one
// process exclude each other exactly as two processes do, because each Acquire opens
// its own descriptor.
func Acquire(ctx context.Context, path string) (*Handle, error) {
	return acquire(ctx, path, os.O_CREATE|os.O_RDWR)
}

// AcquireExisting locks path only when it already exists and otherwise returns an error
// matching fs.ErrNotExist. Read-only callers use it so observing state that was never
// written creates neither the lock file nor the directory holding it. Because Release
// deletes the lock file, a resource with no live holder usually has none.
func AcquireExisting(ctx context.Context, path string) (*Handle, error) {
	return acquire(ctx, path, os.O_RDWR)
}

func acquire(ctx context.Context, path string, flags int) (*Handle, error) {
	for {
		file, err := os.OpenFile(path, flags, 0o600)
		if err != nil {
			return nil, fmt.Errorf("opening lock file %s: %w", path, err)
		}
		if err := file.Chmod(0o600); err != nil {
			_ = file.Close()
			return nil, fmt.Errorf("setting permissions on lock file %s: %w", path, err)
		}
		if err := wait(ctx, file); err != nil {
			_ = file.Close()
			return nil, err
		}
		named, err := locksNamedFile(file, path)
		if err != nil {
			_ = file.Close()
			return nil, err
		}
		if named {
			return &Handle{file: file, path: path}, nil
		}
		// The holder this attempt waited on deleted the lock file as it released, so this
		// descriptor now locks a file the path no longer names and excludes nobody.
		// Dropping it and contending for whatever the name refers to now is what keeps
		// deletion safe: every acquirer either holds the file at the path or starts over.
		if err := file.Close(); err != nil {
			return nil, fmt.Errorf("closing lock file %s: %w", path, err)
		}
	}
}

// locksNamedFile reports whether the locked descriptor is still the file path names.
func locksNamedFile(file *os.File, path string) (bool, error) {
	locked, err := file.Stat()
	if err != nil {
		return false, fmt.Errorf("inspecting lock file %s: %w", path, err)
	}
	named, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("inspecting lock file %s: %w", path, err)
	}
	return os.SameFile(locked, named), nil
}

// Release deletes the lock file, unlocks it, and closes its descriptor. Calling it more
// than once, or on a nil handle, is a no-op so callers can release from a defer and an
// explicit close path both.
func (handle *Handle) Release() error {
	if handle == nil || handle.file == nil {
		return nil
	}
	file := handle.file
	handle.file = nil

	// Delete while the lock is still held, so no acquirer can observe the file at this
	// path without holding the lock on it. One already blocked on this descriptor's file
	// wakes to find the path no longer names it and starts over; a later one creates the
	// file again. Deleting after unlocking would instead let two holders believe they had
	// the lock, one on the deleted file and one on its replacement.
	var removeErr error
	if err := os.Remove(handle.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		removeErr = fmt.Errorf("removing lock file %s: %w", handle.path, err)
	}

	// Unlock before closing: closing would drop the lock anyway, but an explicit
	// unlock reports failures the close would swallow.
	var unlockErr error
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_UN); err != nil {
		unlockErr = fmt.Errorf("unlocking %s: %w", file.Name(), err)
	}
	if err := file.Close(); err != nil {
		return errors.Join(removeErr, unlockErr, fmt.Errorf("closing lock file %s: %w", file.Name(), err))
	}
	return errors.Join(removeErr, unlockErr)
}

func wait(ctx context.Context, file *os.File) error {
	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("locking %s: %w", file.Name(), err)
		}
		err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			return fmt.Errorf("locking %s: %w", file.Name(), err)
		}
		timer := time.NewTimer(retryInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("locking %s: %w", file.Name(), ctx.Err())
		case <-timer.C:
		}
	}
}
