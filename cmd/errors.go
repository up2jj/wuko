package cmd

import (
	"errors"
	"io"
	"os"

	"github.com/up2jj/wuko/tui"
)

type reportedError struct{ error }

// WriteError renders a command error using the richest safe adapter available.
// It returns true when the error was handled.
func WriteError(writer io.Writer, err error) bool {
	var reported reportedError
	if errors.As(err, &reported) {
		return true
	}
	cwd, _ := os.Getwd()
	return tui.WriteValidation(writer, err, cwd)
}
