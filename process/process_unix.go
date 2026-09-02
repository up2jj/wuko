//go:build darwin || linux

package process

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"os/user"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
	"github.com/muesli/cancelreader"
	"github.com/up2jj/wuko/ptyinteract"
	"golang.org/x/term"
)

const terminationGracePeriod = 2 * time.Second

type Options struct {
	Command string
	Args    []string
	Dir     string
	Env     map[string]string
	// User is a username or numeric user ID for the child process. Empty inherits the current user.
	User  string
	Stdin io.Reader
	// StdinOutlivesProcess reports that Stdin stays open past the child's own lifetime, so the
	// exit must be reported without waiting for a stdin pump to reach EOF. This executor
	// satisfies it by requiring an *os.File, which os/exec passes to the child as a duplicated
	// descriptor instead of copying in a goroutine that Cmd.Wait would join forever.
	StdinOutlivesProcess bool
	Stdout               io.Writer
	Stderr               io.Writer
	// TTY runs the command in a pseudo-terminal. User handoff requires file-backed terminal Stdin.
	TTY bool
	// Interactions scripts ordered writes and prompt responses before optional user handoff.
	Interactions *ptyinteract.Plan
	// Interact hands the PTY to Stdin after Interactions complete. TTY without Interactions
	// preserves legacy behavior and always hands off immediately.
	Interact bool
	// Terminal customizes the outer terminal while the user controls the child PTY.
	Terminal *TerminalAppearance
	// CaptureLimit bounds each captured output stream. Zero means unlimited. Output written to
	// Stdout and Stderr is unaffected.
	CaptureLimit int64
	StdoutPolicy OutputPolicy
	StderrPolicy OutputPolicy
	// Started is called exactly once after the command has been started successfully.
	Started func()
	// TerminationSignal is sent on cancellation before SIGKILL escalation. Zero means SIGTERM.
	TerminationSignal syscall.Signal
	// TerminationParentOnly signals only the direct child instead of its process group.
	TerminationParentOnly bool
	// TerminationGracePeriod bounds the wait before SIGKILL. Zero uses the package default.
	TerminationGracePeriod time.Duration
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

// Executor runs commands in one execution environment.
type Executor interface {
	Run(context.Context, Options) (Result, error)
}

// CancelPolicy lets an executor declare that canceling Run does not stop the process it
// started. A Docker exec has no kill API, so its command keeps running inside the container
// after Run returns. Executors that do not implement this are assumed to stop on cancel.
type CancelPolicy interface {
	CancelStopsProcess() bool
}

// LocalExecutor runs commands as child processes on the current host.
type LocalExecutor struct{}

// Run executes a command with the local executor.
func Run(ctx context.Context, options Options) (Result, error) {
	return LocalExecutor{}.Run(ctx, options)
}

// Run executes a command as a local child process.
func (LocalExecutor) Run(ctx context.Context, options Options) (Result, error) {
	if options.Command == "" {
		return Result{}, fmt.Errorf("command is required")
	}
	if !options.StdoutPolicy.Valid() || !options.StderrPolicy.Valid() {
		return Result{}, fmt.Errorf("invalid output policy")
	}
	if options.Interactions != nil && !options.TTY {
		return Result{}, fmt.Errorf("PTY interactions require TTY")
	}
	if options.Interact && options.Interactions == nil {
		return Result{}, fmt.Errorf("PTY user handoff requires interactions")
	}
	if _, isFile := options.Stdin.(*os.File); options.StdinOutlivesProcess && !isFile {
		return Result{}, fmt.Errorf("stdin that outlives %s must be an *os.File", options.Command)
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	credential, credentialErr := credentialForUser(options.User)
	if credentialErr != nil {
		return Result{}, credentialErr
	}
	if options.TTY {
		return runTTY(ctx, options, credential)
	}
	command := exec.Command(options.Command, options.Args...)
	command.Dir = options.Dir
	command.Env = environment(options.Env)
	command.Stdin = options.Stdin
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Credential: credential}
	command.WaitDelay = terminationGracePeriod + time.Second

	stdout := newCaptureBuffer(options.CaptureLimit)
	stderr := newCaptureBuffer(options.CaptureLimit)
	command.Stdout = outputWriter(options.StdoutPolicy, options.Stdout, &stdout)
	command.Stderr = outputWriter(options.StderrPolicy, options.Stderr, &stderr)
	if err := command.Start(); err != nil {
		if options.User != "" {
			return Result{}, fmt.Errorf("starting %s as user %q: %w", options.Command, options.User, err)
		}
		return Result{}, fmt.Errorf("starting %s: %w", options.Command, err)
	}
	if options.Started != nil {
		options.Started()
	}
	wait := make(chan error, 1)
	go func() { wait <- command.Wait() }()

	var err error
	canceled := false
	select {
	case err = <-wait:
	case <-ctx.Done():
		canceled = true
		err = terminateProcess(command.Process.Pid, wait, options)
	}
	result := Result{Stdout: stdout.String(), Stderr: stderr.String(), ExitCode: 0, StdoutTruncated: stdout.truncated, StderrTruncated: stderr.truncated}
	if canceled {
		return result, ctx.Err()
	}
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

func runTTY(ctx context.Context, options Options, credential *syscall.Credential) (result Result, runErr error) {
	handoff := options.Interactions == nil || options.Interact
	var terminal *os.File
	size := &pty.Winsize{Rows: 24, Cols: 80}
	if handoff {
		var ok bool
		terminal, ok = options.Stdin.(*os.File)
		if !ok || !term.IsTerminal(int(terminal.Fd())) {
			return Result{}, fmt.Errorf("tty user handoff requires file-backed terminal stdin")
		}
		var err error
		size, err = pty.GetsizeFull(terminal)
		if err != nil {
			return Result{}, fmt.Errorf("reading terminal size: %w", err)
		}
	}

	command := exec.Command(options.Command, options.Args...)
	command.Dir = options.Dir
	command.Env = environment(options.Env)
	command.WaitDelay = terminationGracePeriod + time.Second
	attrs := &syscall.SysProcAttr{Setsid: true, Setctty: true, Credential: credential}
	ptmx, err := pty.StartWithAttrs(command, size, attrs)
	if err != nil {
		if options.User != "" {
			return Result{}, fmt.Errorf("starting %s as user %q with TTY: %w", options.Command, options.User, err)
		}
		return Result{}, fmt.Errorf("starting %s with TTY: %w", options.Command, err)
	}
	if options.Started != nil {
		options.Started()
	}

	stopResize := startTTYResize(terminal, ptmx)
	defer stopResize()

	stdout := newCaptureBuffer(options.CaptureLimit)
	interactionDone := make(chan struct{})
	var interactionOutput *interactionOutputBuffer
	if options.Interactions != nil {
		interactionOutput = newInteractionOutputBuffer(interactionDone, ptyinteract.MaxUnmatchedBytes)
	}
	outputDone := make(chan error, 1)
	go func() {
		writer := outputWriter(options.StdoutPolicy, options.Stdout, &stdout)
		if interactionOutput != nil {
			writer = &interactionOutputWriter{writer: writer, output: interactionOutput, done: interactionDone}
		}
		_, copyErr := io.Copy(writer, ptmx)
		if interactionOutput != nil {
			interactionOutput.Close()
		}
		outputDone <- copyErr
	}()
	wait := make(chan error, 1)
	go func() { wait <- command.Wait() }()
	processExited := make(chan struct{})
	var processExitedOnce sync.Once
	markProcessExited := func() { processExitedOnce.Do(func() { close(processExited) }) }

	var input cancelreader.CancelReader
	var inputDone chan error
	var terminalState *term.State
	var restoreAppearance func()
	startHandoff := func() error {
		var startErr error
		input, startErr = cancelreader.NewReader(terminal)
		if startErr != nil {
			return fmt.Errorf("preparing terminal input: %w", startErr)
		}
		terminalState, startErr = term.MakeRaw(int(terminal.Fd()))
		if startErr != nil {
			_ = input.Close()
			input = nil
			return fmt.Errorf("enabling raw terminal mode: %w", startErr)
		}
		restoreAppearance = applyTerminalAppearance(options.Stdout, options.Terminal)
		inputDone = make(chan error, 1)
		go func() {
			_, copyErr := io.Copy(ptmx, input)
			inputDone <- copyErr
		}()
		return nil
	}
	stopHandoff := func() error {
		var stopErr error
		if input != nil {
			input.Cancel()
			inputErr := <-inputDone
			if !expectedPTYStreamError(inputErr) {
				stopErr = errors.Join(stopErr, fmt.Errorf("writing terminal input: %w", inputErr))
			}
			if closeErr := input.Close(); closeErr != nil {
				stopErr = errors.Join(stopErr, fmt.Errorf("closing terminal input: %w", closeErr))
			}
		}
		if terminalState != nil {
			if restoreErr := term.Restore(int(terminal.Fd()), terminalState); restoreErr != nil {
				stopErr = errors.Join(stopErr, fmt.Errorf("restoring terminal mode: %w", restoreErr))
			}
		}
		if restoreAppearance != nil {
			restoreAppearance()
			restoreAppearance = nil
		}
		return stopErr
	}
	defer func() { runErr = errors.Join(runErr, stopHandoff()) }()

	canceled := false
	waited := false
	var interactionErr error
	if options.Interactions != nil {
		interactionResult := make(chan error, 1)
		go func() {
			defer close(interactionDone)
			interactionResult <- options.Interactions.Run(ctx, interactionOutput.Stream(), processExited, ptyinteract.NewSink(ptmx))
		}()
		select {
		case interactionErr = <-interactionResult:
			if interactionErr != nil {
				err = terminateProcess(command.Process.Pid, wait, options)
				waited = true
				markProcessExited()
			}
		case err = <-wait:
			waited = true
			markProcessExited()
			interactionErr = <-interactionResult
		case <-ctx.Done():
			canceled = true
			err = terminateProcess(command.Process.Pid, wait, options)
			waited = true
			markProcessExited()
			interactionErr = <-interactionResult
		}
	}
	if interactionErr == nil && handoff && !waited {
		if startErr := startHandoff(); startErr != nil {
			interactionErr = startErr
			err = terminateProcess(command.Process.Pid, wait, options)
			waited = true
			markProcessExited()
		}
	}
	if !waited {
		select {
		case err = <-wait:
			markProcessExited()
		case <-ctx.Done():
			canceled = true
			err = terminateProcess(command.Process.Pid, wait, options)
			markProcessExited()
		}
	}
	stopResize()
	outputErr := drainPTYOutput(ptmx, outputDone)

	result = Result{Stdout: stdout.String(), ExitCode: 0, StdoutTruncated: stdout.truncated}
	if !expectedPTYStreamError(outputErr) {
		runErr = errors.Join(runErr, fmt.Errorf("reading terminal output: %w", outputErr))
	}
	if canceled {
		return result, errors.Join(ctx.Err(), runErr)
	}
	if interactionErr != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			result.ExitCode = exitErr.ExitCode()
		}
		return result, errors.Join(interactionErr, runErr)
	}
	if err == nil {
		return result, runErr
	}
	if ctx.Err() != nil {
		return result, errors.Join(ctx.Err(), runErr)
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		result.ExitCode = exitErr.ExitCode()
		return result, errors.Join(&ExitError{Command: options.Command, Code: result.ExitCode, Err: err}, runErr)
	}
	return result, errors.Join(fmt.Errorf("waiting for %s: %w", options.Command, err), runErr)
}

