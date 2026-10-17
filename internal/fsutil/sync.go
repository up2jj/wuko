package fsutil

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// SyncDirectory flushes a directory's entries so a rename into it survives a crash. Several
// network filesystems cannot do this and say so; that is not a durability failure, and treating
// it as one would fail a step whose change is already in place, which a retry would then repeat.
func SyncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("opening directory %s: %w", path, err)
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil && !DirectorySyncUnsupported(err) {
		return fmt.Errorf("syncing directory %s: %w", path, err)
	}
	return nil
}

// DirectorySyncUnsupported reports whether a directory sync failed only because the filesystem
// does not support it.
func DirectorySyncUnsupported(err error) bool {
	return errors.Is(err, syscall.EINVAL) || errors.Is(err, syscall.ENOTSUP) || errors.Is(err, syscall.ENOSYS)
}
