package filelock

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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