func startTTYResize(terminal, ptmx *os.File) func() {
	if terminal == nil {
		return func() {}
	}
	resizeSignals := make(chan os.Signal, 1)
	resizeStop := make(chan struct{})
	resizeDone := make(chan struct{})
	var resizeOnce sync.Once
	signal.Notify(resizeSignals, syscall.SIGWINCH)
	go func() {
		defer close(resizeDone)
		for {
			select {
			case <-resizeSignals:
				_ = pty.InheritSize(terminal, ptmx)
			case <-resizeStop:
				return
			}
		}
	}()
	return func() {
		resizeOnce.Do(func() {
			signal.Stop(resizeSignals)
			close(resizeStop)
			<-resizeDone
		})
	}
}

type interactionOutputWriter struct {
	writer io.Writer
	output *interactionOutputBuffer
	done   <-chan struct{}
}

func (writer *interactionOutputWriter) Write(data []byte) (int, error) {
	written, err := writer.writer.Write(data)
	if written == 0 {
		return written, err
	}
	select {
	case <-writer.done:
	default:
		writer.output.Add(bytes.Clone(data[:written]))
	}
	return written, err
}

type interactionOutputBuffer struct {
	input    chan []byte
	output   chan []byte
	overflow chan struct{}
	stop     <-chan struct{}
	limit    int
}

