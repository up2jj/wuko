package sdk

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"sync"
	"sync/atomic"
)

const maxProtocolFrame = 10 << 20

type wireRequest struct {
	ID     string          `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

type wireResponse struct {
	ID     string          `json:"id"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *wireError      `json:"error,omitempty"`
}

type outgoingResponse struct {
	ID     string     `json:"id"`
	Result any        `json:"result,omitempty"`
	Error  *wireError `json:"error,omitempty"`
}

type wireError struct {
	Code    string `json:"code,omitempty"`
	Message string `json:"message"`
}

type peer struct {
	out       io.Writer
	writeMu   sync.Mutex
	mu        sync.Mutex
	active    map[string]context.CancelFunc
	callbacks map[string]chan wireResponse
	abandoned map[string]struct{}
	next      atomic.Uint64
	wg        sync.WaitGroup
}

type requestContextKey struct{}
type peerContextKey struct{}

func serve(root context.Context, input io.Reader, output io.Writer, dispatch func(context.Context, wireRequest, *peer) (any, error)) error {
	protocolPeer := &peer{out: output, active: make(map[string]context.CancelFunc), callbacks: make(map[string]chan wireResponse), abandoned: make(map[string]struct{})}
	// A shutdown reply waits for the in-flight requests and so cannot be one of them: adding it
	// to wg would make it wait on itself. Without a counter of its own, a cancel arriving between
	// the shutdown request and its reply lets the process exit before the reply is written, and
	// the host's close waits out its timeout instead of returning.
	var shutdownReply sync.WaitGroup
	defer func() {
		protocolPeer.cancelAll()
		protocolPeer.wg.Wait()
		shutdownReply.Wait()
	}()
	lines := make(chan []byte)
	readErrors := make(chan error, 1)
	// serve returns on protocol errors while root is still live; without a stop signal the
	// reader would block forever on an unread line and leak itself and the scanner buffer.
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		scanner := bufio.NewScanner(input)
		// The cap bounds the JSON, and the terminating newline is framing on top of it, so the
		// scanner needs room for both: capped at maxProtocolFrame it would reject the largest
		// frame either side is allowed to write as a token too long.
		scanner.Buffer(make([]byte, 64<<10), maxProtocolFrame+1)
		for scanner.Scan() {
			line := append([]byte(nil), scanner.Bytes()...)
			select {
			case lines <- line:
			case <-root.Done():
				return
			case <-stop:
				return
			}
		}
		readErrors <- scanner.Err()
		close(lines)
	}()
	shuttingDown := false
	for {
		select {
		case <-root.Done():
			protocolPeer.cancelAll()
			protocolPeer.wg.Wait()
			return root.Err()
		case line, ok := <-lines:
			if !ok {
				protocolPeer.cancelAll()
				protocolPeer.wg.Wait()
				if err := <-readErrors; err != nil && root.Err() == nil {
					return fmt.Errorf("reading protocol: %w", err)
				}
				return nil
			}
			var envelope struct {
				ID     string `json:"id"`
				Method string `json:"method"`
			}
			if err := json.Unmarshal(line, &envelope); err != nil || envelope.ID == "" && envelope.Method != "cancel" {
				return fmt.Errorf("malformed protocol frame")
			}
			if envelope.Method == "" {
				var response wireResponse
				if err := json.Unmarshal(line, &response); err != nil || (response.Error == nil) == (response.Result == nil) {
					return fmt.Errorf("malformed callback response %q", envelope.ID)
				}
				if !protocolPeer.deliver(response) {
					return fmt.Errorf("unknown callback response %q", envelope.ID)
				}
				continue
			}
			var request wireRequest
			if err := json.Unmarshal(line, &request); err != nil {
				return fmt.Errorf("decoding request: %w", err)
			}
			if request.ID == "" {
				if request.Method != "cancel" {
					return fmt.Errorf("notification %q is unsupported", request.Method)
				}
				var params struct {
					ID string `json:"id"`
				}
				if err := json.Unmarshal(request.Params, &params); err != nil || params.ID == "" {
					return fmt.Errorf("invalid cancel notification")
				}
				protocolPeer.cancel(params.ID)
				continue
			}
			if request.Method == "shutdown" {
				if shuttingDown {
					return fmt.Errorf("duplicate shutdown request")
				}
				shuttingDown = true
				protocolPeer.cancelAll()
				shutdownReply.Add(1)
				go func(id string) {
					defer shutdownReply.Done()
					protocolPeer.wg.Wait()
					_ = protocolPeer.reply(id, map[string]any{}, nil)
				}(request.ID)
				continue
			}
			if shuttingDown {
				_ = protocolPeer.reply(request.ID, nil, fmt.Errorf("plugin is shutting down"))
				continue
			}
			requestCtx, cancel := context.WithCancel(root)
			requestCtx = context.WithValue(requestCtx, requestContextKey{}, request.ID)
			requestCtx = context.WithValue(requestCtx, peerContextKey{}, protocolPeer)
			if !protocolPeer.add(request.ID, cancel) {
				cancel()
				return fmt.Errorf("duplicate request id %q", request.ID)
			}
			protocolPeer.wg.Add(1)
			go func(request wireRequest, requestCtx context.Context) {
				defer protocolPeer.wg.Done()
				defer protocolPeer.remove(request.ID)
				result, err := dispatch(requestCtx, request, protocolPeer)
				_ = protocolPeer.reply(request.ID, result, err)
			}(request, requestCtx)
		}
	}
}

