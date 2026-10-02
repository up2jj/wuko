package sdktest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/up2jj/wuko/plugin/sdk"
)

type testConfig struct {
	Name string `json:"name"`
}

func TestHarnessFramesEventsCallbacksAndCancellation(t *testing.T) {
	plugin, err := sdk.New("acme")
	if err != nil {
		t.Fatal(err)
	}
	var validates atomic.Int32
	var renders atomic.Int32
	var functions atomic.Int32
	var cleanups atomic.Int32
	handler := sdk.StepHandler[testConfig]{
		Run: func(ctx context.Context, request sdk.StepRequest[testConfig]) (sdk.Result, error) {
			if _, err := request.Stdout.Write([]byte("started\n")); err != nil {
				return sdk.Result{}, err
			}
			if err := request.Host.ValidateTemplate(ctx, "hello"); err != nil {
				return sdk.Result{}, err
			}
			rendered, err := request.Host.RenderTemplate(ctx, "hello {{ .name }}", map[string]any{"name": request.Config.Name})
			if err != nil {
				return sdk.Result{}, err
			}
			value, err := request.Host.CallFunction(ctx, "decorate", []any{request.Config.Name})
			if err != nil {
				return sdk.Result{}, err
			}
			ready := sdk.Result{Outputs: map[string]any{"rendered": rendered, "value": value, "count": 9007199254740991}}
			if err := request.Ready(ready); err != nil {
				return sdk.Result{}, err
			}
			<-ctx.Done()
			return sdk.Result{Outputs: map[string]any{"stopped": true}}, nil
		},
		Service: func(context.Context, sdk.StepRequest[testConfig]) (sdk.Service, error) {
			return sdk.Service{Kind: "server", KeepAlive: true, FailFast: true}, nil
		},
		Cleanup: func(context.Context, sdk.CleanupRequest[testConfig]) error {
			cleanups.Add(1)
			return nil
		},
	}
	if err := sdk.RegisterStep(plugin, "server", handler,
		sdk.WithHostCallbacks(sdk.HostTemplateValidate, sdk.HostTemplateRender, sdk.HostFunctionCall)); err != nil {
		t.Fatal(err)
	}
	host := Start(t, plugin, WithCallbacks(Callbacks{
		ValidateTemplate: func(_ context.Context, content string) error {
			validates.Add(1)
			if content != "hello" {
				return fmt.Errorf("unexpected content %q", content)
			}
			return nil
		},
		RenderTemplate: func(_ context.Context, content string, extra map[string]any) (string, error) {
			renders.Add(1)
			return strings.ReplaceAll(content, "{{ .name }}", fmt.Sprint(extra["name"])), nil
		},
		CallFunction: func(_ context.Context, name string, args []any) (any, error) {
			functions.Add(1)
			if name != "decorate" {
				return nil, fmt.Errorf("unexpected function %q", name)
			}
			return fmt.Sprint(args[0]) + "!", nil
		},
	}))

	initialized := host.Initialization()
	if initialized.Protocol != sdk.Protocol || initialized.Namespace != "acme" || len(initialized.Steps) != 1 {
		t.Fatalf("initialization = %+v", initialized)
	}
	if got := initialized.Steps[0].HostCallbacks; len(got) != 3 {
		t.Fatalf("host callbacks = %v", got)
	}

	serviceCall := host.CallStep(StepRequest{Operation: StepService, Type: "acme.server"})
	var service sdk.Service
	if err := serviceCall.Await().DecodeResult(&service); err != nil {
		t.Fatal(err)
	}
	if service.Kind != "server" || !service.KeepAlive || !service.FailFast {
		t.Fatalf("service = %+v", service)
	}
	cleanup := host.CallStep(StepRequest{
		Operation: StepCleanup,
		Type:      "acme.server",
		With:      map[string]any{"name": "world"},
		Result:    sdk.Result{Outputs: map[string]any{"started": true}},
	}).Await()
	if cleanup.Error != nil || cleanups.Load() != 1 {
		t.Fatalf("cleanup = %+v, calls = %d", cleanup, cleanups.Load())
	}

	call := host.CallStep(StepRequest{Type: "acme.server", With: map[string]any{"name": "world"}})
	ready := call.AwaitEvent("ready")
	stdout := call.AwaitEvent("stdout")
	if string(stdout.Data) != "started\n" {
		t.Fatalf("stdout = %q", stdout.Data)
	}
	var readyResult sdk.Result
	if err := ready.DecodeResult(&readyResult); err != nil {
		t.Fatal(err)
	}
	if readyResult.Outputs["rendered"] != "hello world" || readyResult.Outputs["value"] != "world!" {
		t.Fatalf("ready result = %#v", readyResult)
	}
	if _, ok := readyResult.Outputs["count"].(json.Number); !ok {
		t.Fatalf("count type = %T, want json.Number", readyResult.Outputs["count"])
	}
	call.Cancel()
	call.Cancel()
	var final sdk.Result
	if err := call.Await().DecodeResult(&final); err != nil {
		t.Fatal(err)
	}
	if final.Outputs["stopped"] != true {
		t.Fatalf("final result = %#v", final)
	}
	if validates.Load() != 1 || renders.Load() != 1 || functions.Load() != 1 {
		t.Fatalf("callback calls = validate:%d render:%d function:%d", validates.Load(), renders.Load(), functions.Load())
	}
}

