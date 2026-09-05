//go:build darwin

package ptyinteract

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

func disableEcho(terminal *os.File) (func() error, error) {
	state, err := unix.IoctlGetTermios(int(terminal.Fd()), unix.TIOCGETA)
	if err != nil {
		return nil, fmt.Errorf("reading PTY terminal attributes: %w", err)
	}
	updated := *state
	updated.Lflag &^= unix.ECHO
	if err := unix.IoctlSetTermios(int(terminal.Fd()), unix.TIOCSETA, &updated); err != nil {
		return nil, fmt.Errorf("disabling PTY echo: %w", err)
	}
	return func() error {
		// A write to a PTY master may return before the slave line discipline has
		// finished processing and echoing its input. Restore only after that output
		// drains, or ECHO can be re-enabled in time to expose the sensitive bytes.
		if err := unix.IoctlSetTermios(int(terminal.Fd()), unix.TIOCSETAW, state); err != nil {
			return fmt.Errorf("restoring PTY echo: %w", err)
		}
		return nil
	}, nil
}