func newInteractionOutputBuffer(stop <-chan struct{}, limit int) *interactionOutputBuffer {
	buffer := &interactionOutputBuffer{
		input: make(chan []byte), output: make(chan []byte), overflow: make(chan struct{}),
		stop: stop, limit: limit,
	}
	go buffer.run()
	return buffer
}

func (buffer *interactionOutputBuffer) Add(data []byte) {
	if len(data) == 0 {
		return
	}
	select {
	case buffer.input <- data:
	case <-buffer.stop:
	}
}

func (buffer *interactionOutputBuffer) Close() { close(buffer.input) }

func (buffer *interactionOutputBuffer) Stream() ptyinteract.Stream {
	return ptyinteract.Stream{Output: buffer.output, Overflow: buffer.overflow}
}

func (buffer *interactionOutputBuffer) run() {
	defer close(buffer.output)
	var queued [][]byte
	queuedBytes := 0
	input := buffer.input
	for input != nil || len(queued) > 0 {
		if len(queued) == 0 {
			select {
			case data, ok := <-input:
				if !ok {
					input = nil
					continue
				}
				if len(data) > buffer.limit {
					close(buffer.overflow)
					<-buffer.stop
					return
				}
				queued = append(queued, data)
				queuedBytes += len(data)
			case <-buffer.stop:
				return
			}
			continue
		}
		select {
		case data, ok := <-input:
			if !ok {
				input = nil
			} else {
				if len(data) > buffer.limit-queuedBytes {
					close(buffer.overflow)
					<-buffer.stop
					return
				}
				queued = append(queued, data)
				queuedBytes += len(data)
			}
		case buffer.output <- queued[0]:
			queuedBytes -= len(queued[0])
			queued[0] = nil
			queued = queued[1:]
		case <-buffer.stop:
			return
		}
	}
}