func TestHarnessDemultiplexesConcurrentCallsAndCallbacks(t *testing.T) {
	plugin, err := sdk.New("acme")
	if err != nil {
		t.Fatal(err)
	}
	releases := map[string]chan struct{}{"first": make(chan struct{}), "second": make(chan struct{})}
	started := make(chan string, 2)
	if err := sdk.RegisterStep(plugin, "work", sdk.StepHandler[testConfig]{
		Run: func(ctx context.Context, request sdk.StepRequest[testConfig]) (sdk.Result, error) {
			started <- request.Config.Name
			value, err := request.Host.CallFunction(ctx, "identity", []any{request.Config.Name})
			if err != nil {
				return sdk.Result{}, err
			}
			select {
			case <-releases[request.Config.Name]:
				return sdk.Result{Outputs: map[string]any{"value": value}}, nil
			case <-ctx.Done():
				return sdk.Result{}, sdk.Coded("canceled", ctx.Err())
			}
		},
	}, sdk.WithHostCallbacks(sdk.HostFunctionCall)); err != nil {
		t.Fatal(err)
	}
	host := Start(t, plugin, WithCallbacks(Callbacks{CallFunction: func(_ context.Context, _ string, args []any) (any, error) {
		return args[0], nil
	}}))
	first := host.CallStep(StepRequest{Type: "acme.work", With: map[string]any{"name": "first"}})
	second := host.CallStep(StepRequest{Type: "acme.work", With: map[string]any{"name": "second"}})
	if first.ID() == second.ID() {
		t.Fatalf("duplicate request id %q", first.ID())
	}
	for range 2 {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("step did not start")
		}
	}
	close(releases["second"])
	var secondResult sdk.Result
	if err := second.Await().DecodeResult(&secondResult); err != nil {
		t.Fatal(err)
	}
	if secondResult.Outputs["value"] != "second" {
		t.Fatalf("second result = %#v", secondResult)
	}
	first.Cancel()
	response := first.Await()
	if response.Error == nil || response.Error.Code != "canceled" {
		t.Fatalf("first response error = %+v", response.Error)
	}
}

