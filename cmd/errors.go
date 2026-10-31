package cmd

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/up2jj/wuko/tui"
	"github.com/up2jj/wuko/useroutput"
)

type reportedError struct{ error }

// WriteError renders a command error using the richest safe adapter available.
// It returns true when the error was handled.
func WriteError(writer io.Writer, err error) bool {
	var reported reportedError
	if errors.As(err, &reported) {
		return true
	}
	view := useroutput.Inspect(err)
	if len(view.Failures) > 0 {
		for _, failure := range view.Failures {
			if !failure.Reported {
				useroutput.Write(writer, useroutput.Message{Text: failure.Message, Plain: true}, false)
			}
		}
		if view.Ordinary != nil {
			cwd, _ := os.Getwd()
			if !tui.WriteValidationColor(writer, view.Ordinary, cwd, colorEnabled(writer)) {
				fmt.Fprintln(writer, "wuko:", view.Ordinary)
			}
		}
		return true
	}
	cwd, _ := os.Getwd()
	return tui.WriteValidationColor(writer, err, cwd, colorEnabled(writer))
}
