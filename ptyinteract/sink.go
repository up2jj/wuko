//go:build darwin || linux

package ptyinteract

import "os"

type ptySink struct{ terminal *os.File }

// NewSink returns a Sink that writes to a PTY master and suppresses line echo for sensitive data.
func NewSink(terminal *os.File) Sink { return &ptySink{terminal: terminal} }

func (sink *ptySink) Write(data []byte) (int, error) { return sink.terminal.Write(data) }

func (sink *ptySink) WriteSensitive(data []byte) (err error) {
	restore, err := disableEcho(sink.terminal)
	if err != nil {
		return err
	}
	defer func() { err = joinRestore(err, restore()) }()
	return writeAll(sink.terminal, data)
}