func drainPTYOutput(ptmx *os.File, outputDone <-chan error) error {
	timer := time.NewTimer(terminationGracePeriod + time.Second)
	defer timer.Stop()
	select {
	case err := <-outputDone:
		_ = ptmx.Close()
		return err
	case <-timer.C:
		_ = ptmx.Close()
		return <-outputDone
	}
}

func expectedPTYStreamError(err error) bool {
	return err == nil || errors.Is(err, cancelreader.ErrCanceled) || errors.Is(err, os.ErrClosed) || errors.Is(err, syscall.EIO)
}

func credentialForUser(identity string) (*syscall.Credential, error) {
	if identity == "" {
		return nil, nil
	}
	account, err := lookupUser(identity)
	if err != nil {
		return nil, fmt.Errorf("looking up user %q: %w", identity, err)
	}
	uid, err := parseUserID("user", account.Uid)
	if err != nil {
		return nil, fmt.Errorf("resolving user %q: %w", identity, err)
	}
	gid, err := parseUserID("primary group", account.Gid)
	if err != nil {
		return nil, fmt.Errorf("resolving user %q: %w", identity, err)
	}
	if uid == uint32(os.Geteuid()) && gid == uint32(os.Getegid()) {
		return nil, nil
	}

	groupIDs, err := account.GroupIds()
	if err != nil {
		return nil, fmt.Errorf("looking up groups for user %q: %w", identity, err)
	}
	groups := make([]uint32, len(groupIDs))
	for i, groupID := range groupIDs {
		groups[i], err = parseUserID("supplementary group", groupID)
		if err != nil {
			return nil, fmt.Errorf("resolving user %q: %w", identity, err)
		}
	}
	return &syscall.Credential{Uid: uid, Gid: gid, Groups: groups}, nil
}

