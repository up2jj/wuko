//go:build darwin || linux

package process

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
	"github.com/muesli/cancelreader"
	"github.com/up2jj/wuko/ptyinteract"
	"golang.org/x/term"
)

// TerminalSize is the character grid of a pseudo-terminal.
type TerminalSize struct {
	Rows uint16
	Cols uint16
}

// TTYSession is the child side of one pseudo-terminal session. LocalExecutor drives a PTY it
// allocated itself; an executor whose PTY lives elsewhere, such as inside a plugin's container,
// implements TTYSession so RunTTYSession applies the same user handoff, interactions, capture,
// and terminal restoration to it.
type TTYSession interface {
	// Read returns the child's terminal output in order, and io.EOF once that output ends.
	io.Reader
	// Write delivers terminal input to the child.
	io.Writer
	// WriteSensitive delivers input whose terminal echo should be suppressed where possible.
	WriteSensitive([]byte) error
	Resize(TerminalSize) error
	// Exited delivers the child's exit status exactly once.
	Exited() <-chan error
	// Terminate stops a child that is still running and returns its exit status. It is called
	// instead of receiving from Exited, never after it.
	Terminate() error
	// CloseOutput ends a Read still blocked after the child exited.
	CloseOutput() error
}

// TTYStartSize validates the stdin of a TTY run and reports the size its child terminal should
// start with: the user's terminal when the run hands off to the user, 80x24 otherwise.
func TTYStartSize(options Options) (TerminalSize, error) {
	_, size, err := ttyTerminal(options)
	return size, err
}

// RunTTYSession drives a TTY run whose child the caller has already started. The result's
// Stdout is the terminal output captured here; a TTY merges stderr into it.
func RunTTYSession(ctx context.Context, session TTYSession, options Options) (Result, error) {
	terminal, _, err := ttyTerminal(options)
	if err != nil {
		return Result{}, err
	}
	return driveTTY(ctx, session, terminal, options)
}

// ttyTerminal returns the host terminal a user handoff reads from, nil when the run never hands
// off, and the size the child's terminal starts with.
func ttyTerminal(options Options) (*os.File, TerminalSize, error) {
	size := TerminalSize{Rows: 24, Cols: 80}
	if options.Interactions != nil && !options.Interact {
		return nil, size, nil
	}
	terminal, ok := options.Stdin.(*os.File)
	if !ok || !term.IsTerminal(int(terminal.Fd())) {
		return nil, size, fmt.Errorf("tty user handoff requires file-backed terminal stdin")
	}
	current, err := pty.GetsizeFull(terminal)
	if err != nil {
		return nil, size, fmt.Errorf("reading terminal size: %w", err)
	}
	return terminal, TerminalSize{Rows: current.Rows, Cols: current.Cols}, nil
}

func runTTY(ctx context.Context, options Options, credential *syscall.Credential) (Result, error) {
	terminal, size, err := ttyTerminal(options)
	if err != nil {
		return Result{}, err
	}
	command := exec.Command(options.Command, options.Args...)
	command.Dir = options.Dir
	command.Env = environment(options.Env)
	command.WaitDelay = terminationGracePeriod + time.Second
	attrs := &syscall.SysProcAttr{Setsid: true, Setctty: true, Credential: credential}
	ptmx, err := pty.StartWithAttrs(command, &pty.Winsize{Rows: size.Rows, Cols: size.Cols}, attrs)
	if err != nil {
		if options.User != "" {
			return Result{}, fmt.Errorf("starting %s as user %q with TTY: %w", options.Command, options.User, err)
		}
		return Result{}, fmt.Errorf("starting %s with TTY: %w", options.Command, err)
	}
	// Setsid makes the child the leader of a new session and process group.
	defer TrackGroup(command.Process.Pid)()
	if options.Started != nil {
		options.Started()
	}
	wait := make(chan error, 1)
	go func() { wait <- command.Wait() }()
	return driveTTY(ctx, &localTTY{ptmx: ptmx, pid: command.Process.Pid, wait: wait, options: options}, terminal, options)
}

// localTTY is a child running on a PTY this process allocated.
type localTTY struct {
	ptmx    *os.File
	pid     int
	wait    chan error
	options Options
}

func (s *localTTY) Read(data []byte) (int, error)  { return s.ptmx.Read(data) }
func (s *localTTY) Write(data []byte) (int, error) { return s.ptmx.Write(data) }
func (s *localTTY) WriteSensitive(data []byte) error {
	return ptyinteract.NewSink(s.ptmx).WriteSensitive(data)
}
func (s *localTTY) Resize(size TerminalSize) error {
	return pty.Setsize(s.ptmx, &pty.Winsize{Rows: size.Rows, Cols: size.Cols})
}
func (s *localTTY) Exited() <-chan error { return s.wait }
func (s *localTTY) Terminate() error     { return terminateProcess(s.pid, s.wait, s.options) }
func (s *localTTY) CloseOutput() error   { return s.ptmx.Close() }

