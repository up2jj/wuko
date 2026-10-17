package fsutil

import (
	"fmt"
	"syscall"
	"testing"
)

func TestDirectorySyncUnsupportedRecognizesOnlyUnsupportedErrors(t *testing.T) {
	for _, unsupported := range []error{syscall.EINVAL, syscall.ENOTSUP, syscall.ENOSYS} {
		if !DirectorySyncUnsupported(fmt.Errorf("syncing directory: %w", unsupported)) {
			t.Errorf("%v was not recognized as unsupported", unsupported)
		}
	}
	if DirectorySyncUnsupported(syscall.EIO) {
		t.Error("EIO was treated as unsupported")
	}
}

func TestSyncDirectorySucceedsOnALocalDirectory(t *testing.T) {
	if err := SyncDirectory(t.TempDir()); err != nil {
		t.Fatal(err)
	}
}