func TestHarnessReturnsStepAndCallbackErrors(t *testing.T) {
	plugin, err := sdk.New("acme")
	if err != nil {
		t.Fatal(err)
	}
	if err := sdk.RegisterStep(plugin, "strict", sdk.StepHandler[testConfig]{
		Validate: func(context.Context, sdk.StepRequest[testConfig]) error {
			return sdk.Coded("invalid", errors.New("configuration rejected"))
		},
		Run: func(ctx context.Context, request sdk.StepRequest[testConfig]) (sdk.Result, error) {
			_, err := request.Host.CallFunction(ctx, "missing", nil)
			return sdk.Result{}, sdk.Coded("step_failed", err)
		},
	}, sdk.WithHostCallbacks(sdk.HostFunctionCall)); err != nil {
		t.Fatal(err)
	}
	host := Start(t, plugin)
	validation := host.CallStep(StepRequest{Operation: StepValidate, Type: "acme.strict"}).Await()
	if validation.Error == nil || validation.Error.Code != "invalid" || validation.Error.Message != "configuration rejected" {
		t.Fatalf("validation error = %+v", validation.Error)
	}
	response := host.CallStep(StepRequest{Type: "acme.strict"}).Await()
	if response.Error == nil || response.Error.Code != "step_failed" || !strings.Contains(response.Error.Message, "not configured") {
		t.Fatalf("callback error = %+v", response.Error)
	}
	if err := response.DecodeResult(&sdk.Result{}); err != response.Error {
		t.Fatalf("DecodeResult error = %v, want response error", err)
	}
}

func TestHarnessHandlesConcurrentCallbackResponses(t *testing.T) {
	plugin, err := sdk.New("acme")
	if err != nil {
		t.Fatal(err)
	}
	const callbacks = 32
	if err := sdk.RegisterStep(plugin, "parallel", sdk.StepHandler[struct{}]{
		Run: func(ctx context.Context, request sdk.StepRequest[struct{}]) (sdk.Result, error) {
			var wait sync.WaitGroup
			callbackErrors := make(chan error, callbacks)
			for index := range callbacks {
				wait.Go(func() {
					value, err := request.Host.CallFunction(ctx, "value", []any{index})
					if err != nil {
						callbackErrors <- err
						return
					}
					if value == nil {
						callbackErrors <- errors.New("nil callback value")
					}
				})
			}
			wait.Wait()
			close(callbackErrors)
			if err := <-callbackErrors; err != nil {
				return sdk.Result{}, err
			}
			return sdk.Result{}, nil
		},
	}, sdk.WithHostCallbacks(sdk.HostFunctionCall)); err != nil {
		t.Fatal(err)
	}
	var called atomic.Int32
	host := Start(t, plugin, WithCallbacks(Callbacks{CallFunction: func(_ context.Context, _ string, args []any) (any, error) {
		called.Add(1)
		return args[0], nil
	}}))
	if response := host.CallStep(StepRequest{Type: "acme.parallel"}).Await(); response.Error != nil {
		t.Fatal(response.Error)
	}
	if called.Load() != callbacks {
		t.Fatalf("callback calls = %d, want %d", called.Load(), callbacks)
	}
}

func TestHarnessCloseCancelsAndAwaitsActiveCalls(t *testing.T) {
	plugin, err := sdk.New("acme")
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	stopped := make(chan struct{})
	if err := sdk.RegisterStep(plugin, "wait", sdk.StepHandler[struct{}]{Run: func(ctx context.Context, _ sdk.StepRequest[struct{}]) (sdk.Result, error) {
		close(started)
		<-ctx.Done()
		close(stopped)
		return sdk.Result{}, nil
	}}); err != nil {
		t.Fatal(err)
	}
	host := Start(t, plugin)
	_ = host.CallStep(StepRequest{Type: "acme.wait"})
	<-started
	host.Close()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("active call was not stopped")
	}
	host.Close()
}

func TestHarnessAutomaticCleanupStopsPlugin(t *testing.T) {
	stopped := make(chan struct{})
	t.Run("child", func(t *testing.T) {
		plugin, err := sdk.New("acme")
		if err != nil {
			t.Fatal(err)
		}
		started := make(chan struct{})
		if err := sdk.RegisterStep(plugin, "wait", sdk.StepHandler[struct{}]{Run: func(ctx context.Context, _ sdk.StepRequest[struct{}]) (sdk.Result, error) {
			close(started)
			<-ctx.Done()
			close(stopped)
			return sdk.Result{}, nil
		}}); err != nil {
			t.Fatal(err)
		}
		host := Start(t, plugin)
		_ = host.CallStep(StepRequest{Type: "acme.wait"})
		<-started
	})
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("test cleanup did not stop plugin")
	}
}

