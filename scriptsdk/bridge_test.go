package scriptsdk

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/up2jj/wuko/helper"
	"github.com/up2jj/wuko/step"
)

func openBridge(t *testing.T, request step.Request) *Bridge {
	t.Helper()
	b, err := Open(t.Context(), request, map[string]any{"count": 2})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := b.Close(); err != nil {
			t.Error(err)
		}
	})
	return b
}

// This client uses only the published HTTP/JSON protocol, with no Node adapter.
func bridgeClient(t *testing.T, b *Bridge) *http.Client {
	t.Helper()
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", b.descriptor.Socket)
	}}
	t.Cleanup(transport.CloseIdleConnections)
	return &http.Client{Transport: transport}
}

func postCall(ctx context.Context, client *http.Client, token, body string) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://wuko/v1/call", strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	return client.Do(request)
}

func callBridge(t *testing.T, b *Bridge, method string, params any) callResponse {
	t.Helper()
	body, err := json.Marshal(map[string]any{"version": Version, "method": method, "params": params})
	if err != nil {
		t.Fatal(err)
	}
	response, err := postCall(t.Context(), bridgeClient(t, b), b.descriptor.Token, string(body))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	var result callResponse
	if err := decode(data, &result); err != nil {
		t.Fatalf("decode %s: %v", data, err)
	}
	return result
}

func TestBridgeProtocolPreservesValuesAndStagesResults(t *testing.T) {
	b := openBridge(t, step.Request{WorkflowName: "test", Vars: map[string]any{"original": true}})
	contextResult := callBridge(t, b, "context", map[string]any{})
	if contextResult.Error != nil {
		t.Fatal(contextResult.Error)
	}
	snapshot := contextResult.Result.(map[string]any)
	if snapshot["args"].(map[string]any)["count"] != json.Number("2") {
		t.Fatalf("context = %#v", snapshot)
	}
	if _, leaked := snapshot["token"]; leaked {
		t.Fatal("context leaked credentials")
	}
	for _, method := range []string{"output", "set_var"} {
		result := callBridge(t, b, method, map[string]any{"name": "large", "value": json.Number("9007199254740993")})
		if result.Error != nil {
			t.Fatal(result.Error)
		}
	}
	result := b.Result()
	if result.Outputs["large"] != int64(9007199254740993) || result.Variables["large"] != int64(9007199254740993) {
		t.Fatalf("result = %#v", result)
	}
	result.Outputs["large"] = "changed"
	if b.Result().Outputs["large"] == "changed" {
		t.Fatal("result is not isolated")
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(b.dir); !os.IsNotExist(err) {
		t.Fatalf("bootstrap resources remain: %v", err)
	}
}

func TestBridgeHelpersAndErrors(t *testing.T) {
	b := openBridge(t, step.Request{
		Secret: func(reference string) (string, error) { return "resolved:" + reference, nil },
		Helpers: helper.Set{
			"custom": func(_ context.Context, args []any) (any, error) { return args, nil },
			"broken": func(context.Context, []any) (any, error) { panic("private panic contents") },
		},
	})
	for _, test := range []struct {
		name, method string
		params       any
		want         string
	}{
		{"builtin", "helper", map[string]any{"name": "upper", "args": []any{"hello"}}, `"HELLO"`},
		{"typed helper", "helper", map[string]any{"name": "list", "args": []any{true, 3, nil}}, `[true,3,null]`},
		{"numeric helper", "helper", map[string]any{"name": "repeat", "args": []any{3, "x"}}, `"xxx"`},
		{"plugin", "helper", map[string]any{"name": "custom", "args": []any{map[string]any{"ok": true}}}, `[{"ok":true}]`},
		{"secret", "secret", map[string]any{"reference": "test:token"}, `"resolved:test:token"`},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := callBridge(t, b, test.method, test.params)
			if result.Error != nil {
				t.Fatal(result.Error)
			}
			data, _ := json.Marshal(result.Result)
			if string(data) != test.want {
				t.Fatalf("result = %s, want %s", data, test.want)
			}
		})
	}
	for _, test := range []struct {
		method string
		params any
	}{
		{"unknown", map[string]any{}},
		{"output", map[string]any{"name": "missing"}},
		{"set_var", map[string]any{"name": " ", "value": false}},
		{"output", map[string]any{"name": "extra", "value": 1, "extra": true}},
		{"helper", map[string]any{"name": "upper", "args": []any{true}}},
		{"helper", map[string]any{"name": "secret", "args": []any{"x"}}},
		{"helper", map[string]any{"name": "broken"}},
		{"render", map[string]any{"template": "hello"}},
		{"step", map[string]any{"type": "shell"}},
	} {
		t.Run(test.method+" error", func(t *testing.T) {
			result := callBridge(t, b, test.method, test.params)
			if result.Error == nil || result.Error.Code == "" {
				t.Fatalf("expected structured error: %#v", result)
			}
			if strings.Contains(result.Error.Message, "private panic") {
				t.Fatal("panic contents leaked")
			}
		})
	}
}

