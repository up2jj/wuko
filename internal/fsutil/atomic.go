// Package fsutil holds the filesystem primitives shared by wuko's on-disk state.
package fsutil

import (
	"fmt"
	"os"
	"path/filepath"
)

// WriteFileAtomic replaces path with data durably. The data is written and synced to a
// temporary sibling with mode perm, renamed over the destination and followed by a directory
// sync, so a crash leaves either the old file or the complete new one, never a truncated one.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) (resultErr error) {
	directory := filepath.Dir(path)
	file, err := os.CreateTemp(directory, "."+filepath.Base(path)+"-*")
	if err != nil {
		return fmt.Errorf("creating temporary file for %s: %w", path, err)
	}
	temporary := file.Name()
	renamed := false
	defer func() {
		_ = file.Close()
		if !renamed {
			_ = os.Remove(temporary)
		}
	}()
	if err := file.Chmod(perm); err != nil {
		return fmt.Errorf("setting permissions on %s: %w", path, err)
	}
	if _, err := file.Write(data); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("syncing %s: %w", path, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("closing %s: %w", path, err)
	}
	if err := os.Rename(temporary, path); err != nil {
		return fmt.Errorf("replacing %s: %w", path, err)
	}
	renamed = true
	dir, err := os.Open(directory)
	if err != nil {
		return fmt.Errorf("opening directory of %s: %w", path, err)
	}
	defer dir.Close()
	if err := dir.Sync(); err != nil {
		return fmt.Errorf("syncing directory of %s: %w", path, err)
	}
	return nil
}
