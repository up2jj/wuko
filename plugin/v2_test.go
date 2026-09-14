package plugin

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/up2jj/wuko/helper"
	"github.com/up2jj/wuko/secret"
	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/workflow"
)

func TestV2StepContextAndServiceSnapshot(t *testing.T) {
	current := "before"
	renderer := &snapshotTestRenderer{current: &current}
	request := step.Request{
		WorkflowSource: "workflow.yaml", WorkflowDirBorrowed: true, WorkflowTimezone: "Europe/Warsaw",
		EnvironmentLoaders: []string{"mise"}, PresetVars: map[string]any{"target": "before"},
		Vars: map[string]any{"target": "before"}, Bindings: map[string]any{"foreach": map[string]any{"index": 1}},
		TemplateRenderer: renderer,
	}
	if _, exists := stepContext(request, ProtocolV1)["workflow_source"]; exists {
		t.Fatal("v1 context was expanded")
	}
	v2 := stepContext(request, ProtocolV2)
	if v2["workflow_source"] != "workflow.yaml" || v2["workflow_timezone"] != "Europe/Warsaw" {
		t.Fatalf("v2 context = %#v", v2)
	}
	frozen := freezeStepRequest(request)
	request.Vars["target"] = "after"
	current = "after"
	if frozen.Vars["target"] != "before" {
		t.Fatalf("frozen vars = %#v", frozen.Vars)
	}
	value, err := frozen.TemplateRenderer.RenderContent("ignored")
	if err != nil || value != "before" {
		t.Fatalf("snapshot render = %q, %v", value, err)
	}
}

func TestV2HostTemplateSecretAndHelperCallbacks(t *testing.T) {
	current := "snapshot"
	runner := &pluginStep{declaration: stepDeclaration{Type: "http.mock_server", HostCallbacks: []string{
		"host.template.validate", "host.template.render", "host.function.call",
	}}}
	request := step.Request{
		TemplateRenderer: &snapshotTestRenderer{current: &current},
		Secret:           func(reference string) (string, error) { return "secret:" + reference, nil },
		Helpers: helper.Set{"tools_slug": func(_ context.Context, args []any) (any, error) {
			return strings.ToLower(args[0].(string)), nil
		}},
	}
	callback := runner.hostCallbacks(request)
	if _, err := callback(t.Context(), "host.template.validate", json.RawMessage(`{"parent_id":"1","content":"hello"}`)); err != nil {
		t.Fatal(err)
	}
	rendered, err := callback(t.Context(), "host.template.render", json.RawMessage(`{"parent_id":"1","content":"hello","extra":{"suffix":"!"}}`))
	if err != nil || rendered.(map[string]any)["value"] != "snapshot!" {
		t.Fatalf("render callback = %#v, %v", rendered, err)
	}
	secret, err := callback(t.Context(), "host.function.call", json.RawMessage(`{"parent_id":"1","name":"secret","args":["env://TOKEN"]}`))
	if err != nil || secret.(map[string]any)["value"] != "secret:env://TOKEN" {
		t.Fatalf("secret callback = %#v, %v", secret, err)
	}
	value, err := callback(t.Context(), "host.function.call", json.RawMessage(`{"parent_id":"1","name":"tools_slug","args":["HELLO"]}`))
	if err != nil || value.(map[string]any)["value"] != "hello" {
		t.Fatalf("helper callback = %#v, %v", value, err)
	}
}

