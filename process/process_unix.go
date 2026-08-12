//go:build darwin || linux

package process

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"slices"
	"strings"
	"syscall"
	"time"
)

type Options struct {
	Command string
	Args    []string
	Dir     string
	Env     map[string]string
	Stdin   io.Reader
	Stdout  io.Writer
	Stderr  io.Writer
	// CaptureLimit bounds each captured output stream. Zero means unlimited. Output written to
	// Stdout and Stderr is unaffected.
	CaptureLimit int64
}

type Result struct {
	Stdout          string
	Stderr          string
	ExitCode        int
	StdoutTruncated bool
	StderrTruncated bool
}

type ExitError struct {
	Command string
	Code    int
	Err     error
}

func (e *ExitError) Error() string { return fmt.Sprintf("%s exited with status %d", e.Command, e.Code) }
func (e *ExitError) Unwrap() error { return e.Err }

func Run(ctx context.Context, options Options) (Result, error) {
	if options.Command == "" {
		return Result{}, fmt.Errorf("command is required")
	}
	command := exec.CommandContext(ctx, options.Command, options.Args...)
	command.Dir = options.Dir
	command.Env = environment(options.Env)
	command.Stdin = options.Stdin
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.WaitDelay = 2 * time.Second
	command.Cancel = func() error {
		if command.Process == nil {
			return nil
		}
		err := syscall.Kill(-command.Process.Pid, syscall.SIGTERM)
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		return err
	}

	stdout := newCaptureBuffer(options.CaptureLimit)
	stderr := newCaptureBuffer(options.CaptureLimit)
	command.Stdout = io.MultiWriter(writerOrDiscard(options.Stdout), &stdout)
	command.Stderr = io.MultiWriter(writerOrDiscard(options.Stderr), &stderr)
	err := command.Run()
	result := Result{Stdout: stdout.String(), Stderr: stderr.String(), ExitCode: 0, StdoutTruncated: stdout.truncated, StderrTruncated: stderr.truncated}
	if err == nil {
		return result, nil
	}
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		result.ExitCode = exitErr.ExitCode()
		return result, &ExitError{Command: options.Command, Code: result.ExitCode, Err: err}
	}
	return result, fmt.Errorf("starting %s: %w", options.Command, err)
}

type captureBuffer struct {
	bytes.Buffer
	limit     int64
	truncated bool
}

func newCaptureBuffer(limit int64) captureBuffer { return captureBuffer{limit: limit} }

func (buffer *captureBuffer) Write(data []byte) (int, error) {
	length := len(data)
	if buffer.limit <= 0 {
		_, _ = buffer.Buffer.Write(data)
		return length, nil
	}
	remaining := buffer.limit - int64(buffer.Buffer.Len())
	if remaining > 0 {
		write := min(int64(len(data)), remaining)
		_, _ = buffer.Buffer.Write(data[:write])
	}
	if int64(length) > remaining {
		buffer.truncated = true
	}
	return length, nil
}

func environment(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		result = append(result, key+"="+values[key])
	}
	return result
}

func writerOrDiscard(writer io.Writer) io.Writer {
	if writer == nil {
		return io.Discard
	}
	return writer
}

func StringInput(value string) io.Reader {
	if value == "" {
		return nil
	}
	return strings.NewReader(value)
}
