package process

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"strings"
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
