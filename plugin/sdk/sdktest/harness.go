package sdktest

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/up2jj/wuko/plugin/sdk"
)

const (
	defaultTimeout   = 10 * time.Second
	maxProtocolFrame = 10 << 20
)

// Harness is an in-memory host for one SDK plugin.
type Harness struct {
	t         testing.TB
	timeout   time.Duration
	callbacks Callbacks

	ctx    context.Context
	cancel context.CancelFunc
	input  *io.PipeWriter
	output *io.PipeReader

	writeMu sync.Mutex
	mu      sync.Mutex
	calls   map[string]*Call
	failure error
	closing bool
	closed  bool
	next    atomic.Uint64

	// Callbacks are counted rather than tracked in a WaitGroup: the protocol reader
	// keeps starting handlers while close waits for them, and a WaitGroup.Add racing a
	// returning Wait panics and takes the whole test binary with it.
	callbacksActive int
	callbackIdle    chan struct{}

	initialization Initialization
	serveDone      chan error
	readDone       chan struct{}
	closeDone      chan struct{}
	closeOnce      sync.Once
	reportOnce     sync.Once
	closeErr       error
}

// Call represents one in-flight step request.
type Call struct {
	harness *Harness
	id      string
	ctx     context.Context
	cancel  context.CancelFunc

	mu         sync.Mutex
	changed    chan struct{}
	events     []Event
	response   *Response
	failure    error
	cancelSent bool
}

// Start connects plugin to an in-memory JSONL host and performs initialization.
func Start(t testing.TB, plugin *sdk.Plugin, configured ...Option) *Harness {
	t.Helper()
	if plugin == nil {
		t.Fatal("sdktest: plugin is nil")
	}
	settings := options{timeout: defaultTimeout}
	for _, option := range configured {
		if option == nil {
			t.Fatal("sdktest: nil option")
		}
		if err := option(&settings); err != nil {
			t.Fatalf("sdktest: configure harness: %v", err)
		}
	}

	pluginInput, hostInput := io.Pipe()
	hostOutput, pluginOutput := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	harness := &Harness{
		t: t, timeout: settings.timeout, callbacks: settings.callbacks,
		ctx: ctx, cancel: cancel, input: hostInput, output: hostOutput, calls: make(map[string]*Call),
		serveDone: make(chan error, 1), readDone: make(chan struct{}), closeDone: make(chan struct{}),
	}
	t.Cleanup(harness.Close)
	go func() {
		err := plugin.Serve(ctx, pluginInput, pluginOutput)
		_ = pluginInput.Close()
		_ = pluginOutput.Close()
		harness.serveDone <- err
	}()
	go harness.read()

	call := harness.request("initialize", map[string]any{"protocol": sdk.Protocol})
	response := call.Await()
	if response.Error != nil {
		t.Fatalf("sdktest: initialize: %v", response.Error)
	}
	if err := response.DecodeResult(&harness.initialization); err != nil {
		t.Fatalf("sdktest: decode initialize result: %v", err)
	}
	return harness
}

// Initialization returns the declarations received during Start.
func (harness *Harness) Initialization() Initialization {
	return harness.initialization
}

// CallStep sends one framed step request.
func (harness *Harness) CallStep(request StepRequest) *Call {
	harness.t.Helper()
	if request.Type == "" {
		harness.t.Fatal("sdktest: step type is required")
	}
	method := string(request.Operation)
	if request.Operation == "" {
		method = string(StepRun)
	}
	switch StepOperation(method) {
	case StepRun, StepValidate, StepCleanup, StepService:
	default:
		harness.t.Fatalf("sdktest: unsupported step operation %q", request.Operation)
	}
	with := request.With
	if with == nil {
		with = map[string]any{}
	}
	params := map[string]any{"type": request.Type, "with": with, "context": request.Context}
	if method == string(StepCleanup) {
		params["result"] = request.Result
	}
	return harness.request(method, params)
}

// Close gracefully shuts down the plugin. It is safe to call more than once.
func (harness *Harness) Close() {
	harness.t.Helper()
	harness.closeOnce.Do(func() {
		harness.closeErr = harness.close()
		close(harness.closeDone)
	})
	<-harness.closeDone
	harness.reportOnce.Do(func() {
		if harness.closeErr != nil {
			harness.t.Errorf("sdktest: close: %v", harness.closeErr)
		}
	})
}

