package process

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
)

const maxRPCLineBytes = 10 << 20

var errRPCSessionClosed = errors.New("process RPC session closed")

type rpcRegistry struct {
	mu      sync.Mutex
	workers map[string]*rpcWorker
	changed chan struct{}
	// next holds one rotation cursor per worker set. A single shared cursor would be indexed
	// into whatever slice the current call passed, so a single-worker call would reset the
	// position a pool was rotating through.
	next map[string]int
}

type rpcWorker struct {
	id      string
	session *rpcSession
	call    *rpcCall
}

type rpcCall struct {
	id       string
	response chan rpcResponse
}

type rpcResponse struct {
	value any
	err   error
}

type rpcSession struct {
	registry *rpcRegistry
	workerID string
	// input carries requests to the worker's stdin, and reader is the end the worker reads.
	// Both are OS pipe files so os/exec hands the child a duplicated descriptor: with any
	// other reader it copies stdin in a goroutine that Cmd.Wait joins, and that goroutine
	// only returns once this session closes, which happens after the exit it would hide.
	input    *os.File
	reader   *os.File
	protocol chan error
	done     chan struct{}

	// closed marks a session that has been detached for good, guarded by the registry mutex so
	// that a late activate cannot publish a session the lifecycle has already torn down.
	closed    bool
	outputMu  sync.Mutex
	output    []byte
	failOnce  sync.Once
	closeOnce sync.Once
}

type rpcRequest struct {
	ID      string `json:"id"`
	Payload any    `json:"payload"`
}

type rpcError struct {
	Message string `json:"message"`
}

type rpcEnvelope struct {
	ID     string          `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error"`
}

func newRPCRegistry() *rpcRegistry {
	return &rpcRegistry{workers: make(map[string]*rpcWorker), changed: make(chan struct{}), next: make(map[string]int)}
}

func (registry *rpcRegistry) declare(id string) error {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if _, exists := registry.workers[id]; exists {
		return fmt.Errorf("process RPC worker %q is already registered", id)
	}
	registry.workers[id] = &rpcWorker{id: id}
	registry.signalLocked()
	return nil
}

func (registry *rpcRegistry) forget(id string, cause error) {
	if cause == nil {
		cause = errRPCSessionClosed
	}
	registry.mu.Lock()
	worker := registry.workers[id]
	if worker != nil {
		if worker.call != nil {
			worker.call.response <- rpcResponse{err: cause}
		}
		delete(registry.workers, id)
		registry.signalLocked()
	}
	registry.mu.Unlock()
}

func (registry *rpcRegistry) attach(id string, session *rpcSession) error {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if session.closed {
		return errRPCSessionClosed
	}
	worker := registry.workers[id]
	if worker == nil {
		return fmt.Errorf("process RPC worker %q is not registered", id)
	}
	if worker.session != nil {
		return fmt.Errorf("process RPC worker %q already has an active session", id)
	}
	worker.session = session
	registry.signalLocked()
	return nil
}

func (registry *rpcRegistry) detach(session *rpcSession, cause error) {
	registry.mu.Lock()
	session.closed = true
	worker := registry.workers[session.workerID]
	if worker != nil && worker.session == session {
		worker.session = nil
		if worker.call != nil {
			worker.call.response <- rpcResponse{err: cause}
			worker.call = nil
		}
		registry.signalLocked()
	}
	registry.mu.Unlock()
}

func (registry *rpcRegistry) signalLocked() {
	close(registry.changed)
	registry.changed = make(chan struct{})
}

func (registry *rpcRegistry) call(ctx context.Context, workerIDs []string, payload any) (any, string, error) {
	for {
		worker, call, changed, err := registry.reserve(workerIDs)
		if err != nil {
			return nil, "", err
		}
		if worker == nil {
			select {
			case <-changed:
				continue
			case <-ctx.Done():
				return nil, "", fmt.Errorf("waiting for an available process RPC worker: %w", ctx.Err())
			}
		}
		value, retry, err := registry.invoke(ctx, worker, call, payload)
		if retry && ctx.Err() == nil {
			// The request never reached a worker, so nothing has run it. Reserving again lets
			// an idle or restarted worker take the call within the caller's timeout.
			continue
		}
		return value, worker.id, err
	}
}

