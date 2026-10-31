// Package useroutput defines user-facing messages and deliberate workflow failures.
package useroutput

import (
	"errors"
	"fmt"
	"io"
	"strings"
)

// Level controls a message's symbol and terminal color, not workflow success.
type Level string

const (
	Info    Level = "info"
	Success Level = "success"
	Warn    Level = "warn"
	Error   Level = "error"
)

// Valid reports whether a level is supported.
func (level Level) Valid() bool {
	return level == Info || level == Success || level == Warn || level == Error
}

// Message is explicit workflow output. Plain messages have no symbol or color.
type Message struct {
	Text  string
	Level Level
	Plain bool
}

// Write renders a message as complete lines, without interpreting its contents.
func Write(writer io.Writer, message Message, color bool) {
	if writer == nil {
		return
	}
	marker, code := "•", "36"
	switch message.Level {
	case Success:
		marker, code = "✓", "32"
	case Warn:
		marker, code = "!", "33"
	case Error:
		marker, code = "✗", "31"
	}
	if color {
		marker = "\x1b[" + code + "m" + marker + "\x1b[0m"
	}
	var output strings.Builder
	for _, line := range strings.Split(strings.TrimSuffix(message.Text, "\n"), "\n") {
		if message.Plain {
			fmt.Fprintln(&output, line)
		} else {
			fmt.Fprintf(&output, "%s %s\n", marker, line)
		}
	}
	io.WriteString(writer, output.String())
}

// Failure retains diagnostic context while requesting a plain terminal message.
type Failure struct {
	Message  string
	Code     int
	Reported bool
}

func (failure *Failure) Error() string { return "assertion failed: " + failure.Message }

// ErrorView separates deliberate failures from ordinary errors in a joined error tree.
// Redaction adapters implement UserOutputErrors to supply a safe projection without
// exposing the original, unredacted causes.
type ErrorView struct {
	Failures []Failure
	Ordinary error
}

// Inspect preserves ordinary errors and unwraps only when deliberate failures are present.
func Inspect(err error) ErrorView {
	if err == nil {
		return ErrorView{}
	}
	if safe, ok := err.(interface{ UserOutputErrors() ErrorView }); ok {
		return safe.UserOutputErrors()
	}
	if failure, ok := err.(*Failure); ok {
		return ErrorView{Failures: []Failure{*failure}}
	}
	var children []error
	switch wrapped := err.(type) {
	case interface{ Unwrap() []error }:
		children = wrapped.Unwrap()
	case interface{ Unwrap() error }:
		children = []error{wrapped.Unwrap()}
	default:
		return ErrorView{Ordinary: err}
	}
	view := ErrorView{}
	var ordinary []error
	for _, child := range children {
		part := Inspect(child)
		view.Failures = append(view.Failures, part.Failures...)
		ordinary = append(ordinary, part.Ordinary)
	}
	if len(view.Failures) == 0 {
		return ErrorView{Ordinary: err}
	}
	view.Ordinary = errors.Join(ordinary...)
	return view
}

// OnlyPlain reports whether an error contains exclusively deliberate failures.
func OnlyPlain(err error) bool {
	view := Inspect(err)
	return len(view.Failures) > 0 && view.Ordinary == nil
}