func (harness *Harness) close() error {
	harness.mu.Lock()
	if harness.closed {
		harness.mu.Unlock()
		return nil
	}
	harness.closing = true
	failure := harness.failure
	active := make([]*Call, 0, len(harness.calls))
	for _, call := range harness.calls {
		active = append(active, call)
	}
	var shutdown *Call
	if failure == nil {
		shutdown = harness.newCallLocked()
	}
	harness.mu.Unlock()

	for _, call := range active {
		call.cancelContext()
	}

	var closeErr error
	if shutdown != nil {
		if err := harness.write(map[string]any{"id": shutdown.id, "method": "shutdown", "params": map[string]any{}}); err != nil {
			closeErr = errors.Join(closeErr, fmt.Errorf("sending shutdown: %w", err))
			harness.cancel()
		} else {
			response, err := shutdown.waitResponse(harness.timeout)
			if err != nil {
				closeErr = errors.Join(closeErr, fmt.Errorf("awaiting shutdown: %w", err))
				harness.cancel()
			} else if response.Error != nil {
				closeErr = errors.Join(closeErr, fmt.Errorf("shutdown: %w", response.Error))
			}
		}
	}

	if err := harness.waitCallbacks(time.Now().Add(harness.timeout)); err != nil {
		closeErr = errors.Join(closeErr, err)
		harness.cancel()
	}

	if err := harness.input.Close(); err != nil && !errors.Is(err, io.ErrClosedPipe) {
		closeErr = errors.Join(closeErr, fmt.Errorf("closing plugin input: %w", err))
	}
	select {
	case err := <-harness.serveDone:
		if err != nil && !errors.Is(err, context.Canceled) {
			closeErr = errors.Join(closeErr, fmt.Errorf("serving plugin: %w", err))
		}
	case <-time.After(harness.timeout):
		closeErr = errors.Join(closeErr, fmt.Errorf("waiting for plugin: timeout after %s", harness.timeout))
		harness.cancel()
	}
	select {
	case <-harness.readDone:
	case <-time.After(harness.timeout):
		closeErr = errors.Join(closeErr, fmt.Errorf("waiting for protocol reader: timeout after %s", harness.timeout))
		harness.cancel()
	}

	harness.cancel()
	harness.mu.Lock()
	harness.closed = true
	if harness.failure != nil {
		closeErr = errors.Join(closeErr, harness.failure)
	}
	harness.mu.Unlock()
	return closeErr
}

// ID returns the generated request ID.
func (call *Call) ID() string { return call.id }

// Await waits for the terminal response. A transport failure or timeout fails
// the test; a plugin error is returned in Response.Error.
func (call *Call) Await() Response {
	call.harness.t.Helper()
	response, err := call.waitResponse(call.harness.timeout)
	if err != nil {
		call.harness.fail(err)
		call.harness.t.Fatalf("sdktest: await call %s: %v", call.id, err)
	}
	return response
}

// AwaitEvent waits for a named event while retaining other queued events.
func (call *Call) AwaitEvent(name string) Event {
	call.harness.t.Helper()
	if name == "" {
		call.harness.t.Fatal("sdktest: event name is required")
	}
	deadline := time.Now().Add(call.harness.timeout)
	for {
		call.mu.Lock()
		for index, event := range call.events {
			if event.Name == name {
				call.events = append(call.events[:index], call.events[index+1:]...)
				call.mu.Unlock()
				return event
			}
		}
		if call.failure != nil {
			err := call.failure
			call.mu.Unlock()
			call.harness.t.Fatalf("sdktest: await %s event for call %s: %v", name, call.id, err)
		}
		if call.response != nil {
			response := *call.response
			call.mu.Unlock()
			if response.Error != nil {
				call.harness.t.Fatalf("sdktest: call %s failed before %s event: %v", call.id, name, response.Error)
			}
			call.harness.t.Fatalf("sdktest: call %s completed before %s event", call.id, name)
		}
		changed := call.changed
		call.mu.Unlock()
		if !waitUntil(changed, deadline) {
			err := fmt.Errorf("timed out after %s waiting for %s event on call %s", call.harness.timeout, name, call.id)
			call.harness.fail(err)
			call.harness.t.Fatal("sdktest: " + err.Error())
		}
	}
}