func TestBarePluginIsAcceptedOnTheProtocolItAnswersWith(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	launches := filepath.Join(directory, "launches")
	script := []byte("#!/bin/sh\nprintf x >> \"" + launches + "\"\nWUKO_PLUGIN_TEST_HELPER=1 exec \"" + executable + "\"\n")
	path := filepath.Join(directory, "wuko-plugin-acme")
	if err := os.WriteFile(path, script, 0o700); err != nil {
		t.Fatal(err)
	}
	client, initialized, protocol, err := launchInitialized(t.Context(), path, "acme", "", "test", io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer closeClient(client)
	if protocol != ProtocolV1 || initialized.Protocol != ProtocolV1 {
		t.Fatalf("negotiated %q with %#v", protocol, initialized)
	}
	// The helper answers v1 to the v2 offer, which is the whole negotiation: shutting the
	// process down to relaunch it and be told the same thing is wasted work.
	data, err := os.ReadFile(launches)
	if err != nil || len(data) != 1 {
		t.Fatalf("plugin launched %d times, want 1 (%v)", len(data), err)
	}
	// A pinned protocol is not negotiable, so the same answer is a handshake failure there.
	pinned, _, _, err := launchInitialized(t.Context(), path, "acme", ProtocolV2, "test", io.Discard)
	if err == nil {
		closeClient(pinned)
		t.Fatal("expected a pinned v2 handshake to reject a v1 answer")
	}
}

func TestBidirectionalCallbacksAreScopedAndDoNotBlockReader(t *testing.T) {
	client, scanner, encoder := newPipeClient(t)
	pluginDone := make(chan error, 1)
	go func() {
		if !scanner.Scan() {
			pluginDone <- io.EOF
			return
		}
		var parent requestFrame
		_ = json.Unmarshal(scanner.Bytes(), &parent)
		for _, callback := range []requestFrame{
			{ID: "plugin-1", Method: "host.function.call", Params: map[string]any{"parent_id": parent.ID, "name": "one"}},
			{ID: "plugin-2", Method: "host.function.call", Params: map[string]any{"parent_id": parent.ID, "name": "two"}},
			{ID: "plugin-3", Method: "host.function.call", Params: map[string]any{"parent_id": "completed", "name": "bad"}},
		} {
			if err := encoder.Encode(callback); err != nil {
				pluginDone <- err
				return
			}
		}
		responses := make(map[string]responseFrame)
		for len(responses) < 3 && scanner.Scan() {
			var response responseFrame
			if err := json.Unmarshal(scanner.Bytes(), &response); err != nil {
				pluginDone <- err
				return
			}
			responses[response.ID] = response
		}
		if responses["plugin-1"].Error != nil || responses["plugin-2"].Error != nil {
			pluginDone <- fmt.Errorf("active callbacks failed: %#v", responses)
			return
		}
		if response := responses["plugin-3"]; response.Error == nil || response.Error.Code != "unknown_parent" {
			pluginDone <- fmt.Errorf("unknown parent response = %#v", response)
			return
		}
		result, _ := json.Marshal(map[string]any{"ok": true})
		pluginDone <- encoder.Encode(responseFrame{ID: parent.ID, Result: result})
	}()

	var mu sync.Mutex
	called := make(map[string]bool)
	host := func(_ context.Context, _ string, raw json.RawMessage) (any, error) {
		var params struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(raw, &params); err != nil {
			return nil, err
		}
		mu.Lock()
		called[params.Name] = true
		mu.Unlock()
		return map[string]any{"value": params.Name}, nil
	}
	var result map[string]any
	if err := client.callWithOptions(t.Context(), "step.run", map[string]any{}, &result, callOptions{host: host}); err != nil {
		t.Fatal(err)
	}
	if err := <-pluginDone; err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !called["one"] || !called["two"] {
		t.Fatalf("callback calls = %v", called)
	}
}

func TestCanceledV2CallDrainsVerificationError(t *testing.T) {
	client, scanner, encoder := newPipeClient(t)
	received := make(chan struct{})
	go func() {
		if !scanner.Scan() {
			return
		}
		var request requestFrame
		_ = json.Unmarshal(scanner.Bytes(), &request)
		close(received)
		if !scanner.Scan() {
			return
		}
		_ = encoder.Encode(responseFrame{ID: request.ID, Error: &wireError{Code: "verification_failed", Message: "mock verification failed"}})
	}()
	ctx, cancel := context.WithCancel(t.Context())
	go func() {
		<-received
		cancel()
	}()
	err := client.callWithOptions(ctx, "step.run", map[string]any{}, &struct{}{}, callOptions{drainCancel: time.Second})
	if err == nil || !strings.Contains(err.Error(), "mock verification failed") {
		t.Fatalf("drained error = %v", err)
	}
}

func TestCanceledCallStopsStreamingIntoCallerWriters(t *testing.T) {
	for attempt := 0; attempt < 10; attempt++ {
		client, scanner, encoder := newPipeClient(t)
		go func() {
			for scanner.Scan() {
				var request requestFrame
				if json.Unmarshal(scanner.Bytes(), &request) != nil || request.Method != "step.run" {
					continue
				}
				go func(id string) {
					for i := 0; i < 200; i++ {
						if encoder.Encode(eventFrame{ID: id, Event: "stdout", Data: base64.StdEncoding.EncodeToString([]byte("x"))}) != nil {
							return
						}
					}
				}(request.ID)
			}
		}()
		writer := &releasableWriter{}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		_ = client.call(ctx, "step.run", map[string]any{}, &struct{}{}, streamEvents(writer, nil, nil))
		// The caller owns the writer again the moment call returns.
		writer.released.Store(true)
		time.Sleep(30 * time.Millisecond)
		cancel()
		if late := writer.late.Load(); late != 0 {
			t.Fatalf("attempt %d wrote %d events after the canceled call returned", attempt, late)
		}
	}
}

type releasableWriter struct {
	released atomic.Bool
	late     atomic.Int64
}

func (w *releasableWriter) Write(data []byte) (int, error) {
	time.Sleep(time.Millisecond)
	if w.released.Load() {
		w.late.Add(1)
	}
	return len(data), nil
}

func TestV2RejectsMalformedReverseCallAndOversizedRequest(t *testing.T) {
	t.Run("malformed reverse call", func(t *testing.T) {
		client, scanner, encoder := newPipeClient(t)
		go func() {
			if !scanner.Scan() {
				return
			}
			var parent requestFrame
			_ = json.Unmarshal(scanner.Bytes(), &parent)
			_ = encoder.Encode(map[string]any{
				"id": "plugin-1", "method": "host.function.call",
				"params": map[string]any{"parent_id": parent.ID}, "unexpected": true,
			})
		}()
		err := client.callWithOptions(t.Context(), "step.run", map[string]any{}, &struct{}{}, callOptions{host: func(context.Context, string, json.RawMessage) (any, error) {
			return struct{}{}, nil
		}})
		if err == nil || !strings.Contains(err.Error(), "malformed plugin host request") {
			t.Fatalf("malformed frame error = %v", err)
		}
	})
	t.Run("oversized host request", func(t *testing.T) {
		client, _, _ := newPipeClient(t)
		err := client.call(t.Context(), "step.run", map[string]any{"value": strings.Repeat("x", maxFrameSize)}, &struct{}{}, nil)
		if err == nil || !strings.Contains(err.Error(), "exceeds 10 MiB") {
			t.Fatalf("frame limit error = %v", err)
		}
	})
}

func TestV2ServiceReadinessAndPostReadyFailure(t *testing.T) {
	client, scanner, encoder := newPipeClient(t)
	go func() {
		for scanner.Scan() {
			var request requestFrame
			_ = json.Unmarshal(scanner.Bytes(), &request)
			switch request.Method {
			case "step.service":
				result, _ := json.Marshal(serviceOptionsResult{Kind: "mock_server", KeepAlive: true, FailFast: true, ExitOnEnd: true})
				_ = encoder.Encode(responseFrame{ID: request.ID, Result: result})
			case "step.run":
				ready, _ := json.Marshal(step.Result{Outputs: map[string]any{"ready": true, "url": "http://127.0.0.1:43125"}})
				_ = encoder.Encode(eventFrame{ID: request.ID, Event: "ready", Result: ready})
				_ = encoder.Encode(eventFrame{ID: request.ID, Event: "stdout", Data: base64.StdEncoding.EncodeToString([]byte("after-ready"))})
			case "cancel":
				params := request.Params.(map[string]any)
				_ = encoder.Encode(responseFrame{ID: params["id"].(string), Error: &wireError{Message: "verification failed"}})
				return
			}
		}
	}()
	launcher := newTestServiceLauncher()
	var stdout synchronizedBuffer
	runner := &pluginStep{
		plugin:      &runningPlugin{protocol: ProtocolV2, client: client},
		declaration: stepDeclaration{Type: "http.mock_server", Service: true},
		name:        "http.mock_server", raw: map[string]any{},
	}
	result, err := runner.Run(t.Context(), step.Request{StepID: "mock", Services: launcher, Stdout: &stdout})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outputs["url"] != "http://127.0.0.1:43125" || !launcher.options.KeepAlive || !launcher.options.FailFast || !launcher.options.ExitOnEnd {
		t.Fatalf("result = %#v, options = %#v", result, launcher.options)
	}
	deadline := time.Now().Add(time.Second)
	for stdout.String() != "after-ready" && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if stdout.String() != "after-ready" {
		t.Fatalf("late service output = %q", stdout.String())
	}
	launcher.cancel()
	if err := <-launcher.done; err == nil || !strings.Contains(err.Error(), "verification failed") {
		t.Fatalf("background error = %v", err)
	}
}

func TestV2ServiceFailureBeforeReadyAbortsOwningStep(t *testing.T) {
	client, scanner, encoder := newPipeClient(t)
	go func() {
		for scanner.Scan() {
			var request requestFrame
			_ = json.Unmarshal(scanner.Bytes(), &request)
			switch request.Method {
			case "step.service":
				result, _ := json.Marshal(serviceOptionsResult{Kind: "forward_proxy", FailFast: true})
				_ = encoder.Encode(responseFrame{ID: request.ID, Result: result})
			case "step.run":
				_ = encoder.Encode(responseFrame{ID: request.ID, Error: &wireError{Message: "listen failed"}})
				return
			}
		}
	}()
	launcher := newTestServiceLauncher()
	runner := &pluginStep{plugin: &runningPlugin{protocol: ProtocolV2, client: client}, declaration: stepDeclaration{Service: true}, name: "http.forward_proxy"}
	_, err := runner.Run(t.Context(), step.Request{StepID: "proxy", Services: launcher})
	if err == nil || !strings.Contains(err.Error(), "listen failed") {
		t.Fatalf("owning step error = %v", err)
	}
	if backgroundErr := <-launcher.done; !errors.Is(backgroundErr, step.ErrServiceAborted) {
		t.Fatalf("background error = %v", backgroundErr)
	}
}

type testServiceLauncher struct {
	ctx     context.Context
	cancel  context.CancelFunc
	done    chan error
	options step.ServiceOptions
}

func newTestServiceLauncher() *testServiceLauncher {
	ctx, cancel := context.WithCancel(context.Background())
	return &testServiceLauncher{ctx: ctx, cancel: cancel, done: make(chan error, 1)}
}

func (launcher *testServiceLauncher) StartService(_ string, _ string, options step.ServiceOptions, run func(context.Context) error) error {
	launcher.options = options
	go func() { launcher.done <- run(launcher.ctx) }()
	return nil
}

func newPipeClient(t *testing.T) (*client, *bufio.Scanner, *json.Encoder) {
	t.Helper()
	pluginInput, hostInput := io.Pipe()
	hostOutput, pluginOutput := io.Pipe()
	client := newClient(nil, hostInput)
	go client.read(hostOutput)
	t.Cleanup(func() {
		_ = hostInput.Close()
		_ = pluginOutput.Close()
		_ = pluginInput.Close()
		_ = hostOutput.Close()
	})
	return client, bufio.NewScanner(pluginInput), json.NewEncoder(pluginOutput)
}

type snapshotTestRenderer struct {
	current *string
	frozen  string
}

func (renderer *snapshotTestRenderer) Validate(string) error        { return nil }
func (renderer *snapshotTestRenderer) ValidateContent(string) error { return nil }
func (renderer *snapshotTestRenderer) Render(string) (string, error) {
	return renderer.RenderContent("")
}
func (renderer *snapshotTestRenderer) RenderContent(string) (string, error) {
	if renderer.current != nil {
		return *renderer.current, nil
	}
	return renderer.frozen, nil
}
func (renderer *snapshotTestRenderer) RenderWith(_ string, extra map[string]any) (string, error) {
	return renderer.RenderContentWith("", extra)
}
func (renderer *snapshotTestRenderer) RenderContentWith(_ string, extra map[string]any) (string, error) {
	value, _ := renderer.RenderContent("")
	suffix, _ := extra["suffix"].(string)
	return value + suffix, nil
}
func (renderer *snapshotTestRenderer) Snapshot() step.DataTemplateRenderer {
	value, _ := renderer.RenderContent("")
	return &snapshotTestRenderer{frozen: value}
}
func (renderer *snapshotTestRenderer) WithoutSecrets() step.DataTemplateRenderer {
	return renderer
}

func TestTemplateRenderCallbackCannotReachSecretsOnItsOwn(t *testing.T) {
	session := secret.NewSession(t.Context(), secret.Options{Runner: stubSecretRunner{}, BaseEnv: map[string]string{}})
	renderer, err := workflow.NewRendererWithSecrets(nil, session)
	if err != nil {
		t.Fatal(err)
	}
	request := step.Request{TemplateRenderer: &workflowTestRenderer{renderer: renderer, data: map[string]any{}}}
	params, err := json.Marshal(map[string]any{"parent_id": "1", "content": `{{ secret "op://Production/API/token" }}`})
	if err != nil {
		t.Fatal(err)
	}
	limited := (&pluginStep{declaration: stepDeclaration{Type: "acme.render", HostCallbacks: []string{"host.template.render"}}}).hostCallbacks(request)
	if _, err := limited(t.Context(), "host.template.render", params); err == nil || !strings.Contains(err.Error(), "secret is unavailable") {
		t.Fatalf("render for a step without host.function.call = %v", err)
	}
	// Declaring host.function.call already grants secret access directly, so denying it inside
	// templates for the same step would restrict nothing.
	full := (&pluginStep{declaration: stepDeclaration{Type: "acme.render", HostCallbacks: []string{"host.template.render", "host.function.call"}}}).hostCallbacks(request)
	value, err := full(t.Context(), "host.template.render", params)
	if err != nil || value.(map[string]any)["value"] != "workflow-token" {
		t.Fatalf("render for a step with host.function.call = %#v, %v", value, err)
	}
}

func TestHostCallbacksAreRefusedBeyondTheConcurrencyLimit(t *testing.T) {
	const overflow = 8
	const total = maxConcurrentHostCalls + overflow
	client, scanner, encoder := newPipeClient(t)
	release := make(chan struct{})
	accepted := make(chan struct{}, total)
	host := func(context.Context, string, json.RawMessage) (any, error) {
		accepted <- struct{}{}
		<-release
		return map[string]any{"value": "ok"}, nil
	}
	pluginDone := make(chan error, 1)
	go func() {
		if !scanner.Scan() {
			pluginDone <- io.EOF
			return
		}
		var parent requestFrame
		_ = json.Unmarshal(scanner.Bytes(), &parent)
		go func() {
			for i := 0; i < total; i++ {
				if encoder.Encode(requestFrame{ID: fmt.Sprintf("cb-%d", i), Method: "host.function.call", Params: map[string]any{"parent_id": parent.ID, "name": "blocked"}}) != nil {
					return
				}
			}
		}()
		refused, answered := 0, 0
		for answered < total && scanner.Scan() {
			var response responseFrame
			if err := json.Unmarshal(scanner.Bytes(), &response); err != nil {
				pluginDone <- err
				return
			}
			answered++
			if response.Error != nil {
				if response.Error.Code != "too_many_callbacks" {
					pluginDone <- fmt.Errorf("callback %s failed with %#v", response.ID, response.Error)
					return
				}
				refused++
				if refused == overflow {
					// Every refusal arrived while the accepted callbacks were still running,
					// which is the backpressure the limit exists to apply.
					close(release)
				}
			}
		}
		if refused != overflow {
			pluginDone <- fmt.Errorf("refused %d callbacks, want %d", refused, overflow)
			return
		}
		result, _ := json.Marshal(map[string]any{"ok": true})
		pluginDone <- encoder.Encode(responseFrame{ID: parent.ID, Result: result})
	}()
	var result map[string]any
	if err := client.callWithOptions(t.Context(), "step.run", map[string]any{}, &result, callOptions{host: host}); err != nil {
		t.Fatal(err)
	}
	if err := <-pluginDone; err != nil {
		t.Fatal(err)
	}
	if len(accepted) != maxConcurrentHostCalls {
		t.Fatalf("%d callbacks ran concurrently, want %d", len(accepted), maxConcurrentHostCalls)
	}
	// The last accepted callbacks release their budget after writing their results, so the
	// parent response can arrive just before the final release.
	deadline := time.Now().Add(2 * time.Second)
	for {
		client.hostMu.Lock()
		calls, bytes := client.hostCalls, client.hostBytes
		client.hostMu.Unlock()
		if calls == 0 && bytes == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("budget leaked: %d calls, %d bytes", calls, bytes)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestCanceledStartIsRetriedButAFailedStartIsNot(t *testing.T) {
	client, scanner, encoder := newPipeClient(t)
	answer := make(chan responseFrame, 4)
	go func() {
		for scanner.Scan() {
			var request requestFrame
			if json.Unmarshal(scanner.Bytes(), &request) != nil || request.Method != "plugin.start" {
				continue
			}
			select {
			case frame := <-answer:
				frame.ID = request.ID
				_ = encoder.Encode(frame)
			default:
			}
		}
	}()
	plugin := &runningPlugin{namespace: "acme", protocol: ProtocolV2, client: client, initialized: initializeResult{Lifecycle: true}}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if err := plugin.start(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled start = %v", err)
	}
	// The cleanup scope runs with cancellation stripped and must still be able to start the
	// plugin it has to stop.
	answer <- responseFrame{Result: json.RawMessage(`{}`)}
	if err := plugin.start(t.Context()); err != nil {
		t.Fatalf("retried start = %v", err)
	}

	failing, failingScanner, failingEncoder := newPipeClient(t)
	attempts := 0
	go func() {
		for failingScanner.Scan() {
			var request requestFrame
			if json.Unmarshal(failingScanner.Bytes(), &request) != nil || request.Method != "plugin.start" {
				continue
			}
			attempts++
			_ = failingEncoder.Encode(responseFrame{ID: request.ID, Error: &wireError{Message: "start refused"}})
		}
	}()
	broken := &runningPlugin{namespace: "acme", protocol: ProtocolV2, client: failing, initialized: initializeResult{Lifecycle: true}}
	for range 2 {
		if err := broken.start(t.Context()); err == nil || err.Error() != "start refused" {
			t.Fatalf("failed start = %v", err)
		}
	}
	if attempts != 1 {
		t.Fatalf("plugin.start was retried %d times after a real failure", attempts)
	}
}

type stubSecretRunner struct{}

func (stubSecretRunner) Run(_ context.Context, command secret.Command) (string, error) {
	if command.Name == "op" {
		return "workflow-token", nil
	}
	return "", fmt.Errorf("unexpected secret command %q", command.Name)
}

type workflowTestRenderer struct {
	renderer *workflow.Renderer
	data     map[string]any
}

func (r *workflowTestRenderer) Validate(value string) error { return r.renderer.Validate(value) }
func (r *workflowTestRenderer) Render(value string) (string, error) {
	return r.renderer.Render(value, r.data)
}
func (r *workflowTestRenderer) ValidateContent(value string) error {
	return r.renderer.ValidateUncached(value)
}
func (r *workflowTestRenderer) RenderContent(value string) (string, error) {
	return r.renderer.RenderUncached(value, r.data)
}
func (r *workflowTestRenderer) RenderWith(value string, extra map[string]any) (string, error) {
	return r.renderer.Render(value, r.overlay(extra))
}
func (r *workflowTestRenderer) RenderContentWith(value string, extra map[string]any) (string, error) {
	return r.renderer.RenderUncached(value, r.overlay(extra))
}
func (r *workflowTestRenderer) Snapshot() step.DataTemplateRenderer { return r }
func (r *workflowTestRenderer) WithoutSecrets() step.DataTemplateRenderer {
	return &workflowTestRenderer{renderer: r.renderer.WithoutSecrets(), data: r.data}
}
func (r *workflowTestRenderer) overlay(extra map[string]any) map[string]any {
	data := maps.Clone(r.data)
	maps.Copy(data, extra)
	return data
}