// reserve claims an idle worker from workerIDs. A pool member whose service has ended is no
// longer registered, so the members that remain keep serving calls and only a pool with nothing
// registered fails outright. Members that are registered but busy or restarting leave the caller
// waiting on the returned channel instead.
func (registry *rpcRegistry) reserve(workerIDs []string) (*rpcWorker, *rpcCall, <-chan struct{}, error) {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if !registry.registeredLocked(workerIDs) {
		return nil, nil, nil, unregisteredWorkersError(workerIDs)
	}
	key := poolKey(workerIDs)
	start := registry.next[key]
	for offset := range len(workerIDs) {
		index := (start + offset) % len(workerIDs)
		worker := registry.workers[workerIDs[index]]
		if worker == nil || worker.session == nil || worker.call != nil {
			continue
		}
		id, err := opaqueID("call")
		if err != nil {
			return nil, nil, nil, err
		}
		call := &rpcCall{id: id, response: make(chan rpcResponse, 1)}
		worker.call = call
		registry.next[key] = (index + 1) % len(workerIDs)
		return worker, call, nil, nil
	}
	return nil, nil, registry.changed, nil
}

func (registry *rpcRegistry) registeredLocked(workerIDs []string) bool {
	for _, id := range workerIDs {
		if registry.workers[id] != nil {
			return true
		}
	}
	return false
}

func poolKey(workerIDs []string) string {
	return strings.Join(workerIDs, "\x00")
}

func unregisteredWorkersError(workerIDs []string) error {
	if len(workerIDs) == 1 {
		return fmt.Errorf("process RPC worker %q is not registered", workerIDs[0])
	}
	names := make([]string, len(workerIDs))
	for index, id := range workerIDs {
		names[index] = strconv.Quote(id)
	}
	return fmt.Errorf("no process RPC worker in the pool is registered: %s", strings.Join(names, ", "))
}

// invoke sends payload to the reserved worker and waits for its response. The second result
// marks a failure that happened before the request reached the worker: nothing has run the call,
// so the caller may reserve another worker instead of failing. A request that was written is
// never sent again, even when its answer never arrives.
func (registry *rpcRegistry) invoke(ctx context.Context, worker *rpcWorker, call *rpcCall, payload any) (any, bool, error) {
	request, err := json.Marshal(rpcRequest{ID: call.id, Payload: payload})
	if err != nil {
		registry.release(worker, call)
		return nil, false, fmt.Errorf("encoding process RPC request: %w", err)
	}
	if len(request)+1 > maxRPCLineBytes {
		registry.release(worker, call)
		return nil, false, fmt.Errorf("process RPC request exceeds %d bytes", maxRPCLineBytes)
	}
	request = append(request, '\n')

	registry.mu.Lock()
	session := worker.session
	registry.mu.Unlock()
	if session == nil {
		registry.release(worker, call)
		return nil, true, errRPCSessionClosed
	}
	if err := session.write(ctx, request); err != nil {
		session.fail(fmt.Errorf("writing process RPC request: %w", err))
		// A failed write stops before the newline that completes the request line, so no
		// worker can have acted on what reached the pipe.
		return nil, true, fmt.Errorf("writing process RPC request: %w", err)
	}

	select {
	case response := <-call.response:
		return response.value, false, response.err
	case <-ctx.Done():
		// Keep the worker reserved until its matching response arrives or the session
		// closes. Releasing it here could route that late response to a later call.
		return nil, false, fmt.Errorf("waiting for process RPC response: %w", ctx.Err())
	}
}

func (registry *rpcRegistry) release(worker *rpcWorker, call *rpcCall) {
	registry.mu.Lock()
	if current := registry.workers[worker.id]; current == worker && worker.call == call {
		worker.call = nil
		registry.signalLocked()
	}
	registry.mu.Unlock()
}