// Cancel sends a cancellation notification for the call. Repeated cancellation
// and cancellation after completion are no-ops.
func (call *Call) Cancel() {
	call.harness.t.Helper()
	// Close has already canceled every active call and closed the plugin's input, so a
	// notification sent now would only fail the test on a closed pipe.
	call.harness.mu.Lock()
	closing := call.harness.closing
	call.harness.mu.Unlock()
	call.mu.Lock()
	if closing || call.cancelSent || call.response != nil || call.failure != nil {
		call.mu.Unlock()
		return
	}
	call.cancelSent = true
	call.cancel()
	call.mu.Unlock()
	if err := call.harness.write(map[string]any{"method": "cancel", "params": map[string]any{"id": call.id}}); err != nil {
		call.harness.fail(fmt.Errorf("canceling call %s: %w", call.id, err))
		call.harness.t.Fatalf("sdktest: cancel call %s: %v", call.id, err)
	}
}

func (harness *Harness) request(method string, params any) *Call {
	harness.t.Helper()
	harness.mu.Lock()
	if harness.closing {
		harness.mu.Unlock()
		harness.t.Fatal("sdktest: harness is closing")
	}
	if harness.failure != nil {
		err := harness.failure
		harness.mu.Unlock()
		harness.t.Fatalf("sdktest: harness failed: %v", err)
	}
	call := harness.newCallLocked()
	harness.mu.Unlock()
	if err := harness.write(map[string]any{"id": call.id, "method": method, "params": params}); err != nil {
		harness.fail(fmt.Errorf("sending %s request: %w", method, err))
		harness.t.Fatalf("sdktest: send %s request: %v", method, err)
	}
	return call
}

func (harness *Harness) newCallLocked() *Call {
	id := fmt.Sprintf("sdktest-%d", harness.next.Add(1))
	ctx, cancel := context.WithCancel(harness.ctx)
	call := &Call{harness: harness, id: id, ctx: ctx, cancel: cancel, changed: make(chan struct{})}
	harness.calls[id] = call
	return call
}

func (call *Call) waitResponse(timeout time.Duration) (Response, error) {
	deadline := time.Now().Add(timeout)
	for {
		call.mu.Lock()
		if call.response != nil {
			response := *call.response
			call.mu.Unlock()
			return response, nil
		}
		if call.failure != nil {
			err := call.failure
			call.mu.Unlock()
			return Response{}, err
		}
		changed := call.changed
		call.mu.Unlock()
		if !waitUntil(changed, deadline) {
			return Response{}, fmt.Errorf("timed out after %s", timeout)
		}
	}
}

func waitUntil(changed <-chan struct{}, deadline time.Time) bool {
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	select {
	case <-changed:
		return true
	case <-timer.C:
		return false
	}
}

func (call *Call) deliverEvent(event Event) {
	call.mu.Lock()
	call.events = append(call.events, event)
	call.notifyLocked()
	call.mu.Unlock()
}

func (call *Call) deliverResponse(response Response) {
	call.mu.Lock()
	call.response = &response
	call.cancel()
	call.notifyLocked()
	call.mu.Unlock()
}

func (call *Call) fail(err error) {
	call.mu.Lock()
	if call.failure == nil {
		call.failure = err
		call.cancel()
		call.notifyLocked()
	}
	call.mu.Unlock()
}

func (call *Call) cancelContext() {
	call.mu.Lock()
	call.cancel()
	call.mu.Unlock()
}

func (call *Call) notifyLocked() {
	close(call.changed)
	call.changed = make(chan struct{})
}

func (harness *Harness) read() {
	var failure error
	defer func() {
		// Nothing drains the plugin's output once this goroutine stops, so every plugin
		// write would block on the pipe forever: its serve loop and step goroutines would
		// leak and close would wait out its timeouts. Closing the read end makes those
		// writes fail with the reason the host gave up.
		_ = harness.output.CloseWithError(failure)
		// fail before readDone: close reads the recorded failure once the reader is done.
		harness.fail(failure)
		close(harness.readDone)
	}()
	scanner := bufio.NewScanner(harness.output)
	scanner.Buffer(make([]byte, 64<<10), maxProtocolFrame+1)
	for scanner.Scan() {
		line := append([]byte(nil), scanner.Bytes()...)
		if err := harness.route(line); err != nil {
			failure = err
			return
		}
	}
	if err := scanner.Err(); err != nil {
		failure = fmt.Errorf("reading plugin protocol: %w", err)
		return
	}
	harness.mu.Lock()
	closing := harness.closing
	harness.mu.Unlock()
	if !closing {
		failure = io.ErrUnexpectedEOF
	}
}

