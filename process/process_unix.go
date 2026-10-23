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
	"os/user"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/muesli/cancelreader"
	"github.com/up2jj/wuko/ptyinteract"
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
	// PromptStdin and PromptStderr are host-side streams for an executor that must obtain
	// authorization before starting the child. LocalExecutor ignores them.
	PromptStdin  io.Reader
	PromptStderr io.Writer
	Interactive  bool
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
	// RemoteTerminal, with TTY, bridges the child's PTY to a terminal on the far side of a
	// connection instead of the user's: Stdin carries its input, Stdout receives the PTY's
	// output, and sizes come from RemoteTerminal. Handoff and interactions are the far side's
	// to perform, so they cannot be combined with it.
	RemoteTerminal *RemoteTerminal
	// CaptureLimit bounds each captured output stream. Zero means unlimited. Output written to
	// Stdout and Stderr is unaffected.
	CaptureLimit int64
	StdoutPolicy OutputPolicy
	StderrPolicy OutputPolicy
	// Started is called exactly once after the command has been started successfully.
	Started func()
	// StartedPID reports the local child PID after start, before waiting for it. It is
	// intended for host-side supervision; remote executors need not call it.
	StartedPID func(int)
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
	if options.RemoteTerminal != nil && (!options.TTY || options.Interactions != nil) {
		return Result{}, fmt.Errorf("a remote terminal requires TTY without interactions")
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
	stdoutPipe, err := pipeOutput(&command.Stdout, outputWriter(options.StdoutPolicy, options.Stdout, &stdout))
	if err != nil {
		return Result{}, fmt.Errorf("creating stdout pipe for %s: %w", options.Command, err)
	}
	stderrPipe, err := pipeOutput(&command.Stderr, outputWriter(options.StderrPolicy, options.Stderr, &stderr))
	if err != nil {
		stdoutPipe.abandon()
		return Result{}, fmt.Errorf("creating stderr pipe for %s: %w", options.Command, err)
	}
	if err := command.Start(); err != nil {
		stdoutPipe.abandon()
		stderrPipe.abandon()
		if options.User != "" {
			return Result{}, fmt.Errorf("starting %s as user %q: %w", options.Command, options.User, err)
		}
		return Result{}, fmt.Errorf("starting %s: %w", options.Command, err)
	}
	stdoutPipe.started()
	stderrPipe.started()
	defer TrackGroup(command.Process.Pid)()
	if options.StartedPID != nil {
		options.StartedPID(command.Process.Pid)
	}
	if options.Started != nil {
		options.Started()
	}
	wait := make(chan error, 1)
	go func() { wait <- command.Wait() }()

	canceled := false
	select {
	case err = <-wait:
	case <-ctx.Done():
		canceled = true
		err = terminateProcess(command.Process.Pid, wait, options)
	}
	// The command has exited, but a child it backgrounded may still run in its process group
	// and hold the output pipes open. A step owns everything it started, so the leftovers are
	// stopped before the output is drained; otherwise they would outlive the step and their
	// pipes would stall it.
	stopLeftoverGroup(command.Process.Pid, options)
	stdoutPipe.drain()
	stderrPipe.drain()
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

// outputPipe copies a child's output stream to a writer through a pipe the caller owns. Handing
// os/exec an *os.File keeps Cmd.Wait from joining the copy, so Wait reports the command's exit
// as soon as it happens even while a backgrounded child still holds the pipe.
type outputPipe struct {
	reader *os.File
	writer *os.File
	done   chan struct{}
}

// pipeOutput attaches writer to target. Writers os/exec can pass to the child directly, files
// and the discard sink, need no pipe and yield a nil outputPipe, whose methods are no-ops.
func pipeOutput(target *io.Writer, writer io.Writer) (*outputPipe, error) {
	if _, isFile := writer.(*os.File); isFile || writer == io.Discard || writer == nil {
		if writer == io.Discard {
			writer = nil
		}
		*target = writer
		return nil, nil
	}
	reader, pipeWriter, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	pipe := &outputPipe{reader: reader, writer: pipeWriter, done: make(chan struct{})}
	go func() {
		defer close(pipe.done)
		if _, err := io.Copy(writer, reader); err != nil {
			// Keep reading after the writer fails; a child whose pipe fills up would block
			// in write and never exit.
			_, _ = io.Copy(io.Discard, reader)
		}
	}()
	*target = pipeWriter
	return pipe, nil
}

// started releases the parent's copy of the write end, so the copy sees EOF once every
// process holding it has exited.
func (pipe *outputPipe) started() {
	if pipe != nil {
		_ = pipe.writer.Close()
	}
}

// abandon releases both ends when the command never started.
func (pipe *outputPipe) abandon() {
	if pipe != nil {
		_ = pipe.writer.Close()
		_ = pipe.reader.Close()
		<-pipe.done
	}
}

// drain waits for the copy to finish. A process that left the group, and so survived
// stopLeftoverGroup, can keep the pipe open indefinitely; after the grace period the read
// end is closed so the step is not held hostage by it.
func (pipe *outputPipe) drain() {
	if pipe == nil {
		return
	}
	timer := time.NewTimer(terminationGracePeriod)
	defer timer.Stop()
	select {
	case <-pipe.done:
	case <-timer.C:
		_ = pipe.reader.Close()
		<-pipe.done
	}
	_ = pipe.reader.Close()
}

// stopLeftoverGroup terminates what remains of a process group after its leader exited, with
// the configured signal and SIGKILL escalation after the grace period.
func stopLeftoverGroup(processID int, options Options) {
	if !processGroupAlive(processID) {
		return
	}
	terminationSignal := options.TerminationSignal
	if terminationSignal == 0 {
		terminationSignal = syscall.SIGTERM
	}
	gracePeriod := options.TerminationGracePeriod
	if gracePeriod == 0 {
		gracePeriod = terminationGracePeriod
	}
	_ = signalProcessGroup(processID, terminationSignal)
	deadline := time.Now().Add(gracePeriod)
	for processGroupAlive(processID) {
		if time.Now().After(deadline) {
			_ = signalProcessGroup(processID, syscall.SIGKILL)
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
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
