package plugin

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

const maxFrameSize = 10 << 20

const (
	// maxConcurrentHostCalls and maxHostCallBytes bound what one plugin can make the host hold
	// while its callbacks run: a goroutine apiece plus a retained copy of the request params,
	// which a single frame may push to just under maxFrameSize. Protocol v2 invites concurrent
	// callbacks, so exceeding the limit is answered with a retryable refusal rather than a
	// teardown.
	maxConcurrentHostCalls = 64
	maxHostCallBytes       = 32 << 20
	// maxRefusedHostCalls bounds the refusals waiting to be written back. A plugin that fills
	// this has ignored every refusal already sent and is no longer speaking the protocol.
	maxRefusedHostCalls = 256
)

type requestFrame struct {
	ID     string `json:"id"`
	Method string `json:"method"`
	Params any    `json:"params,omitempty"`
}
type incomingRequestFrame struct {
	ID     string          `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}
type responseFrame struct {
	ID     string          `json:"id"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *wireError      `json:"error,omitempty"`
}
type outgoingResponseFrame struct {
	ID     string     `json:"id"`
	Result any        `json:"result,omitempty"`
	Error  *wireError `json:"error,omitempty"`
}
type wireError struct {
	Code    string `json:"code,omitempty"`
	Message string `json:"message"`
}
type pendingCall struct {
	response chan responseFrame
	state    *callState
}

// callState owns the request-scoped sinks of one in-flight call. Dispatch and detachment are
// serialized so a canceled or completed caller cannot be reached again: detach blocks behind an
// event already being delivered, which a plain "clear the field" guard cannot do. Detaching also
// releases the closures, which capture the whole step request, instead of holding them for as
// long as the plugin leaves the request unanswered.
type callState struct {
	mu       sync.Mutex
	detached bool
	event    func(eventFrame)
	ctx      context.Context
	host     hostCall
}

func (s *callState) deliver(frame eventFrame) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.detached || s.event == nil {
		return
	}
	s.event(frame)
}

// hostTarget reports the callback of a still-active call. The callback itself runs outside the
// lock: it is handed the caller's context and may block on the plugin, so holding the lock would
// make cancellation wait for the plugin it is trying to cancel.
func (s *callState) hostTarget() (context.Context, hostCall) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.detached {
		return nil, nil
	}
	return s.ctx, s.host
}

func (s *callState) detach() {
	s.mu.Lock()
	s.detached = true
	s.event = nil
	s.host = nil
	s.ctx = nil
	s.mu.Unlock()
}

type hostCall func(context.Context, string, json.RawMessage) (any, error)

type callOptions struct {
	event       func(eventFrame)
	host        hostCall
	drainCancel time.Duration
}
type eventFrame struct {
	ID     string          `json:"id"`
	Event  string          `json:"event"`
	Data   string          `json:"data,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
}

type client struct {
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	writeMu sync.Mutex
	mu      sync.Mutex
	pending map[string]pendingCall
	next    atomic.Uint64
	done    chan struct{}
	readErr error
	// hostMu guards the in-flight host callback budget. refusals carries the ids the read loop
	// turned away, drained by one writer started on first use.
	hostMu     sync.Mutex
	hostCalls  int
	hostBytes  int
	refusals   chan string
	refuseOnce sync.Once
}

func launch(ctx context.Context, path string, stderr io.Writer) (*client, error) {
	cmd := exec.CommandContext(context.WithoutCancel(ctx), path)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("launching plugin: %w", err)
	}
	c := newClient(cmd, stdin)
	go c.read(stdout)
	return c, nil
}

func newClient(cmd *exec.Cmd, stdin io.WriteCloser) *client {
	return &client{cmd: cmd, stdin: stdin, pending: make(map[string]pendingCall), done: make(chan struct{}), refusals: make(chan string, maxRefusedHostCalls)}
}

func (c *client) read(reader io.Reader) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64<<10), maxFrameSize)
	for scanner.Scan() {
		line := scanner.Bytes()
		var header struct {
			ID     string          `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
			Event  string          `json:"event"`
		}
		if json.Unmarshal(line, &header) != nil || header.ID == "" {
			c.fail(fmt.Errorf("malformed plugin frame"))
			return
		}
		if header.Method != "" {
			var request incomingRequestFrame
			if decodeFrame(line, &request) != nil || request.Method == "" {
				c.fail(fmt.Errorf("malformed plugin host request"))
				return
			}
			if c.acquireHostCall(len(request.Params)) {
				go c.handleHostCall(request.ID, request.Method, request.Params)
				continue
			}
			// Over the limit the params are dropped instead of being parked in a goroutine, and
			// the refusal is queued for a separate writer: this loop must never block on stdin,
			// or a plugin that pipelines callbacks before reading answers deadlocks the client.
			if !c.refuseHostCall(request.ID) {
				c.fail(fmt.Errorf("plugin ignored host callback backpressure"))
				return
			}
			continue
		}
		c.mu.Lock()
		pending, ok := c.pending[header.ID]
		if header.Event == "" && ok {
			delete(c.pending, header.ID)
		}
		c.mu.Unlock()
		if !ok {
			c.fail(fmt.Errorf("plugin sent unknown response id %q", header.ID))
			return
		}
		if header.Event != "" {
			var event eventFrame
			if decodeFrame(line, &event) != nil || (event.Event != "stdout" && event.Event != "stderr" && event.Event != "started" && event.Event != "ready") {
				c.fail(fmt.Errorf("malformed plugin event"))
				return
			}
			pending.state.deliver(event)
			continue
		}
		var response responseFrame
		if decodeFrame(line, &response) != nil || (response.Error == nil && response.Result == nil) || (response.Error != nil && response.Result != nil) {
			c.fail(fmt.Errorf("plugin response must contain exactly one of result or error"))
			return
		}
		pending.response <- response
	}
	if err := scanner.Err(); err != nil {
		c.fail(fmt.Errorf("reading plugin protocol: %w", err))
	} else {
		c.fail(io.EOF)
	}
}

