// Package fakecli helps a test binary stand in for an external command.
package fakecli

import "syscall"

// Exit ends a test binary that is acting as a fake command. os.Exit would run the race
// detector's exit hook, which pauses for a second (GORACE atexit_sleep_ms) so that other
// goroutines can finish reporting; a fake that is invoked many times per test pays that for
// every call. A fake runs no goroutines worth waiting for, and its output is unbuffered.
func Exit(code int) {
	syscall.Exit(code)
}
