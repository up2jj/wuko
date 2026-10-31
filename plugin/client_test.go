package plugin

import (
	"context"
	"errors"
	"os"
	"syscall"
	"testing"
	"testing/synctest"
	"time"
)

func TestPluginLaunchRetriesOnlyTextFileBusy(t *testing.T) {
	t.Parallel()
	busy := &os.PathError{Op: "fork/exec", Path: "plugin", Err: syscall.ETXTBSY}
	for _, test := range []struct {
		name     string
		failures []error
		wantErr  error
		attempts int
		wait     time.Duration
	}{
		{name: "success", attempts: 1},
		{name: "transient busy", failures: []error{busy}, attempts: 2, wait: 100 * time.Millisecond},
		{name: "success on final attempt", failures: []error{busy, busy, busy}, attempts: 4, wait: 700 * time.Millisecond},
		{name: "persistent busy", failures: []error{busy, busy, busy, busy}, wantErr: syscall.ETXTBSY, attempts: 4, wait: 700 * time.Millisecond},
		{name: "permission denied", failures: []error{syscall.EACCES}, wantErr: syscall.EACCES, attempts: 1},
		{name: "missing executable", failures: []error{os.ErrNotExist}, wantErr: os.ErrNotExist, attempts: 1},
		{name: "busy then permission denied", failures: []error{busy, syscall.EACCES}, wantErr: syscall.EACCES, attempts: 2, wait: 100 * time.Millisecond},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) {
				started := time.Now()
				attempts := 0
				want := &client{}
				got, err := retryPluginLaunch(t.Context(), func() (*client, error) {
					attempts++
					if attempts <= len(test.failures) {
						return nil, test.failures[attempts-1]
					}
					return want, nil
				})
				if !errors.Is(err, test.wantErr) {
					t.Fatalf("launch error = %v, want %v", err, test.wantErr)
				}
				if err == nil && got != want {
					t.Fatal("launch did not return the successful client")
				}
				if attempts != test.attempts || time.Since(started) != test.wait {
					t.Fatalf("attempts=%d, wait=%s, want %d and %s", attempts, time.Since(started), test.attempts, test.wait)
				}
			})
		})
	}
}

func TestPluginLaunchRetryRespectsCancellation(t *testing.T) {
	t.Parallel()
	for _, timeout := range []time.Duration{0, 50 * time.Millisecond} {
		t.Run(timeout.String(), func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(t.Context(), timeout)
				defer cancel()
				started := time.Now()
				attempts := 0
				_, err := retryPluginLaunch(ctx, func() (*client, error) {
					attempts++
					return nil, syscall.ETXTBSY
				})
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("launch error = %v, want deadline exceeded", err)
				}
				wantAttempts := 1
				if timeout == 0 {
					wantAttempts = 0
				}
				if attempts != wantAttempts || time.Since(started) != timeout {
					t.Fatalf("attempts=%d, wait=%s, want %d and %s", attempts, time.Since(started), wantAttempts, timeout)
				}
			})
		})
	}
}
