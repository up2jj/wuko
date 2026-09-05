//go:build darwin || linux

package process

import (
	"bytes"
	"io"
	"os"
	"slices"
	"sync"
)

// sensitiveOutputWriter is a second line of defense for sensitive PTY input. Disabling ECHO
// around a master write is not sufficient on Linux: the write can return before the slave line
// discipline consumes the bytes, allowing echo restoration to overtake them. Values are
// registered before the write and removed from both captured and streamed output if they appear.
type sensitiveOutputWriter struct {
	mu       sync.Mutex
	writer   io.Writer
	values   [][]byte
	pending  []byte
	closed   bool
	writeErr error
}

func newSensitiveOutputWriter(writer io.Writer) *sensitiveOutputWriter {
	return &sensitiveOutputWriter{writer: writer}
}

func (writer *sensitiveOutputWriter) Redact(value []byte) {
	if len(value) == 0 {
		return
	}
	writer.mu.Lock()
	defer writer.mu.Unlock()
	writer.values = append(writer.values, bytes.Clone(value))
	slices.SortFunc(writer.values, func(left, right []byte) int { return len(right) - len(left) })
}

func (writer *sensitiveOutputWriter) Write(data []byte) (int, error) {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	if writer.closed {
		return 0, os.ErrClosed
	}
	if writer.writeErr != nil {
		return 0, writer.writeErr
	}
	if len(writer.values) == 0 {
		return writer.writer.Write(data)
	}

	writer.pending = append(writer.pending, data...)
	for _, value := range writer.values {
		writer.pending = bytes.ReplaceAll(writer.pending, value, nil)
	}
	hold := writer.sensitivePrefixSuffix()
	visible := writer.pending[:len(writer.pending)-hold]
	if err := writeFull(writer.writer, visible); err != nil {
		writer.writeErr = err
		return 0, err
	}
	writer.pending = append(writer.pending[:0], writer.pending[len(writer.pending)-hold:]...)
	return len(data), nil
}

func (writer *sensitiveOutputWriter) Close() error {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	if writer.closed {
		return writer.writeErr
	}
	writer.closed = true
	// pending is a prefix of a sensitive value. Withhold it at EOF: exposing a partial secret is
	// worse than dropping an ambiguous output suffix.
	writer.pending = nil
	return writer.writeErr
}

func (writer *sensitiveOutputWriter) sensitivePrefixSuffix() int {
	hold := 0
	for _, value := range writer.values {
		limit := min(len(writer.pending), len(value)-1)
		for length := limit; length > hold; length-- {
			if bytes.Equal(writer.pending[len(writer.pending)-length:], value[:length]) {
				hold = length
				break
			}
		}
	}
	return hold
}

func writeFull(writer io.Writer, data []byte) error {
	for len(data) > 0 {
		written, err := writer.Write(data)
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
		data = data[written:]
	}
	return nil
}
