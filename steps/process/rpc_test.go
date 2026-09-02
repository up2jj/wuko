package process

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/up2jj/wuko/step"
)

func TestRPCWorkerProcess(t *testing.T) {
	if os.Getenv("WUKO_RPC_TEST_HELPER") != "1" {
		return
	}
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 64<<10), maxRPCLineBytes)
	encoder := json.NewEncoder(os.Stdout)
	for scanner.Scan() {
		var request rpcRequest
		if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
			os.Exit(2)
		}
		response := map[string]any{"id": request.ID, "result": map[string]any{
			"payload": request.Payload,
			"worker":  os.Getenv("WUKO_RPC_TEST_WORKER"),
		}}
		if err := encoder.Encode(response); err != nil {
			os.Exit(3)
		}
	}
	if err := scanner.Err(); err != nil && !strings.Contains(err.Error(), "file already closed") {
		os.Exit(4)
	}
	os.Exit(0)
}

func TestProcessCallUsesSingleWorker(t *testing.T) {
	registry := newRPCRegistry()
	worker := startRPCWorker(t, registry, "single")
	call, err := newCall(map[string]any{
		"worker":  worker,
		"payload": map[string]any{"component": "Hello", "props": map[string]any{"name": "Wuko"}},
	}, registry)
	if err != nil {
		t.Fatal(err)
	}
	result, err := call.Run(t.Context(), step.Request{})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"payload": map[string]any{"component": "Hello", "props": map[string]any{"name": "Wuko"}},
		"worker":  "single",
	}
	if got := result.Outputs["result"]; !reflect.DeepEqual(got, want) {
		t.Fatalf("result = %#v, want %#v", got, want)
	}
	if result.Outputs["worker_id"] != worker {
		t.Fatalf("worker_id = %v, want %q", result.Outputs["worker_id"], worker)
	}
}

func TestProcessCallSelectsPoolWorkersFairly(t *testing.T) {
	registry := newRPCRegistry()
	workers := []any{startRPCWorker(t, registry, "alpha"), startRPCWorker(t, registry, "beta")}
	call, err := newCall(map[string]any{"pool": "vars.workers", "payload_expr": "vars.payload"}, registry)
	if err != nil {
		t.Fatal(err)
	}
	var selected []string
	for index := range 4 {
		result, err := call.Run(t.Context(), step.Request{Vars: map[string]any{"workers": workers, "payload": index}})
		if err != nil {
			t.Fatal(err)
		}
		selected = append(selected, result.Outputs["worker_id"].(string))
	}
	want := []string{workers[0].(string), workers[1].(string), workers[0].(string), workers[1].(string)}
	if !reflect.DeepEqual(selected, want) {
		t.Fatalf("selected workers = %#v, want %#v", selected, want)
	}
}

func TestRPCWorkerExitIsObservedWhileReadinessWaits(t *testing.T) {
	registry := newRPCRegistry()
	runnerValue, err := newProcess(map[string]any{
		"script":    "printf 'starting\\n' >&2; exit 7",
		"rpc":       "jsonl",
		"readiness": map[string]any{"log": map[string]any{"pattern": "never matches", "timeout": "20s"}},
	}, registry)
	if err != nil {
		t.Fatal(err)
	}
	services := newTestServices(t)
	started := time.Now()
	_, err = runnerValue.Run(t.Context(), step.Request{
		StepID: "worker", Services: services, Env: map[string]string{}, Stdout: io.Discard, Stderr: io.Discard,
	})
	if err == nil || !strings.Contains(err.Error(), "process exited before readiness log matched") {
		t.Fatalf("error = %v", err)
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("exit reported after %s; the readiness timeout was reached instead", elapsed)
	}
}