func (peer *peer) add(id string, cancel context.CancelFunc) bool {
	peer.mu.Lock()
	defer peer.mu.Unlock()
	if _, exists := peer.active[id]; exists {
		return false
	}
	peer.active[id] = cancel
	return true
}

func (peer *peer) remove(id string) {
	peer.mu.Lock()
	cancel := peer.active[id]
	delete(peer.active, id)
	peer.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (peer *peer) cancel(id string) {
	peer.mu.Lock()
	cancel := peer.active[id]
	peer.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (peer *peer) cancelAll() {
	peer.mu.Lock()
	cancels := make([]context.CancelFunc, 0, len(peer.active))
	for _, cancel := range peer.active {
		cancels = append(cancels, cancel)
	}
	peer.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
}

func (peer *peer) deliver(response wireResponse) bool {
	peer.mu.Lock()
	target := peer.callbacks[response.ID]
	if target != nil {
		delete(peer.callbacks, response.ID)
	}
	_, abandoned := peer.abandoned[response.ID]
	if abandoned {
		delete(peer.abandoned, response.ID)
	}
	peer.mu.Unlock()
	if target == nil {
		return abandoned
	}
	target <- response
	return true
}

func (peer *peer) write(value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if len(data) > maxProtocolFrame {
		return fmt.Errorf("protocol frame exceeds 10 MiB")
	}
	peer.writeMu.Lock()
	defer peer.writeMu.Unlock()
	data = append(data, '\n')
	_, err = peer.out.Write(data)
	return err
}

func (peer *peer) reply(id string, result any, err error) error {
	response := outgoingResponse{ID: id, Result: result}
	if err != nil {
		response.Result = nil
		response.Error = &wireError{Message: err.Error()}
		var coded *Error
		if errors.As(err, &coded) {
			response.Error.Code = coded.Code
		}
	}
	return peer.write(response)
}

func (peer *peer) event(id, event string, data []byte) error {
	return peer.write(map[string]any{"id": id, "event": event, "data": base64.StdEncoding.EncodeToString(data)})
}

func (peer *peer) eventResult(id, event string, result Result) error {
	return peer.write(map[string]any{"id": id, "event": event, "result": result})
}

type eventWriter struct {
	peer  *peer
	id    string
	event string
}

func (writer eventWriter) Write(data []byte) (int, error) {
	const chunkSize = 32 << 10
	for start := 0; start < len(data); start += chunkSize {
		end := min(start+chunkSize, len(data))
		if err := writer.peer.event(writer.id, writer.event, data[start:end]); err != nil {
			return start, err
		}
	}
	return len(data), nil
}

func callHost(ctx context.Context, method string, params map[string]any, result any) error {
	protocolPeer, ok := ctx.Value(peerContextKey{}).(*peer)
	if !ok {
		return fmt.Errorf("host peer is unavailable")
	}
	parent, ok := ctx.Value(requestContextKey{}).(string)
	if !ok {
		return fmt.Errorf("parent request is unavailable")
	}
	id := "plugin-" + strconv.FormatUint(protocolPeer.next.Add(1), 10)
	wireParams := make(map[string]any, len(params)+1)
	for key, value := range params {
		wireParams[key] = value
	}
	wireParams["parent_id"] = parent
	replies := make(chan wireResponse, 1)
	protocolPeer.mu.Lock()
	protocolPeer.callbacks[id] = replies
	protocolPeer.mu.Unlock()
	if err := protocolPeer.write(wireRequest{ID: id, Method: method, Params: mustJSON(wireParams)}); err != nil {
		protocolPeer.mu.Lock()
		delete(protocolPeer.callbacks, id)
		protocolPeer.mu.Unlock()
		return err
	}
	select {
	case response := <-replies:
		if response.Error != nil {
			return &Error{Code: response.Error.Code, Err: errors.New(response.Error.Message)}
		}
		if result != nil {
			return json.Unmarshal(response.Result, result)
		}
		return nil
	case <-ctx.Done():
		// Cancellation can race a reply that deliver has already routed. Only a callback still
		// registered here can still be answered, so only that one is worth remembering as
		// abandoned; marking one deliver already completed would leak a map entry no response
		// will ever clear.
		protocolPeer.mu.Lock()
		if _, pending := protocolPeer.callbacks[id]; pending {
			delete(protocolPeer.callbacks, id)
			protocolPeer.abandoned[id] = struct{}{}
		}
		protocolPeer.mu.Unlock()
		return ctx.Err()
	}
}

func mustJSON(value any) json.RawMessage {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return encoded
}