// ttySink lets an interaction plan write into any session.
type ttySink struct{ session TTYSession }

func (sink ttySink) Write(data []byte) (int, error)   { return sink.session.Write(data) }
func (sink ttySink) WriteSensitive(data []byte) error { return sink.session.WriteSensitive(data) }

func driveTTY(ctx context.Context, session TTYSession, terminal *os.File, options Options) (result Result, runErr error) {
	handoff := options.Interactions == nil || options.Interact
	stopResize := startTTYResize(terminal, session)
	defer stopResize()

	stdout := newCaptureBuffer(options.CaptureLimit)
	interactionDone := make(chan struct{})
	var interactionOutput *interactionOutputBuffer
	if options.Interactions != nil {
		interactionOutput = newInteractionOutputBuffer(interactionDone, ptyinteract.MaxUnmatchedBytes)
	}
	outputDone := make(chan error, 1)
	sensitiveOutput := newSensitiveOutputWriter(outputWriter(options.StdoutPolicy, options.Stdout, &stdout))
	go func() {
		var writer io.Writer = sensitiveOutput
		if interactionOutput != nil {
			writer = &interactionOutputWriter{writer: writer, output: interactionOutput, done: interactionDone}
		}
		_, copyErr := io.Copy(writer, session)
		if interactionOutput != nil {
			interactionOutput.Close()
		}
		outputDone <- errors.Join(copyErr, sensitiveOutput.Close())
	}()
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
			_, copyErr := io.Copy(session, input)
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

	var err error
	canceled := false
	waited := false
	var interactionErr error
	if options.Interactions != nil {
		interactionResult := make(chan error, 1)
		go func() {
			defer close(interactionDone)
			stream := interactionOutput.Stream()
			stream.Redact = sensitiveOutput.Redact
			interactionResult <- options.Interactions.Run(ctx, stream, processExited, ttySink{session: session})
		}()
		select {
		case interactionErr = <-interactionResult:
			if interactionErr != nil {
				err = session.Terminate()
				waited = true
				markProcessExited()
			}
		case err = <-session.Exited():
			waited = true
			markProcessExited()
			interactionErr = <-interactionResult
		case <-ctx.Done():
			canceled = true
			err = session.Terminate()
			waited = true
			markProcessExited()
			interactionErr = <-interactionResult
		}
	}
	if interactionErr == nil && handoff && !waited {
		if startErr := startHandoff(); startErr != nil {
			interactionErr = startErr
			err = session.Terminate()
			waited = true
			markProcessExited()
		}
	}
	if !waited {
		select {
		case err = <-session.Exited():
			markProcessExited()
		case <-ctx.Done():
			canceled = true
			err = session.Terminate()
			markProcessExited()
		}
	}
	stopResize()
	outputErr := drainTTYOutput(session, outputDone)

	result = Result{Stdout: stdout.String(), ExitCode: 0, StdoutTruncated: stdout.truncated}
	if !expectedPTYStreamError(outputErr) {
		runErr = errors.Join(runErr, fmt.Errorf("reading terminal output: %w", outputErr))
	}
	if canceled {
		return result, errors.Join(ctx.Err(), runErr)
	}
	if interactionErr != nil {
		if code, ok := exitCode(err); ok {
			result.ExitCode = code
		}
		return result, errors.Join(interactionErr, runErr)
	}
	if err == nil {
		return result, runErr
	}
	if ctx.Err() != nil {
		return result, errors.Join(ctx.Err(), runErr)
	}
	if code, ok := exitCode(err); ok {
		result.ExitCode = code
		return result, errors.Join(&ExitError{Command: options.Command, Code: code, Err: err}, runErr)
	}
	return result, errors.Join(fmt.Errorf("waiting for %s: %w", options.Command, err), runErr)
}

// exitCode reports the status of a child that ran and exited, whether it ran locally or a
// remote session reported its status as an ExitError.
func exitCode(err error) (int, bool) {
	var local *exec.ExitError
	if errors.As(err, &local) {
		return local.ExitCode(), true
	}
	var reported *ExitError
	if errors.As(err, &reported) {
		return reported.Code, true
	}
	return 0, false
}

func startTTYResize(terminal *os.File, session TTYSession) func() {
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
				if size, err := pty.GetsizeFull(terminal); err == nil {
					_ = session.Resize(TerminalSize{Rows: size.Rows, Cols: size.Cols})
				}
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

func drainTTYOutput(session TTYSession, outputDone <-chan error) error {
	timer := time.NewTimer(terminationGracePeriod + time.Second)
	defer timer.Stop()
	select {
	case err := <-outputDone:
		_ = session.CloseOutput()
		return err
	case <-timer.C:
		_ = session.CloseOutput()
		return <-outputDone
	}
}