func TestProcessCallUsesTheRegisteredPoolMembers(t *testing.T) {
	registry := newRPCRegistry()
	workers := []any{startRPCWorker(t, registry, "alpha"), startRPCWorker(t, registry, "beta")}
	registry.forget(workers[0].(string), errors.New("worker crashed"))
	call, err := newCall(map[string]any{"pool": "vars.workers", "payload": "ping"}, registry)
	if err != nil {
		t.Fatal(err)
	}
	request := step.Request{Vars: map[string]any{"workers": workers}}
	result, err := call.Run(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outputs["worker_id"] != workers[1] {
		t.Fatalf("worker_id = %v, want %v", result.Outputs["worker_id"], workers[1])
	}
	registry.forget(workers[1].(string), errors.New("worker crashed"))
	if _, err := call.Run(t.Context(), request); err == nil || !strings.Contains(err.Error(), "no process RPC worker in the pool is registered") {
		t.Fatalf("error = %v", err)
	}
}

func TestProcessCallRotatesEachPoolIndependently(t *testing.T) {
	registry := newRPCRegistry()
	workers := []any{startRPCWorker(t, registry, "alpha"), startRPCWorker(t, registry, "beta")}
	pooled, err := newCall(map[string]any{"pool": "vars.workers", "payload": "ping"}, registry)
	if err != nil {
		t.Fatal(err)
	}
	direct, err := newCall(map[string]any{"worker": workers[0].(string), "payload": "ping"}, registry)
	if err != nil {
		t.Fatal(err)
	}
	var selected []string
	for range 2 {
		result, err := pooled.Run(t.Context(), step.Request{Vars: map[string]any{"workers": workers}})
		if err != nil {
			t.Fatal(err)
		}
		selected = append(selected, result.Outputs["worker_id"].(string))
		if _, err := direct.Run(t.Context(), step.Request{}); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{workers[0].(string), workers[1].(string)}
	if !reflect.DeepEqual(selected, want) {
		t.Fatalf("selected workers = %#v, want %#v", selected, want)
	}
}

func TestRPCSessionIsPublishedOnlyAfterReadiness(t *testing.T) {
	registry := newRPCRegistry()
	gate := filepath.Join(t.TempDir(), "ready")
	runnerValue, err := newProcess(map[string]any{
		"script":    `while [ ! -f "$1" ]; do sleep 0.01; done; printf 'ready\n' >&2; cat > /dev/null`,
		"args":      []any{gate},
		"rpc":       "jsonl",
		"readiness": map[string]any{"log": map[string]any{"pattern": "ready", "timeout": "10s"}},
		"shutdown":  map[string]any{"timeout": "100ms"},
	}, registry)
	if err != nil {
		t.Fatal(err)
	}
	worker := runnerValue.(*Runner).workerID
	services := newTestServices(t)
	started := make(chan error, 1)
	go func() {
		_, runErr := runnerValue.Run(t.Context(), step.Request{
			StepID: "worker", Services: services, Env: map[string]string{}, Stdout: io.Discard, Stderr: io.Discard,
		})
		started <- runErr
	}()
	waitForCondition(t, "the worker to be registered", func() bool { return workerRegistered(registry, worker) })
	time.Sleep(50 * time.Millisecond)
	if workerSession(registry, worker) != nil {
		t.Fatal("the session was published while readiness was still pending")
	}
	if err := os.WriteFile(gate, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := <-started; err != nil {
		t.Fatal(err)
	}
	if workerSession(registry, worker) == nil {
		t.Fatal("the session was not published after readiness")
	}
}

func TestRPCCallReachesTheRestartedWorker(t *testing.T) {
	registry := newRPCRegistry()
	runnerValue, err := newProcess(map[string]any{
		"script":    `printf 'ready\n' >&2; read line; id=$(printf '%s' "$line" | sed 's/.*"id":"\([^"]*\)".*/\1/'); printf '{"id":"%s","result":"pong"}\n' "$id"`,
		"rpc":       "jsonl",
		"readiness": map[string]any{"log": map[string]any{"pattern": "ready", "timeout": "10s"}},
		"restart":   map[string]any{"policy": "always", "backoff": "1ms"},
		"shutdown":  map[string]any{"timeout": "100ms"},
	}, registry)
	if err != nil {
		t.Fatal(err)
	}
	services := newTestServices(t)
	result, err := runnerValue.Run(t.Context(), step.Request{
		StepID: "worker", Services: services, Env: map[string]string{}, Stdout: io.Discard, Stderr: io.Discard,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Every instance answers one call and exits, so each later call has to land on a
	// replacement that has matched its readiness log again.
	worker := result.Outputs["worker_id"].(string)
	call, err := newCall(map[string]any{"worker": worker, "payload": "ping", "timeout": "10s"}, registry)
	if err != nil {
		t.Fatal(err)
	}
	for attempt := range 3 {
		previous := workerSession(registry, worker)
		if previous == nil {
			t.Fatalf("call %d: the worker was not callable", attempt)
		}
		out, err := call.Run(t.Context(), step.Request{})
		if err != nil {
			t.Fatalf("call %d: %v", attempt, err)
		}
		if out.Outputs["result"] != "pong" {
			t.Fatalf("call %d result = %#v", attempt, out.Outputs["result"])
		}
		waitForCondition(t, "the replacement worker to become callable", func() bool {
			session := workerSession(registry, worker)
			return session != nil && session != previous
		})
	}
}

func TestRPCRequestThatNeverReachedAWorkerIsRetryable(t *testing.T) {
	registry := newRPCRegistry()
	if err := registry.declare("worker"); err != nil {
		t.Fatal(err)
	}
	session, err := newRPCSession(registry, "worker")
	if err != nil {
		t.Fatal(err)
	}
	if err := session.activate(); err != nil {
		t.Fatal(err)
	}
	worker, call, _, err := registry.reserve([]string{"worker"})
	if err != nil || worker == nil {
		t.Fatalf("reserve = %v, %v", worker, err)
	}
	// The instance exits between the reservation and the request.
	session.close(nil)
	_, retry, err := registry.invoke(t.Context(), worker, call, "ping")
	if !errors.Is(err, errRPCSessionClosed) {
		t.Fatalf("error = %v", err)
	}
	if !retry {
		t.Fatal("a request that never reached a worker must be retryable")
	}
	if err := session.activate(); !errors.Is(err, errRPCSessionClosed) {
		t.Fatalf("a closed session was republished: %v", err)
	}
}

func workerRegistered(registry *rpcRegistry, id string) bool {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	return registry.workers[id] != nil
}

func workerSession(registry *rpcRegistry, id string) *rpcSession {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if worker := registry.workers[id]; worker != nil {
		return worker.session
	}
	return nil
}

func waitForCondition(t *testing.T, what string, condition func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestProcessCallServesConcurrentCallersFromThePool(t *testing.T) {
	registry := newRPCRegistry()
	workers := []any{
		startRPCWorker(t, registry, "alpha"),
		startRPCWorker(t, registry, "beta"),
		startRPCWorker(t, registry, "gamma"),
	}
	call, err := newCall(map[string]any{"pool": "vars.workers", "payload_expr": "vars.payload"}, registry)
	if err != nil {
		t.Fatal(err)
	}
	const callers, each = 6, 5
	failures := make(chan error, callers*each)
	var group sync.WaitGroup
	for caller := range callers {
		group.Add(1)
		go func() {
			defer group.Done()
			for index := range each {
				// Every request carries a distinct payload, so a response routed to the wrong
				// caller shows up as a mismatch rather than passing unnoticed.
				payload := fmt.Sprintf("caller-%d-call-%d", caller, index)
				result, err := call.Run(t.Context(), step.Request{
					Vars: map[string]any{"workers": workers, "payload": payload},
				})
				if err != nil {
					failures <- err
					return
				}
				echoed, _ := result.Outputs["result"].(map[string]any)
				if echoed["payload"] != payload {
					failures <- fmt.Errorf("result = %#v, want the payload %q", result.Outputs["result"], payload)
				}
			}
		}()
	}
	group.Wait()
	close(failures)
	for err := range failures {
		t.Error(err)
	}
}

func TestProcessCallConfigurationValidation(t *testing.T) {
	registry := newRPCRegistry()
	tests := []struct {
		name string
		raw  map[string]any
		want string
	}{
		{name: "missing target", raw: map[string]any{}, want: "exactly one"},
		{name: "two targets", raw: map[string]any{"worker": "one", "pool": "vars.workers"}, want: "exactly one"},
		{name: "two payloads", raw: map[string]any{"worker": "one", "payload": nil, "payload_expr": "vars.payload"}, want: "cannot be combined"},
		{name: "invalid pool expression", raw: map[string]any{"pool": "vars."}, want: "compiling pool"},
		{name: "nonpositive timeout", raw: map[string]any{"worker": "one", "timeout": "0s"}, want: "positive"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := newCall(test.raw, registry)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestRPCRejectsUnknownResponseID(t *testing.T) {
	registry := newRPCRegistry()
	if err := registry.declare("worker"); err != nil {
		t.Fatal(err)
	}
	session, err := newRPCSession(registry, "worker")
	if err != nil {
		t.Fatal(err)
	}
	if err := session.activate(); err != nil {
		t.Fatal(err)
	}
	_, err = io.WriteString(session, `{"id":"unknown","result":null}`+"\n")
	if err == nil || !strings.Contains(err.Error(), "unknown id") {
		t.Fatalf("error = %v", err)
	}
	select {
	case protocolErr := <-session.protocol:
		if !strings.Contains(protocolErr.Error(), "unknown id") {
			t.Fatalf("protocol error = %v", protocolErr)
		}
	default:
		t.Fatal("protocol failure was not reported")
	}
}

func TestRPCTimeoutKeepsWorkerReservedUntilLateResponse(t *testing.T) {
	registry := newRPCRegistry()
	if err := registry.declare("worker"); err != nil {
		t.Fatal(err)
	}
	session, err := newRPCSession(registry, "worker")
	if err != nil {
		t.Fatal(err)
	}
	if err := session.activate(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { session.close(errRPCSessionClosed) })

	requestSeen := make(chan rpcRequest)
	respond := make(chan struct{})
	go func() {
		scanner := bufio.NewScanner(session.reader)
		if !scanner.Scan() {
			return
		}
		var request rpcRequest
		if json.Unmarshal(scanner.Bytes(), &request) != nil {
			return
		}
		requestSeen <- request
		<-respond
		response, _ := json.Marshal(map[string]any{"id": request.ID, "result": "late"})
		_, _ = session.Write(append(response, '\n'))
	}()

	ctx, cancel := context.WithCancel(t.Context())
	result := make(chan error, 1)
	go func() {
		_, _, err := registry.call(ctx, []string{"worker"}, "request")
		result <- err
	}()
	<-requestSeen
	cancel()
	if err := <-result; err == nil || !strings.Contains(err.Error(), "context canceled") {
		t.Fatalf("call error = %v", err)
	}
	worker, _, changed, err := registry.reserve([]string{"worker"})
	if err != nil {
		t.Fatal(err)
	}
	if worker != nil {
		t.Fatal("timed-out worker was released before its late response")
	}
	close(respond)
	select {
	case <-changed:
	case <-t.Context().Done():
		t.Fatal(t.Context().Err())
	}
	worker, call, _, err := registry.reserve([]string{"worker"})
	if err != nil {
		t.Fatal(err)
	}
	if worker == nil {
		t.Fatal("worker was not released after its late response")
	}
	registry.release(worker, call)
}

func startRPCWorker(t *testing.T, registry *rpcRegistry, name string) string {
	t.Helper()
	runnerValue, err := newProcess(map[string]any{
		"command": os.Args[0],
		"args":    []any{"-test.run=^TestRPCWorkerProcess$"},
		"env": map[string]any{
			"WUKO_RPC_TEST_HELPER": "1",
			"WUKO_RPC_TEST_WORKER": name,
		},
		"rpc":      "jsonl",
		"shutdown": map[string]any{"timeout": "100ms"},
	}, registry)
	if err != nil {
		t.Fatal(err)
	}
	services := newTestServices(t)
	result, err := runnerValue.Run(t.Context(), step.Request{
		StepID: name, Services: services, Env: map[string]string{}, Stdout: io.Discard, Stderr: io.Discard,
	})
	if err != nil {
		t.Fatal(err)
	}
	workerID, ok := result.Outputs["worker_id"].(string)
	if !ok || workerID == "" {
		t.Fatal(fmt.Errorf("worker_id output = %#v", result.Outputs["worker_id"]))
	}
	return workerID
}
