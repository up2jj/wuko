package ptyinteract

import (
	"bytes"
	"context"
	"errors"
	"io"
	"time"
)

// Sink writes injected bytes to a PTY and can suppress terminal echo for sensitive writes.
type Sink interface {
	Write([]byte) (int, error)
	WriteSensitive([]byte) error
}

// Stream carries ordered PTY output and reports when its buffering limit is exceeded.
type Stream struct {
	Output   <-chan []byte
	Overflow <-chan struct{}
	// Redact registers sensitive input before it is written. Implementations that expose PTY
	// output should remove registered values because terminal echo suppression is inherently
	// racy on platforms whose line discipline consumes master writes asynchronously.
	Redact func([]byte)
}

// Run executes the plan. Stream.Output must contain the single ordered stream read from the PTY.
// Exited is closed when the child process exits; output should also be closed when its PTY stream
// ends. A successful return means every send has completed.
func (p *Plan) Run(ctx context.Context, stream Stream, exited <-chan struct{}, sink Sink) error {
	if p == nil || len(p.interactions) == 0 {
		return nil
	}
	pending := make([]byte, 0)
	for index, item := range p.interactions {
		if item.pattern != nil {
			remaining, err := waitForMatch(ctx, stream, exited, pending, index+1, item)
			if err != nil {
				return err
			}
			pending = remaining
		}
		if err := write(ctx, stream.Redact, sink, item.send, item.redact, item.sensitive); err != nil {
			return &Error{Index: index + 1, Expect: item.expect, Kind: FailureWrite, Err: err}
		}
	}
	return nil
}

func waitForMatch(ctx context.Context, stream Stream, exited <-chan struct{}, pending []byte, index int, item interaction) ([]byte, error) {
	timer := time.NewTimer(item.timeout)
	defer timer.Stop()
	for {
		if location := item.pattern.FindIndex(pending); location != nil {
			if location[0] > MaxUnmatchedBytes {
				return nil, &Error{Index: index, Expect: item.expect, Kind: FailureOverflow}
			}
			return pending[location[1]:], nil
		}
		if len(pending) > MaxUnmatchedBytes {
			return nil, &Error{Index: index, Expect: item.expect, Kind: FailureOverflow}
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timer.C:
			return nil, &Error{Index: index, Expect: item.expect, Kind: FailureTimeout}
		case <-stream.Overflow:
			return nil, &Error{Index: index, Expect: item.expect, Kind: FailureOverflow}
		case <-exited:
			return nil, &Error{Index: index, Expect: item.expect, Kind: FailureExited}
		case data, ok := <-stream.Output:
			if !ok {
				return nil, &Error{Index: index, Expect: item.expect, Kind: FailureExited}
			}
			pending = append(pending, data...)
		}
	}
}

func write(ctx context.Context, redact func([]byte), sink Sink, data, sensitiveValue []byte, sensitive bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if sensitive {
		if redact != nil && len(sensitiveValue) > 0 {
			redact(bytes.Clone(sensitiveValue))
		}
		return sink.WriteSensitive(data)
	}
	for len(data) > 0 {
		written, err := sink.Write(data)
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
		data = data[written:]
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	return nil
}

func writeAll(writer io.Writer, data []byte) error {
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

func joinRestore(writeErr, restoreErr error) error {
	return errors.Join(writeErr, restoreErr)
}