func (c *client) acquireHostCall(size int) bool {
	c.hostMu.Lock()
	defer c.hostMu.Unlock()
	// The byte budget never turns away the first callback, so a frame larger than the whole
	// budget still runs on its own rather than being refused forever.
	if c.hostCalls >= maxConcurrentHostCalls || (c.hostCalls > 0 && c.hostBytes+size > maxHostCallBytes) {
		return false
	}
	c.hostCalls++
	c.hostBytes += size
	return true
}

func (c *client) releaseHostCall(size int) {
	c.hostMu.Lock()
	c.hostCalls--
	c.hostBytes -= size
	c.hostMu.Unlock()
}

// refuseHostCall queues a retryable refusal, reporting false once the queue is full. The writer
// goroutine starts on first use so a plugin that stays inside the limits costs nothing.
func (c *client) refuseHostCall(id string) bool {
	select {
	case c.refusals <- id:
	default:
		return false
	}
	c.refuseOnce.Do(func() { go c.writeRefusals() })
	return true
}

func (c *client) writeRefusals() {
	for {
		select {
		case id := <-c.refusals:
			c.writeResponse(outgoingResponseFrame{ID: id, Error: &wireError{Code: "too_many_callbacks", Message: "host callback limit reached, retry once an earlier callback completes"}})
		case <-c.done:
			return
		}
	}
}

func (c *client) handleHostCall(id, method string, params json.RawMessage) {
	defer c.releaseHostCall(len(params))
	var scoped struct {
		ParentID string `json:"parent_id"`
	}
	if json.Unmarshal(params, &scoped) != nil || scoped.ParentID == "" {
		c.writeResponse(outgoingResponseFrame{ID: id, Error: &wireError{Code: "invalid_parent", Message: "host callback requires parent_id"}})
		return
	}
	c.mu.Lock()
	parent, ok := c.pending[scoped.ParentID]
	c.mu.Unlock()
	var parentCtx context.Context
	var host hostCall
	if ok {
		parentCtx, host = parent.state.hostTarget()
	}
	if host == nil {
		c.writeResponse(outgoingResponseFrame{ID: id, Error: &wireError{Code: "unknown_parent", Message: "host callback parent is unknown or completed"}})
		return
	}
	result, err := host(parentCtx, method, params)
	if err != nil {
		c.writeResponse(outgoingResponseFrame{ID: id, Error: &wireError{Code: "host_callback", Message: err.Error()}})
		return
	}
	if result == nil {
		result = struct{}{}
	}
	c.writeResponse(outgoingResponseFrame{ID: id, Result: result})
}

func (c *client) writeResponse(frame outgoingResponseFrame) {
	data, err := json.Marshal(frame)
	if err != nil {
		data, _ = json.Marshal(outgoingResponseFrame{ID: frame.ID, Error: &wireError{Code: "invalid_callback_result", Message: "host callback result is not JSON-compatible"}})
	}
	if len(data) > maxFrameSize {
		data, _ = json.Marshal(outgoingResponseFrame{ID: frame.ID, Error: &wireError{Code: "frame_too_large", Message: "host callback response exceeds 10 MiB"}})
	}
	c.writeMu.Lock()
	_, _ = c.stdin.Write(append(data, '\n'))
	c.writeMu.Unlock()
}

