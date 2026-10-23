// Package polltest waits for conditions that tests cannot observe through a channel, such as a
// process exiting or a file appearing. A test whose waiting involves only goroutines and timers
// should use testing/synctest instead, which needs neither polling nor real time.
package polltest

import (
	"testing"
	"time"
)

// interval is how often a condition is checked.
const interval = 10 * time.Millisecond

// Wait checks condition until it holds or timeout elapses, and reports whether it held.
func Wait(timeout time.Duration, condition func() bool) bool {
	deadline := time.Now().Add(timeout)
	for !condition() {
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(interval)
	}
	return true
}

// Until waits like Wait and fails the test when condition does not hold within timeout. What
// names the awaited state in the failure message.
func Until(t testing.TB, timeout time.Duration, what string, condition func() bool) {
	t.Helper()
	if !Wait(timeout, condition) {
		t.Fatalf("timed out after %s waiting for %s", timeout, what)
	}
}