func newRPCSession(registry *rpcRegistry, workerID string) (*rpcSession, error) {
	reader, writer, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("creating process RPC pipe: %w", err)
	}
	return &rpcSession{registry: registry, workerID: workerID, input: writer, reader: reader, protocol: make(chan error, 1), done: make(chan struct{})}, nil
}

func (session *rpcSession) activate() error {
	return session.registry.attach(session.workerID, session)
}

func (session *rpcSession) close(cause error) {
	session.closeOnce.Do(func() {
		if cause == nil {
			cause = errRPCSessionClosed
		}
		_ = session.input.Close()
		_ = session.reader.Close()
		session.registry.detach(session, cause)
		close(session.done)
	})
}

func (session *rpcSession) fail(err error) {
	session.failOnce.Do(func() {
		session.registry.detach(session, err)
		select {
		case session.protocol <- err:
		default:
		}
		_ = session.input.Close()
	})
}

func (session *rpcSession) write(ctx context.Context, data []byte) error {
	done := make(chan error, 1)
	go func() {
		_, err := session.input.Write(data)
		done <- err
	}()
	select {
	case err := <-done:
		if errors.Is(err, os.ErrClosed) {
			return errRPCSessionClosed
		}
		return err
	case <-ctx.Done():
		_ = session.input.Close()
		<-done
		return ctx.Err()
	}
}

func (session *rpcSession) Write(data []byte) (int, error) {
	session.outputMu.Lock()
	defer session.outputMu.Unlock()
	session.output = append(session.output, data...)
	for {
		newline := bytes.IndexByte(session.output, '\n')
		if newline < 0 {
			if len(session.output) > maxRPCLineBytes {
				err := fmt.Errorf("process RPC response exceeds %d bytes", maxRPCLineBytes)
				session.fail(err)
				return 0, err
			}
			return len(data), nil
		}
		if newline+1 > maxRPCLineBytes {
			err := fmt.Errorf("process RPC response exceeds %d bytes", maxRPCLineBytes)
			session.fail(err)
			return 0, err
		}
		line := bytes.TrimSuffix(session.output[:newline], []byte{'\r'})
		session.output = session.output[newline+1:]
		if err := session.handleLine(line); err != nil {
			session.fail(err)
			return 0, err
		}
	}
}

func (session *rpcSession) handleLine(line []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(line))
	decoder.DisallowUnknownFields()
	var envelope rpcEnvelope
	if err := decoder.Decode(&envelope); err != nil {
		return fmt.Errorf("decoding process RPC response: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("decoding process RPC response: trailing JSON value")
		}
		return fmt.Errorf("decoding process RPC response: %w", err)
	}
	if strings.TrimSpace(envelope.ID) == "" {
		return fmt.Errorf("process RPC response requires id")
	}
	if (envelope.Result == nil) == (envelope.Error == nil) {
		return fmt.Errorf("process RPC response %q requires exactly one of result or error", envelope.ID)
	}
	var response rpcResponse
	if envelope.Error != nil {
		if strings.TrimSpace(envelope.Error.Message) == "" {
			return fmt.Errorf("process RPC response %q has an empty error message", envelope.ID)
		}
		response.err = errors.New(envelope.Error.Message)
	} else {
		decoder := json.NewDecoder(bytes.NewReader(envelope.Result))
		if err := decoder.Decode(&response.value); err != nil {
			return fmt.Errorf("decoding process RPC result %q: %w", envelope.ID, err)
		}
	}

	session.registry.mu.Lock()
	worker := session.registry.workers[session.workerID]
	if worker == nil || worker.session != session || worker.call == nil || worker.call.id != envelope.ID {
		session.registry.mu.Unlock()
		return fmt.Errorf("process RPC response has unknown id %q", envelope.ID)
	}
	call := worker.call
	worker.call = nil
	session.registry.signalLocked()
	session.registry.mu.Unlock()
	call.response <- response
	return nil
}

func opaqueID(prefix string) (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("generating process RPC identifier: %w", err)
	}
	return prefix + "_" + hex.EncodeToString(value[:]), nil
}