func decodeFrame(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return fmt.Errorf("trailing JSON data")
	}
	return nil
}

func (c *client) fail(err error) {
	c.mu.Lock()
	if c.readErr != nil {
		c.mu.Unlock()
		return
	}
	c.readErr = err
	abandoned := make([]pendingCall, 0, len(c.pending))
	for id, call := range c.pending {
		delete(c.pending, id)
		abandoned = append(abandoned, call)
	}
	close(c.done)
	c.mu.Unlock()
	for _, call := range abandoned {
		// The channel is buffered and every call receives at most one frame, so this never
		// blocks. Detaching afterwards drops the sinks the failed call can no longer use.
		call.response <- responseFrame{Error: &wireError{Message: err.Error()}}
		call.state.detach()
	}
}

func (c *client) call(ctx context.Context, method string, params, result any, event func(eventFrame)) error {
	return c.callWithOptions(ctx, method, params, result, callOptions{event: event})
}

func (c *client) callWithOptions(ctx context.Context, method string, params, result any, options callOptions) error {
	id := strconv.FormatUint(c.next.Add(1), 10)
	response := make(chan responseFrame, 1)
	c.mu.Lock()
	if c.readErr != nil {
		err := c.readErr
		c.mu.Unlock()
		return err
	}
	state := &callState{event: options.event, ctx: ctx, host: options.host}
	c.pending[id] = pendingCall{response: response, state: state}
	c.mu.Unlock()
	discard := func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		state.detach()
	}
	data, err := json.Marshal(requestFrame{ID: id, Method: method, Params: params})
	if err != nil {
		discard()
		return err
	}
	if len(data) > maxFrameSize {
		discard()
		return fmt.Errorf("plugin request exceeds 10 MiB")
	}
	c.writeMu.Lock()
	_, err = c.stdin.Write(append(data, '\n'))
	c.writeMu.Unlock()
	if err != nil {
		discard()
		return fmt.Errorf("writing plugin request: %w", err)
	}
	decodeResponse := func(frame responseFrame) error {
		if frame.Error != nil {
			return errors.New(frame.Error.Message)
		}
		if result != nil {
			decoder := json.NewDecoder(bytes.NewReader(frame.Result))
			decoder.UseNumber()
			if err := decoder.Decode(result); err != nil {
				return fmt.Errorf("decoding plugin response: %w", err)
			}
		}
		return nil
	}
	select {
	case frame := <-response:
		state.detach()
		return decodeResponse(frame)
	case <-ctx.Done():
		// Detach the sinks so a plugin that keeps streaming for this id cannot write into
		// writers the canceled caller no longer owns; detach waits for an event already being
		// delivered, so no callback outlives this return. The map entry stays registered so
		// the eventual response is not mistaken for an unknown id.
		if options.drainCancel <= 0 {
			state.detach()
		}
		c.notify("cancel", map[string]any{"id": id})
		if options.drainCancel <= 0 {
			return ctx.Err()
		}
		timer := time.NewTimer(options.drainCancel)
		defer timer.Stop()
		select {
		case frame := <-response:
			state.detach()
			if err := decodeResponse(frame); err != nil {
				return err
			}
			return ctx.Err()
		case <-timer.C:
			state.detach()
			return errors.Join(ctx.Err(), fmt.Errorf("plugin did not finish canceled request within %s", options.drainCancel))
		case <-c.done:
			state.detach()
			select {
			case frame := <-response:
				return decodeResponse(frame)
			default:
				c.mu.Lock()
				readErr := c.readErr
				c.mu.Unlock()
				return errors.Join(ctx.Err(), readErr)
			}
		}
	}
}

func (c *client) notify(method string, params any) {
	data, _ := json.Marshal(requestFrame{Method: method, Params: params})
	c.writeMu.Lock()
	_, _ = c.stdin.Write(append(data, '\n'))
	c.writeMu.Unlock()
}

func (c *client) close(ctx context.Context) error {
	var result any
	shutdownErr := c.call(ctx, "shutdown", map[string]any{}, &result, nil)
	_ = c.stdin.Close()
	wait := make(chan error, 1)
	go func() { wait <- c.cmd.Wait() }()
	select {
	case err := <-wait:
		return errors.Join(shutdownErr, err)
	case <-ctx.Done():
	}
	_ = c.cmd.Process.Signal(syscall.SIGTERM)
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	select {
	case err := <-wait:
		return errors.Join(shutdownErr, err)
	case <-timer.C:
		_ = c.cmd.Process.Kill()
		return errors.Join(shutdownErr, <-wait)
	}
}