func (harness *Harness) route(line []byte) error {
	var header struct {
		ID     string `json:"id"`
		Method string `json:"method"`
		Event  string `json:"event"`
	}
	if err := json.Unmarshal(line, &header); err != nil || header.ID == "" || header.Method != "" && header.Event != "" {
		return fmt.Errorf("malformed plugin frame")
	}
	if header.Method != "" {
		var request callbackRequest
		if err := decodeFrame(line, &request); err != nil || request.Method == "" {
			return fmt.Errorf("malformed plugin callback")
		}
		harness.startCallback(request)
		return nil
	}
	if header.Event != "" {
		var event eventFrame
		if err := decodeFrame(line, &event); err != nil || (event.Data == nil) == (event.Result == nil) {
			return fmt.Errorf("malformed plugin event")
		}
		decoded := []byte(nil)
		if event.Data != nil {
			var err error
			decoded, err = base64.StdEncoding.DecodeString(*event.Data)
			if err != nil {
				return fmt.Errorf("malformed plugin %s event data", event.Event)
			}
		}
		harness.mu.Lock()
		call := harness.calls[event.ID]
		harness.mu.Unlock()
		if call == nil {
			return fmt.Errorf("plugin sent event for unknown request id %q", event.ID)
		}
		call.deliverEvent(Event{Name: event.Event, Data: decoded, Result: event.Result})
		return nil
	}

	var response responseFrame
	if err := decodeFrame(line, &response); err != nil {
		return fmt.Errorf("malformed plugin response: %w", err)
	}
	if (response.Error == nil) == (response.Result == nil) {
		return fmt.Errorf("plugin response must contain exactly one of result or error")
	}
	harness.mu.Lock()
	call := harness.calls[response.ID]
	if call != nil {
		delete(harness.calls, response.ID)
	}
	harness.mu.Unlock()
	if call == nil {
		return fmt.Errorf("plugin sent response for unknown request id %q", response.ID)
	}
	call.deliverResponse(Response{Result: response.Result, Error: response.Error})
	return nil
}

func (harness *Harness) startCallback(request callbackRequest) {
	harness.mu.Lock()
	harness.callbacksActive++
	harness.mu.Unlock()
	go func() {
		defer harness.finishCallback()
		harness.handleCallback(request)
	}()
}

func (harness *Harness) finishCallback() {
	harness.mu.Lock()
	harness.callbacksActive--
	if harness.callbacksActive == 0 && harness.callbackIdle != nil {
		close(harness.callbackIdle)
		harness.callbackIdle = nil
	}
	harness.mu.Unlock()
}

// waitCallbacks blocks until no callback handler is running, so that close only
// closes the plugin's input once every callback response has been written.
func (harness *Harness) waitCallbacks(deadline time.Time) error {
	for {
		harness.mu.Lock()
		if harness.callbacksActive == 0 {
			harness.mu.Unlock()
			return nil
		}
		idle := harness.callbackIdle
		if idle == nil {
			idle = make(chan struct{})
			harness.callbackIdle = idle
		}
		harness.mu.Unlock()
		if !waitUntil(idle, deadline) {
			return fmt.Errorf("waiting for callbacks: timeout after %s", harness.timeout)
		}
	}
}

