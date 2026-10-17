package plugin

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/up2jj/wuko/process"
)

// inputChunkSize bounds the bytes one host.input.read answer carries.
const inputChunkSize = 32 << 10

// ttyCancelDrain is how long a canceled TTY run waits for the plugin to report the exit of the
// command it was asked to stop.
const ttyCancelDrain = 10 * time.Second

func (s *pluginSession) Run(ctx context.Context, o process.Options) (process.Result, error) {
	if !o.StdoutPolicy.Valid() || !o.StderrPolicy.Valid() {
		return process.Result{}, fmt.Errorf("invalid output policy")
	}
	if (o.Interactions != nil || o.Interact) && !o.TTY {
		return process.Result{}, fmt.Errorf("PTY interactions require TTY")
	}
	if o.TTY {
		if !s.declaration.TTY {
			return process.Result{}, fmt.Errorf("plugin executor %q does not support TTY", s.plugin.namespace)
		}
		return s.runTTY(ctx, o)
	}
	if o.StdinOutlivesProcess && !s.declaration.Stdin {
		return process.Result{}, fmt.Errorf("plugin executor %q does not support streaming stdin", s.plugin.namespace)
	}
	params := s.runParams(o)
	options := callOptions{}
	var input *hostInput
	var pumped chan struct{}
	switch {
	case o.Stdin != nil && s.declaration.Stdin:
		params["stdin_stream"] = true
		params["stdin_outlives_process"] = o.StdinOutlivesProcess
		input = newHostInput()
		pumped = make(chan struct{})
		go func() {
			defer close(pumped)
			input.pump(o.Stdin)
		}()
		options.host = input.callbacks()
	case o.Stdin != nil:
		stdin, err := io.ReadAll(o.Stdin)
		if err != nil {
			return process.Result{}, err
		}
		params["stdin"] = base64.StdEncoding.EncodeToString(stdin)
	}
	stdout, stderr := o.Stdout, o.Stderr
	if !o.StdoutPolicy.Streams() {
		stdout = nil
	}
	if !o.StderrPolicy.Streams() {
		stderr = nil
	}
	options.event = streamEvents(stdout, stderr, o.Started)
	var wireResult executorRunResult
	err := s.plugin.client.callWithOptions(ctx, "executor.run", params, &wireResult, options)
	if input != nil {
		input.finish()
		// As with a local child, stdin is not read after Run returns unless it outlives the
		// command, in which case only its owner can end the read that is still pending.
		if !o.StdinOutlivesProcess {
			<-pumped
		}
	}
	result := wireResult.processResult()
	if !o.StdoutPolicy.Captures() {
		result.Stdout = ""
	}
	if !o.StderrPolicy.Captures() {
		result.Stderr = ""
	}
	if o.CaptureLimit > 0 {
		if int64(len(result.Stdout)) > o.CaptureLimit {
			result.Stdout = result.Stdout[:o.CaptureLimit]
			result.StdoutTruncated = true
		}
		if int64(len(result.Stderr)) > o.CaptureLimit {
			result.Stderr = result.Stderr[:o.CaptureLimit]
			result.StderrTruncated = true
		}
	}
	if err == nil && result.ExitCode != 0 {
		err = &process.ExitError{Command: o.Command, Code: result.ExitCode}
	}
	return result, err
}

func (s *pluginSession) runParams(o process.Options) map[string]any {
	return map[string]any{"session": s.id, "command": o.Command, "args": o.Args, "dir": o.Dir, "env": o.Env, "capture_limit": o.CaptureLimit, "stdout_policy": o.StdoutPolicy, "stderr_policy": o.StderrPolicy}
}

// runTTY starts the command in the plugin's terminal and drives it with the same handoff,
// interactions, and capture a local PTY gets. Output arrives as stdout events, input leaves
// through host.input.read, and size changes are sent as resize notifications.
func (s *pluginSession) runTTY(ctx context.Context, o process.Options) (process.Result, error) {
	size, err := process.TTYStartSize(o)
	if err != nil {
		return process.Result{}, err
	}
	params := s.runParams(o)
	params["tty"] = map[string]any{"rows": size.Rows, "cols": size.Cols}
	// The host captures and applies the output policy to the terminal stream itself, so the
	// plugin streams all of it.
	params["stdout_policy"], params["stderr_policy"], params["capture_limit"] = process.OutputInherit, process.OutputInherit, 0
	callCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	session := &remoteTTY{client: s.plugin.client, id: s.plugin.client.nextID(), input: newHostInput(), output: newOutputQueue(), exited: make(chan error, 1), cancel: cancel}
	options := callOptions{id: session.id, host: session.input.callbacks(), drainCancel: ttyCancelDrain}
	options.event = func(frame eventFrame) error {
		switch frame.Event {
		case "stdout", "stderr":
			// A terminal has one output stream.
			session.output.write(frame.decoded)
		case "started":
			if o.Started != nil {
				o.Started()
			}
		}
		return nil
	}
	go func() {
		var wireResult executorRunResult
		err := s.plugin.client.callWithOptions(callCtx, "executor.run", params, &wireResult, options)
		if err == nil && wireResult.ExitCode != 0 {
			err = &process.ExitError{Command: o.Command, Code: wireResult.ExitCode}
		}
		// Input pending for a command that has exited fails instead of blocking, which the
		// handoff's input copy needs before it can be stopped.
		session.input.finish()
		session.output.close(io.EOF)
		session.exited <- err
	}()
	return process.RunTTYSession(ctx, session, o)
}