func lookupUser(identity string) (*user.User, error) {
	if numericID(identity) {
		return user.LookupId(identity)
	}
	return user.Lookup(identity)
}

func numericID(value string) bool {
	if value == "" {
		return false
	}
	if value[0] == '-' {
		value = value[1:]
	}
	if value == "" {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func parseUserID(kind, value string) (uint32, error) {
	if strings.HasPrefix(value, "-") {
		parsed, err := strconv.ParseInt(value, 10, 32)
		if err != nil {
			return 0, fmt.Errorf("invalid %s ID %q: %w", kind, value, err)
		}
		return uint32(int32(parsed)), nil
	}
	parsed, err := strconv.ParseUint(value, 10, 32)
	if err != nil {
		return 0, fmt.Errorf("invalid %s ID %q: %w", kind, value, err)
	}
	return uint32(parsed), nil
}

func terminateProcess(processID int, wait <-chan error, options Options) error {
	signal := options.TerminationSignal
	if signal == 0 {
		signal = syscall.SIGTERM
	}
	gracePeriod := options.TerminationGracePeriod
	if gracePeriod == 0 {
		gracePeriod = terminationGracePeriod
	}
	termErr := signalProcess(processID, signal, options.TerminationParentOnly)
	timer := time.NewTimer(gracePeriod)
	defer timer.Stop()

	var waitErr error
	waited := false
	select {
	case waitErr = <-wait:
		waited = true
		if !processGroupAlive(processID) {
			return errors.Join(waitErr, termErr)
		}
		<-timer.C
	case <-timer.C:
	}

	killErr := signalProcess(processID, syscall.SIGKILL, options.TerminationParentOnly)
	if !waited {
		waitErr = <-wait
	}
	return errors.Join(waitErr, termErr, killErr)
}

func signalProcess(processID int, signal syscall.Signal, parentOnly bool) error {
	if parentOnly {
		err := syscall.Kill(processID, signal)
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		return err
	}
	return signalProcessGroup(processID, signal)
}

func signalProcessGroup(processID int, signal syscall.Signal) error {
	err := syscall.Kill(-processID, signal)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}

func processGroupAlive(processID int) bool {
	err := syscall.Kill(-processID, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

type captureBuffer struct {
	buffer    bytes.Buffer
	limit     int64
	truncated bool
}

func newCaptureBuffer(limit int64) captureBuffer { return captureBuffer{limit: limit} }

func (buffer *captureBuffer) Write(data []byte) (int, error) {
	length := len(data)
	if buffer.limit <= 0 {
		_, _ = buffer.buffer.Write(data)
		return length, nil
	}
	remaining := buffer.limit - int64(buffer.buffer.Len())
	if remaining > 0 {
		write := min(int64(len(data)), remaining)
		_, _ = buffer.buffer.Write(data[:write])
	}
	if int64(length) > remaining {
		buffer.truncated = true
	}
	return length, nil
}

func (buffer *captureBuffer) String() string { return buffer.buffer.String() }

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

func outputWriter(policy OutputPolicy, stream io.Writer, capture io.Writer) io.Writer {
	switch {
	case policy.Streams() && policy.Captures():
		return io.MultiWriter(writerOrDiscard(stream), capture)
	case policy.Streams():
		return writerOrDiscard(stream)
	case policy.Captures():
		return capture
	default:
		return io.Discard
	}
}

func StringInput(value string) io.Reader {
	if value == "" {
		return nil
	}
	return strings.NewReader(value)
}
