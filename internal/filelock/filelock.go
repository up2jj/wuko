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
}

// Acquire creates path if needed and blocks until it holds an exclusive lock on it
// or ctx is done. Callers wrap the returned error with the subject being locked.
//
// The lock is released only by Release: flock is held by the open file description,
// so it survives until this handle is closed. That also means two goroutines in one
// process exclude each other exactly as two processes do, because each Acquire opens
// its own descriptor.
func Acquire(ctx context.Context, path string) (*Handle, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
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
	return &Handle{file: file}, nil
}

// Release unlocks the file and closes its descriptor. Calling it more than once, or
// on a nil handle, is a no-op so callers can release from a defer and an explicit
// close path both.
func (handle *Handle) Release() error {
	if handle == nil || handle.file == nil {
		return nil
	}
	file := handle.file
	handle.file = nil

	// Unlock before closing: closing would drop the lock anyway, but an explicit
	// unlock reports failures the close would swallow.
	var unlockErr error
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_UN); err != nil {
		unlockErr = fmt.Errorf("unlocking %s: %w", file.Name(), err)
	}
	if err := file.Close(); err != nil {
		return errors.Join(unlockErr, fmt.Errorf("closing lock file %s: %w", file.Name(), err))
	}
	return unlockErr
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