func (harness *Harness) handleCallback(request callbackRequest) {
	var scoped struct {
		ParentID string `json:"parent_id"`
	}
	if err := json.Unmarshal(request.Params, &scoped); err != nil || scoped.ParentID == "" {
		harness.writeCallbackResponse(request.ID, nil, &ResponseError{Code: "invalid_parent", Message: "host callback requires parent_id"})
		return
	}
	harness.mu.Lock()
	parent := harness.calls[scoped.ParentID]
	harness.mu.Unlock()
	if parent == nil {
		harness.writeCallbackResponse(request.ID, nil, &ResponseError{Code: "unknown_parent", Message: "host callback parent is unknown or completed"})
		return
	}

	var result any = map[string]any{}
	var callbackErr error
	switch request.Method {
	case string(sdk.HostTemplateValidate):
		var params struct {
			Content string `json:"content"`
		}
		if err := json.Unmarshal(request.Params, &params); err != nil {
			callbackErr = fmt.Errorf("decoding template validation callback: %w", err)
		} else if harness.callbacks.ValidateTemplate == nil {
			callbackErr = fmt.Errorf("template validation callback is not configured")
		} else {
			callbackErr = harness.callbacks.ValidateTemplate(parent.ctx, params.Content)
		}
	case string(sdk.HostTemplateRender):
		var params struct {
			Content string         `json:"content"`
			Extra   map[string]any `json:"extra"`
		}
		if err := json.Unmarshal(request.Params, &params); err != nil {
			callbackErr = fmt.Errorf("decoding template render callback: %w", err)
		} else if harness.callbacks.RenderTemplate == nil {
			callbackErr = fmt.Errorf("template render callback is not configured")
		} else {
			value, err := harness.callbacks.RenderTemplate(parent.ctx, params.Content, params.Extra)
			callbackErr = err
			result = map[string]any{"value": value}
		}
	case string(sdk.HostFunctionCall):
		var params struct {
			Name string `json:"name"`
			Args []any  `json:"args"`
		}
		if err := json.Unmarshal(request.Params, &params); err != nil {
			callbackErr = fmt.Errorf("decoding function callback: %w", err)
		} else if harness.callbacks.CallFunction == nil {
			callbackErr = fmt.Errorf("function callback is not configured")
		} else {
			value, err := harness.callbacks.CallFunction(parent.ctx, params.Name, params.Args)
			callbackErr = err
			result = map[string]any{"value": value}
		}
	default:
		callbackErr = fmt.Errorf("unsupported host callback %q", request.Method)
	}
	if callbackErr != nil {
		harness.writeCallbackResponse(request.ID, nil, &ResponseError{Code: "host_callback", Message: callbackErr.Error()})
		return
	}
	harness.writeCallbackResponse(request.ID, result, nil)
}

func (harness *Harness) writeCallbackResponse(id string, result any, protocolError *ResponseError) {
	frame := map[string]any{"id": id}
	if protocolError != nil {
		frame["error"] = protocolError
	} else {
		frame["result"] = result
	}
	data, err := json.Marshal(frame)
	if err != nil {
		data, _ = json.Marshal(map[string]any{"id": id, "error": &ResponseError{Code: "invalid_callback_result", Message: "host callback result is not JSON-compatible"}})
	}
	if len(data) > maxProtocolFrame {
		data, _ = json.Marshal(map[string]any{"id": id, "error": &ResponseError{Code: "frame_too_large", Message: "host callback response exceeds 10 MiB"}})
	}
	if err := harness.writeData(data); err != nil {
		harness.fail(fmt.Errorf("writing callback response %s: %w", id, err))
	}
}

func (harness *Harness) write(frame any) error {
	data, err := json.Marshal(frame)
	if err != nil {
		return err
	}
	if len(data) > maxProtocolFrame {
		return fmt.Errorf("protocol frame exceeds 10 MiB")
	}
	return harness.writeData(data)
}

func (harness *Harness) writeData(data []byte) error {
	harness.writeMu.Lock()
	defer harness.writeMu.Unlock()
	_, err := harness.input.Write(append(data, '\n'))
	return err
}

func (harness *Harness) fail(err error) {
	if err == nil {
		return
	}
	harness.mu.Lock()
	if harness.failure != nil {
		harness.mu.Unlock()
		return
	}
	harness.failure = err
	calls := make([]*Call, 0, len(harness.calls))
	for _, call := range harness.calls {
		calls = append(calls, call)
	}
	harness.mu.Unlock()
	harness.cancel()
	for _, call := range calls {
		call.fail(err)
	}
}

type callbackRequest struct {
	ID     string          `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

type eventFrame struct {
	ID     string          `json:"id"`
	Event  string          `json:"event"`
	Data   *string         `json:"data,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
}

type responseFrame struct {
	ID     string          `json:"id"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *ResponseError  `json:"error,omitempty"`
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