// remoteTTY is the host's view of a terminal a plugin executor allocated.
type remoteTTY struct {
	client *client
	id     string
	input  *hostInput
	output *outputQueue
	exited chan error
	cancel context.CancelFunc
}

func (t *remoteTTY) Read(data []byte) (int, error)  { return t.output.Read(data) }
func (t *remoteTTY) Write(data []byte) (int, error) { return t.input.write(data, false) }
func (t *remoteTTY) WriteSensitive(data []byte) error {
	_, err := t.input.write(data, true)
	return err
}
func (t *remoteTTY) Resize(size process.TerminalSize) error {
	t.client.notify("resize", map[string]any{"id": t.id, "rows": size.Rows, "cols": size.Cols})
	return nil
}
func (t *remoteTTY) Exited() <-chan error { return t.exited }
func (t *remoteTTY) Terminate() error {
	t.cancel()
	return <-t.exited
}
func (t *remoteTTY) CloseOutput() error {
	t.output.close(os.ErrClosed)
	return nil
}

// hostInput answers a call's host.input.read callbacks. Chunks come from a pump over the
// command's stdin or from writes to a terminal; each read takes one, so the plugin consuming
// input is what paces its producer.
type hostInput struct {
	chunks   chan inputChunk
	ended    chan struct{}
	endOnce  sync.Once
	done     chan struct{}
	doneOnce sync.Once
}

type inputChunk struct {
	data      []byte
	sensitive bool
}

func newHostInput() *hostInput {
	return &hostInput{chunks: make(chan inputChunk), ended: make(chan struct{}), done: make(chan struct{})}
}

// pump forwards reader until it ends or the call finishes.
func (input *hostInput) pump(reader io.Reader) {
	defer input.end()
	buffer := make([]byte, inputChunkSize)
	for {
		count, err := reader.Read(buffer)
		if count > 0 {
			if _, writeErr := input.write(buffer[:count], false); writeErr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

// write hands one chunk to the next read, failing with os.ErrClosed once the call finished.
func (input *hostInput) write(data []byte, sensitive bool) (int, error) {
	if len(data) == 0 {
		return 0, nil
	}
	select {
	case input.chunks <- inputChunk{data: append([]byte(nil), data...), sensitive: sensitive}:
		return len(data), nil
	case <-input.done:
		return 0, os.ErrClosed
	}
}

// end reports the end of input; reads that find no chunk then answer eof.
func (input *hostInput) end() { input.endOnce.Do(func() { close(input.ended) }) }

// finish releases producers once the call has returned and nothing will read again.
func (input *hostInput) finish() { input.doneOnce.Do(func() { close(input.done) }) }

func (input *hostInput) read(ctx context.Context) (any, error) {
	select {
	case chunk := <-input.chunks:
		return map[string]any{"data": base64.StdEncoding.EncodeToString(chunk.data), "sensitive": chunk.sensitive}, nil
	default:
	}
	select {
	case chunk := <-input.chunks:
		return map[string]any{"data": base64.StdEncoding.EncodeToString(chunk.data), "sensitive": chunk.sensitive}, nil
	case <-input.ended:
		return map[string]any{"eof": true}, nil
	case <-input.done:
		return map[string]any{"eof": true}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// callbacks answers the host.input.read callbacks of the call it is attached to.
func (input *hostInput) callbacks() hostCall {
	return func(ctx context.Context, method string, raw json.RawMessage) (any, error) {
		if method != hostInputRead {
			return nil, fmt.Errorf("unsupported host callback %q", method)
		}
		return input.read(ctx)
	}
}

// hostInputRead is the v4 callback a plugin reads streamed stdin and terminal input with.
const hostInputRead = "host.input.read"

// outputQueue buffers terminal output between the protocol reader, which must never block on a
// slow consumer, and the session's Read.
type outputQueue struct {
	mu     sync.Mutex
	data   []byte
	err    error
	signal chan struct{}
}

func newOutputQueue() *outputQueue { return &outputQueue{signal: make(chan struct{}, 1)} }

func (queue *outputQueue) write(data []byte) {
	queue.mu.Lock()
	if queue.err == nil {
		queue.data = append(queue.data, data...)
	}
	queue.mu.Unlock()
	queue.notify()
}

// close ends the stream with err once the buffered output has been read; os.ErrClosed discards
// it instead.
func (queue *outputQueue) close(err error) {
	queue.mu.Lock()
	if queue.err == nil || errors.Is(err, os.ErrClosed) {
		queue.err = err
	}
	if errors.Is(err, os.ErrClosed) {
		queue.data = nil
	}
	queue.mu.Unlock()
	queue.notify()
}

func (queue *outputQueue) notify() {
	select {
	case queue.signal <- struct{}{}:
	default:
	}
}

func (queue *outputQueue) Read(data []byte) (int, error) {
	for {
		queue.mu.Lock()
		if len(queue.data) > 0 {
			count := copy(data, queue.data)
			queue.data = queue.data[count:]
			queue.mu.Unlock()
			return count, nil
		}
		err := queue.err
		queue.mu.Unlock()
		if err != nil {
			return 0, err
		}
		<-queue.signal
	}
}
