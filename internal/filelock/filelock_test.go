package filelock

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestAcquireExcludesASecondHolderUntilRelease(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "state.lock")

	first, err := Acquire(t.Context(), path)
	if err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}

	// flock is held by the open file description, so a second Acquire in this same
	// process must contend exactly as another process would.
	second := make(chan error, 1)
	go func() {
		handle, err := Acquire(t.Context(), path)
		if err == nil {
			err = handle.Release()
		}
		second <- err
	}()

	select {
	case err := <-second:
		t.Fatalf("second Acquire() returned %v while the first still held the lock", err)
	case <-time.After(100 * time.Millisecond):
	}

	if err := first.Release(); err != nil {
		t.Fatalf("Release() error = %v", err)
	}
	select {
	case err := <-second:
		if err != nil {
			t.Fatalf("second Acquire() after release: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("second Acquire() never completed after the lock was released")
	}
}

func TestAcquireHonorsContextCancellationWhileContended(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "state.lock")

	holder, err := Acquire(t.Context(), path)
	if err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}
	defer func() { _ = holder.Release() }()

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		_, err := Acquire(ctx, path)
		done <- err
	}()
	time.Sleep(30 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Acquire() error = %v, want context.Canceled", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Acquire() ignored cancellation while contended")
	}
}

func TestReleaseIsIdempotentAndNilSafe(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "state.lock")

	handle, err := Acquire(t.Context(), path)
	if err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}
	if err := handle.Release(); err != nil {
		t.Fatalf("first Release() error = %v", err)
	}
	// Callers release from a defer and an explicit close path both.
	if err := handle.Release(); err != nil {
		t.Errorf("second Release() error = %v, want nil", err)
	}

	var absent *Handle
	if err := absent.Release(); err != nil {
		t.Errorf("nil Release() error = %v, want nil", err)
	}
}

func TestAcquireCreatesTheLockFileWithRestrictivePermissions(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "state.lock")

	handle, err := Acquire(t.Context(), path)
	if err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}
	defer func() { _ = handle.Release() }()

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("lock file mode = %o, want 600", got)
	}
}

func TestAcquireReportsAnUnopenableLockPath(t *testing.T) {
	t.Parallel()
	// A directory component that is a regular file cannot be traversed.
	root := t.TempDir()
	blocker := filepath.Join(root, "file")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Acquire(t.Context(), filepath.Join(blocker, "state.lock")); err == nil {
		t.Fatal("Acquire() error = nil, want a failure opening the lock file")
	}
}

func TestAcquireExistingRefusesToCreateTheLockFile(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := filepath.Join(root, "missing", "state.lock")

	if _, err := AcquireExisting(t.Context(), path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("AcquireExisting() error = %v, want a missing-file error", err)
	}
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Errorf("AcquireExisting() created %s: %v", filepath.Dir(path), err)
	}
}

func TestAcquireExistingLocksAFileThatAlreadyExists(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "state.lock")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	handle, err := AcquireExisting(t.Context(), path)
	if err != nil {
		t.Fatalf("AcquireExisting() error = %v", err)
	}
	if err := handle.Release(); err != nil {
		t.Fatalf("Release() error = %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("Release() left the lock file behind: %v", err)
	}
}

func TestAcquireExistingWaitsOutAHolderAndThenSeesTheDeletedFile(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "state.lock")

	created, err := Acquire(t.Context(), path)
	if err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}

	contended := make(chan error, 1)
	go func() {
		handle, err := AcquireExisting(t.Context(), path)
		if err == nil {
			err = handle.Release()
		}
		contended <- err
	}()
	select {
	case err := <-contended:
		t.Fatalf("AcquireExisting() returned %v while Acquire still held the lock", err)
	case <-time.After(100 * time.Millisecond):
	}

	if err := created.Release(); err != nil {
		t.Fatalf("Release() error = %v", err)
	}
	// The holder deleted the lock file, so the waiter must report the absence rather than
	// keep a lock on a file the path no longer names.
	select {
	case err := <-contended:
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("AcquireExisting() after release = %v, want a missing-file error", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("AcquireExisting() never completed after the lock was released")
	}
}

// Deleting the lock file on release is only safe if no two holders can ever overlap: an
// acquirer that wakes on a file the path no longer names has to start over rather than
// treat it as the lock.
func TestDeletingOnReleaseKeepsHoldersFromOverlapping(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "state.lock")

	const holders = 12
	var held atomic.Bool
	var overlaps atomic.Int64
	var group sync.WaitGroup
	errs := make(chan error, holders)
	for range holders {
		group.Add(1)
		go func() {
			defer group.Done()
			handle, err := Acquire(t.Context(), path)
			if err != nil {
				errs <- err
				return
			}
			if held.Swap(true) {
				overlaps.Add(1)
			}
			time.Sleep(time.Millisecond)
			held.Store(false)
			errs <- handle.Release()
		}()
	}
	group.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := overlaps.Load(); got != 0 {
		t.Fatalf("%d holders overlapped", got)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("the last release left the lock file behind: %v", err)
	}
}