func TestReadReleasesPluginWritesWhenRoutingFails(t *testing.T) {
	reader, writer := io.Pipe()
	harness := &Harness{calls: make(map[string]*Call), output: reader, readDone: make(chan struct{})}
	harness.ctx, harness.cancel = context.WithCancel(context.Background())
	defer harness.cancel()
	go harness.read()
	if _, err := writer.Write([]byte(`{"id":"missing","result":{}}` + "\n")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-harness.readDone:
	case <-time.After(time.Second):
		t.Fatal("reader did not stop on a routing failure")
	}
	// Without the read end closed the plugin would block on this write forever, leaking
	// its serve loop and making Close wait out every one of its timeouts.
	done := make(chan error, 1)
	go func() {
		_, err := writer.Write(make([]byte, 1<<20))
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "unknown request id") {
			t.Fatalf("plugin write error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("plugin write blocked after the reader stopped")
	}
}

func TestWaitCallbacksRacesConcurrentCallbackStarts(t *testing.T) {
	reader, writer := io.Pipe()
	defer reader.Close()
	go func() { _, _ = io.Copy(io.Discard, reader) }()
	harness := &Harness{timeout: 10 * time.Second, calls: make(map[string]*Call), input: writer}
	harness.ctx, harness.cancel = context.WithCancel(context.Background())
	defer harness.cancel()
	request := callbackRequest{ID: "cb", Method: string(sdk.HostFunctionCall), Params: json.RawMessage(`{"parent_id":"gone"}`)}
	var starting sync.WaitGroup
	starting.Go(func() {
		for range 2000 {
			harness.startCallback(request)
		}
	})
	// A WaitGroup cannot carry this: the reader keeps starting handlers while close waits,
	// and an Add that races a returning Wait panics and kills the whole test binary.
	for range 2000 {
		if err := harness.waitCallbacks(time.Now().Add(harness.timeout)); err != nil {
			t.Fatal(err)
		}
	}
	starting.Wait()
	if err := harness.waitCallbacks(time.Now().Add(harness.timeout)); err != nil {
		t.Fatal(err)
	}
	harness.mu.Lock()
	active := harness.callbacksActive
	harness.mu.Unlock()
	if active != 0 {
		t.Fatalf("callbacksActive = %d, want 0", active)
	}
}

func TestCallWaitResponseTimesOut(t *testing.T) {
	call := &Call{changed: make(chan struct{})}
	_, err := call.waitResponse(time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("waitResponse error = %v", err)
	}
}

func TestDecodeResultRejectsTrailingJSON(t *testing.T) {
	var result any
	err := decodeResult([]byte(`{} {}`), &result)
	if err == nil || !strings.Contains(err.Error(), "trailing") {
		t.Fatalf("decodeResult error = %v", err)
	}
}

func TestRouteRejectsMalformedAndUnknownFrames(t *testing.T) {
	harness := &Harness{calls: make(map[string]*Call)}
	for _, frame := range []string{
		`not-json`,
		`{"id":"missing","result":{}}`,
		`{"id":"missing","event":"stdout","data":"eA=="}`,
	} {
		if err := harness.route([]byte(frame)); err == nil {
			t.Fatalf("route(%q) succeeded", frame)
		}
	}
}

func TestWithTimeoutRejectsNonPositiveDurations(t *testing.T) {
	for _, timeout := range []time.Duration{0, -time.Second} {
		if err := WithTimeout(timeout)(&options{}); err == nil {
			t.Fatalf("WithTimeout(%s) succeeded", timeout)
		}
	}
}

func TestWriteRejectsOversizedFrame(t *testing.T) {
	harness := &Harness{}
	err := harness.write(map[string]any{"value": strings.Repeat("x", maxProtocolFrame)})
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("write error = %v", err)
	}
}
