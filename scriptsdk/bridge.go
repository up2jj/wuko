// Package scriptsdk exposes workflow capabilities over a language-independent,
// attempt-scoped local bridge. Language adapters only supply bootstrap and client code.
package scriptsdk

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/workflow"
)

const (
	// Version identifies the bridge protocol, independently of any language SDK.
	Version            = 1
	maxMessageBytes    = 10 << 20
	maxConcurrentCalls = 64
	// closeGrace bounds how long Close waits for host calls that ignore cancellation.
	closeGrace = 5 * time.Second
)

// Descriptor is the language-neutral bootstrap document. Its path is supplied in
// WUKO_SDK_DESCRIPTOR; adapters may read Context synchronously before user code runs.
type Descriptor struct {
	Version int             `json:"version"`
	Socket  string          `json:"socket"`
	Token   string          `json:"token"`
	Context json.RawMessage `json:"context"`
}

type callRequest struct {
	Version int             `json:"version"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type callError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type callResponse struct {
	Version int        `json:"version"`
	Result  any        `json:"result,omitempty"`
	Error   *callError `json:"error,omitempty"`
}

// Bridge owns a private endpoint and the staged result of one shell attempt.
type Bridge struct {
	dir        string
	descriptor Descriptor
	request    step.Request
	server     *http.Server
	cancel     context.CancelFunc
	served     chan struct{}
	slots      chan struct{}
	mu         sync.Mutex
	closing    bool
	outputs    map[string]any
	variables  map[string]any
	calls      sync.WaitGroup
	closeGrace time.Duration
	closeOnce  sync.Once
	closeErr   error
	sequence   atomic.Uint64
}

// Open starts a bridge bound to ctx. Call Close before retrieving its final Result.
func Open(ctx context.Context, request step.Request, args map[string]any) (*Bridge, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// Darwin limits Unix socket paths to 104 bytes; its default temporary directory
	// can consume most of that before a file name is added.
	dir, err := os.MkdirTemp("/tmp", "wuko-sdk-")
	if err != nil {
		return nil, fmt.Errorf("creating script SDK directory: %w", err)
	}
	clean := true
	defer func() {
		if clean {
			_ = os.RemoveAll(dir)
		}
	}()
	var secret [32]byte
	if _, err := rand.Read(secret[:]); err != nil {
		return nil, fmt.Errorf("creating script SDK token: %w", err)
	}
	run := request.RunValue()
	if run.EnvironmentLoaders == nil {
		run.EnvironmentLoaders = []string{}
	}
	snapshot := map[string]any{
		"args": nonNilMap(args), "vars": nonNilMap(request.Vars),
		"inputs": nonNilMap(request.Inputs), "steps": nonNilMap(request.Steps),
		"dependencies": nonNilMap(request.Dependencies), "env": nonNilMap(request.Env),
		"providers": nonNilMap(request.Providers.Values), "bindings": nonNilMap(request.Bindings),
		"workflow": map[string]any{"name": request.WorkflowName, "dir": request.WorkflowDir, "timezone": request.WorkflowTimezone},
		"run":      map[string]any{"dir": run.Dir, "environment_loaders": run.EnvironmentLoaders, "os": run.OS, "arch": run.Arch},
		"attempt":  map[string]any{"number": max(request.Attempt, 1), "max_attempts": max(request.MaxAttempts, 1), "operation_id": request.OperationID},
	}
	// Encode once: the immutable bytes freeze the state for concurrent callbacks and
	// are reused verbatim by the descriptor file and the context call.
	data, err := json.Marshal(snapshot)
	if err != nil {
		return nil, fmt.Errorf("encoding script SDK context: %w", err)
	}
	if len(data) > maxMessageBytes {
		return nil, fmt.Errorf("script SDK context exceeds 10 MiB")
	}
	// Retain only the capabilities callbacks use, so the bridge does not keep the
	// request's state copies alive alongside the frozen snapshot.
	host := step.Request{
		StepID: request.StepID, OperationID: request.OperationID, Secret: request.Secret,
		Helpers: request.Helpers.Clone(), TemplateRenderer: request.TemplateRenderer,
		InlineWorkflows: request.InlineWorkflows,
	}
	if renderer, ok := request.TemplateRenderer.(step.DataTemplateRenderer); ok {
		host.TemplateRenderer = renderer.Snapshot()
	}
	bridgeCtx, cancel := context.WithCancel(ctx)
	b := &Bridge{
		dir: dir, request: host, cancel: cancel, served: make(chan struct{}), closeGrace: closeGrace,
		slots: make(chan struct{}, maxConcurrentCalls), outputs: make(map[string]any), variables: make(map[string]any),
		descriptor: Descriptor{Version: Version, Socket: filepath.Join(dir, "bridge.sock"), Token: hex.EncodeToString(secret[:]), Context: data},
	}
	listener, err := net.Listen("unix", b.descriptor.Socket)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("opening script SDK socket: %w", err)
	}
	if err := os.Chmod(b.descriptor.Socket, 0o600); err != nil {
		cancel()
		_ = listener.Close()
		return nil, fmt.Errorf("protecting script SDK socket: %w", err)
	}
	b.server = &http.Server{
		Handler: http.HandlerFunc(b.handle), ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout: 30 * time.Second, IdleTimeout: 5 * time.Second,
		MaxHeaderBytes: 8192,
		BaseContext:    func(net.Listener) context.Context { return bridgeCtx },
	}
	go func() { defer close(b.served); _ = b.server.Serve(listener) }()
	clean = false
	return b, nil
}

func nonNilMap[V any](value map[string]V) map[string]V {
	if value == nil {
		return map[string]V{}
	}
	return value
}

// Close cancels callbacks, joins active handlers and removes all bootstrap files.
// Handlers still running after the grace period are abandoned; staging rejects
// their writes, so they cannot change the Result.
func (b *Bridge) Close() error {
	b.closeOnce.Do(func() {
		b.mu.Lock()
		b.closing = true
		b.mu.Unlock()
		b.cancel()
		err := b.server.Close()
		<-b.served
		joined := make(chan struct{})
		go func() { b.calls.Wait(); close(joined) }()
		timer := time.NewTimer(b.closeGrace)
		defer timer.Stop()
		select {
		case <-joined:
		case <-timer.C:
			err = errors.Join(err, fmt.Errorf("SDK host calls did not stop within %s of cancellation", b.closeGrace))
		}
		b.closeErr = errors.Join(err, os.RemoveAll(b.dir))
	})
	return b.closeErr
}

// Result returns a copy of staged outputs and variables. The engine owns commit policy.
func (b *Bridge) Result() step.Result {
	b.mu.Lock()
	defer b.mu.Unlock()
	return step.Result{Outputs: workflow.CloneMap(b.outputs), Variables: workflow.CloneMap(b.variables)}
}

func (b *Bridge) handle(w http.ResponseWriter, r *http.Request) {
	if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+b.descriptor.Token)) != 1 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if r.Method != http.MethodPost || r.URL.Path != "/v1/call" {
		http.Error(w, "use POST /v1/call", http.StatusNotFound)
		return
	}
	b.mu.Lock()
	if b.closing {
		b.mu.Unlock()
		http.Error(w, "bridge closed", http.StatusServiceUnavailable)
		return
	}
	b.calls.Add(1)
	b.mu.Unlock()
	defer b.calls.Done()
	// Excess calls queue for a slot; Close or a client disconnect cancels the wait.
	select {
	case b.slots <- struct{}{}:
		defer func() { <-b.slots }()
	case <-r.Context().Done():
		b.reply(w, nil, &callError{Code: "canceled", Message: r.Context().Err().Error()})
		return
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxMessageBytes))
	if err != nil {
		b.reply(w, nil, &callError{Code: "invalid_request", Message: "cannot read SDK request (limit 10 MiB)"})
		return
	}
	var call callRequest
	if err := decode(data, &call); err != nil || call.Version != Version || call.Method == "" {
		b.reply(w, nil, &callError{Code: "invalid_request", Message: "expected bridge version 1 and a method"})
		return
	}
	if len(call.Params) == 0 {
		call.Params = json.RawMessage(`{}`)
	}
	result, err := b.dispatch(r.Context(), call.Method, call.Params)
	if err != nil {
		code := "host_error"
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			code = "canceled"
		}
		b.reply(w, nil, &callError{Code: code, Message: err.Error()})
		return
	}
	b.reply(w, result, nil)
}

func (b *Bridge) reply(w http.ResponseWriter, result any, failure *callError) {
	data, err := json.Marshal(callResponse{Version: Version, Result: result, Error: failure})
	if err != nil || len(data) > maxMessageBytes {
		data, _ = json.Marshal(callResponse{Version: Version, Error: &callError{Code: "invalid_result", Message: "SDK result is not JSON-compatible or exceeds 10 MiB"}})
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(data)
}

func decode(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("unexpected trailing JSON")
	}
	return nil
}

// DescriptorEnvironment names the adapter-neutral bootstrap environment variable.
const DescriptorEnvironment = "WUKO_SDK_DESCRIPTOR"

func (b *Bridge) writeDescriptor() (string, error) {
	data, err := json.Marshal(b.descriptor)
	if err != nil {
		return "", err
	}
	path := filepath.Join(b.dir, "context.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// contextResult excludes transport credentials from the context operation.
func (b *Bridge) contextResult() json.RawMessage { return b.descriptor.Context }