func TestBridgeRejectsUnauthorizedAndMalformedRequests(t *testing.T) {
	b := openBridge(t, step.Request{})
	client := bridgeClient(t, b)
	response, err := postCall(t.Context(), client, "wrong", `{"version":1,"method":"context"}`)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d", response.StatusCode)
	}
	for _, body := range []string{
		`{"version":2,"method":"context"}`, `{"version":1,"method":"context","extra":1}`,
		`{"version":1,"method":"context"} {}`, `{`, strings.Repeat(" ", maxMessageBytes+1),
	} {
		response, err := postCall(t.Context(), client, b.descriptor.Token, body)
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(response.Body)
		response.Body.Close()
		if !bytes.Contains(data, []byte(`"code":"invalid_request"`)) {
			t.Fatalf("expected invalid request: %s", data)
		}
	}
}

func TestBridgeCloseCancelsAndJoinsHostCalls(t *testing.T) {
	started := make(chan struct{})
	finished := make(chan struct{})
	b := openBridge(t, step.Request{Helpers: helper.Set{"wait": func(ctx context.Context, _ []any) (any, error) {
		close(started)
		<-ctx.Done()
		defer close(finished)
		return nil, ctx.Err()
	}}})
	client := bridgeClient(t, b)
	completed := make(chan struct{})
	go func() {
		defer close(completed)
		response, err := postCall(t.Context(), client, b.descriptor.Token, `{"version":1,"method":"helper","params":{"name":"wait"}}`)
		if err == nil {
			response.Body.Close()
		}
	}()
	<-started
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-finished:
	default:
		t.Fatal("Close did not join the callback")
	}
	<-completed
}

func TestBridgeQueuesCallsBeyondConcurrencyLimit(t *testing.T) {
	started := make(chan struct{}, maxConcurrentCalls)
	release := make(chan struct{})
	b := openBridge(t, step.Request{Helpers: helper.Set{"hold": func(ctx context.Context, _ []any) (any, error) {
		started <- struct{}{}
		select {
		case <-release:
			return "held", nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}}})
	client := bridgeClient(t, b)
	results := make(chan string, maxConcurrentCalls+1)
	call := func(body string) {
		response, err := postCall(t.Context(), client, b.descriptor.Token, body)
		if err != nil {
			results <- err.Error()
			return
		}
		data, _ := io.ReadAll(response.Body)
		response.Body.Close()
		results <- string(data)
	}
	for range maxConcurrentCalls {
		go call(`{"version":1,"method":"helper","params":{"name":"hold"}}`)
	}
	for range maxConcurrentCalls {
		<-started
	}
	go call(`{"version":1,"method":"context"}`)
	close(release)
	for range maxConcurrentCalls + 1 {
		if result := <-results; strings.Contains(result, `"error"`) || !strings.Contains(result, `"result"`) {
			t.Fatalf("expected queued call to succeed: %s", result)
		}
	}
}

func TestBridgeCloseAbandonsCallsIgnoringCancellation(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	b, err := Open(t.Context(), step.Request{Helpers: helper.Set{"stuck": func(context.Context, []any) (any, error) {
		close(started)
		<-release
		return nil, nil
	}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	b.closeGrace = 20 * time.Millisecond
	client := bridgeClient(t, b)
	go func() {
		response, err := postCall(t.Context(), client, b.descriptor.Token, `{"version":1,"method":"helper","params":{"name":"stuck"}}`)
		if err == nil {
			response.Body.Close()
		}
	}()
	<-started
	if err := b.Close(); err == nil || !strings.Contains(err.Error(), "did not stop") {
		t.Fatalf("Close error = %v", err)
	}
	if _, err := os.Stat(b.dir); !os.IsNotExist(err) {
		t.Fatalf("bridge directory remains: %v", err)
	}
}
