package control

import (
	"fmt"
	"runtime/debug"
)

// RecoveredPanic converts a recovered value into an error, or nil when there was no panic. Call
// it as RecoveredPanic(recover()) directly inside a deferred function: recover reports a panic
// only to the function the runtime defers, so a helper that called recover itself would always
// see nil. Any code that runs work in a goroutine of its own needs it -- a scheduler can only
// contain the goroutines it started.
func RecoveredPanic(recovered any) error {
	if recovered == nil {
		return nil
	}
	return &panicError{value: recovered, stack: debug.Stack()}
}

// panicError carries a recovered panic. The stack is kept because a panic reaching a goroutine
// boundary is a bug in the code being run, and the trace is the only thing that says where.
// It deliberately does not unwrap to context.Canceled, so a panic is classified as a failure
// rather than mistaken for a cancellation.
type panicError struct {
	value any
	stack []byte
}

func (failure *panicError) Error() string {
	return fmt.Sprintf("panic: %v\n\n%s", failure.value, failure.stack)
}
