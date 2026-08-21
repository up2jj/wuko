package http

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	nethttp "net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/up2jj/wuko/step"
)

func TestJSONRequestAndResponse(t *testing.T) {
	server := httptest.NewServer(nethttp.HandlerFunc(func(writer nethttp.ResponseWriter, request *nethttp.Request) {
		if request.Method != nethttp.MethodPost || request.URL.Query().Get("channel") != "stable" {
			t.Errorf("request = %s %s", request.Method, request.URL.String())
		}
		if request.Header.Get("Authorization") != "Bearer token" || request.Header.Get("Content-Type") != "application/json" {
			t.Errorf("headers = %#v", request.Header)
		}
		data, _ := io.ReadAll(request.Body)
		if string(data) != `{"name":"wuko"}` {
			t.Errorf("body = %q", data)
		}
		writer.Header().Add("X-Result", "one")
		writer.Header().Add("X-Result", "two")
		writer.WriteHeader(nethttp.StatusCreated)
		fmt.Fprint(writer, `{"version":"v1","ready":true}`)
	}))
	defer server.Close()

	runner, err := New(map[string]any{
		"url": server.URL, "method": "post", "headers": map[string]any{"Authorization": "Bearer token"},
		"query": map[string]any{"channel": "stable"}, "json": map[string]any{"name": "wuko"},
		"response": "json", "success_statuses": []any{201},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(t.Context(), step.Request{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outputs["status"] != 201 || result.Outputs["value"].(map[string]any)["version"] != "v1" {
		t.Fatalf("outputs = %#v", result.Outputs)
	}
	headers := result.Outputs["headers"].(map[string]any)
	if got := headers["X-Result"].([]any); len(got) != 2 {
		t.Fatalf("headers = %#v", headers)
	}
}

func TestStatusAndResponseErrors(t *testing.T) {
	server := httptest.NewServer(nethttp.HandlerFunc(func(writer nethttp.ResponseWriter, _ *nethttp.Request) {
		writer.WriteHeader(nethttp.StatusTeapot)
		fmt.Fprint(writer, "no")
	}))
	defer server.Close()
	runner, err := New(map[string]any{"url": server.URL})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(t.Context(), step.Request{})
	if err == nil || result.Outputs["status"] != nethttp.StatusTeapot {
		t.Fatalf("result = %#v, error = %v", result, err)
	}
	var observation step.ObservationError
	if !errors.As(err, &observation) || !observation.ObservationAvailable() {
		t.Fatalf("status error does not expose a completed observation: %T %v", err, err)
	}

	runner, err = New(map[string]any{"url": server.URL, "response": "json", "success_statuses": []any{418}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(t.Context(), step.Request{}); err == nil || !strings.Contains(err.Error(), "decoding JSON") {
		t.Fatalf("error = %v", err)
	} else if errors.As(err, &observation) {
		t.Fatalf("decoding error unexpectedly exposes an observation: %T %v", err, err)
	}
}

func TestResponseLimitAndCancellation(t *testing.T) {
	large := httptest.NewServer(nethttp.HandlerFunc(func(writer nethttp.ResponseWriter, _ *nethttp.Request) {
		writer.Header().Set("Content-Length", fmt.Sprint(maxResponseSize+1))
		writer.WriteHeader(nethttp.StatusOK)
	}))
	defer large.Close()
	runner, err := New(map[string]any{"url": large.URL})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(t.Context(), step.Request{}); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("error = %v", err)
	} else {
		var observation step.ObservationError
		if errors.As(err, &observation) {
			t.Fatalf("response-size error unexpectedly exposes an observation: %T %v", err, err)
		}
	}

	canceled := httptest.NewServer(nethttp.HandlerFunc(func(_ nethttp.ResponseWriter, request *nethttp.Request) {
		<-request.Context().Done()
	}))
	defer canceled.Close()
	runner, err = New(map[string]any{"url": canceled.URL})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = runner.Run(ctx, step.Request{})
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
}

func TestRedirectPolicyRejectsCredentialBoundaryChanges(t *testing.T) {
	client := newClient(nil, nil)
	tests := []struct {
		name    string
		from    string
		to      string
		wantErr bool
	}{
		{name: "same origin", from: "https://api.example.com/start", to: "https://api.example.com/next"},
		{name: "https upgrade", from: "http://api.example.com/start", to: "https://api.example.com/next"},
		{name: "https downgrade", from: "https://api.example.com/start", to: "http://api.example.com/next", wantErr: true},
		{name: "different host", from: "https://api.example.com/start", to: "https://redirect.example.net/next", wantErr: true},
		{name: "different port", from: "https://api.example.com/start", to: "https://api.example.com:8443/next", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			from, err := url.Parse(tt.from)
			if err != nil {
				t.Fatal(err)
			}
			to, err := url.Parse(tt.to)
			if err != nil {
				t.Fatal(err)
			}
			err = client.CheckRedirect(&nethttp.Request{URL: to}, []*nethttp.Request{{URL: from}})
			if (err != nil) != tt.wantErr {
				t.Fatalf("CheckRedirect() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestRejectsInvalidConfiguration(t *testing.T) {
	tests := []map[string]any{
		{},
		{"url": "ftp://example.com"},
		{"url": "https://user:pass@example.com"},
		{"url": "https://example.com", "body": "x", "json": nil},
		{"url": "https://example.com", "response": "yaml"},
		{"url": "https://example.com", "success_statuses": []any{99}},
		{"url": "https://example.com", "unknown": true},
	}
	for _, raw := range tests {
		if _, err := New(raw); err == nil {
			t.Fatalf("New(%#v) succeeded", raw)
		}
	}
}

func TestDecodeJSONPreservesNumbers(t *testing.T) {
	value, err := decodeJSON([]byte(`{"count":9007199254740993}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := value.(map[string]any)["count"]; got != json.Number("9007199254740993") {
		t.Fatalf("count = %#v", got)
	}
}

func TestRetryAfterParsing(t *testing.T) {
	now := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name  string
		value string
		want  time.Duration
	}{
		{name: "delta seconds", value: "12", want: 12 * time.Second},
		{name: "HTTP date", value: now.Add(20 * time.Second).Format(nethttp.TimeFormat), want: 20 * time.Second},
		{name: "past HTTP date", value: now.Add(-time.Second).Format(nethttp.TimeFormat)},
		{name: "negative delta", value: "-2"},
		{name: "invalid", value: "later"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := parseRetryAfter(test.value, now); got != test.want {
				t.Fatalf("parseRetryAfter(%q) = %s, want %s", test.value, got, test.want)
			}
		})
	}
}

func TestConditionalHeadersAndRevalidatedResult(t *testing.T) {
	modified := "Thu, 20 Aug 2026 10:00:00 GMT"
	previous := &step.Result{Outputs: map[string]any{
		"status": 503,
		"headers": map[string]any{
			"Etag":          []any{`"release-42"`},
			"Last-Modified": []any{modified},
			"X-Cached":      []any{"old"},
		},
		"body":  `{"ready":false}`,
		"value": map[string]any{"ready": false},
	}}
	headers := make(nethttp.Header)
	applyConditionalHeaders(headers, previous)
	if headers.Get("If-None-Match") != `"release-42"` || headers.Get("If-Modified-Since") != modified {
		t.Fatalf("conditional headers = %#v", headers)
	}

	explicit := nethttp.Header{"If-None-Match": []string{""}, "If-Modified-Since": []string{"explicit"}}
	applyConditionalHeaders(explicit, previous)
	if values := explicit.Values("If-None-Match"); len(values) != 1 || values[0] != "" {
		t.Fatalf("explicit ETag condition was replaced: %#v", explicit)
	}
	if explicit.Get("If-Modified-Since") != "explicit" {
		t.Fatalf("explicit modified condition was replaced: %#v", explicit)
	}

	current := make(nethttp.Header)
	current.Set("ETag", `"release-43"`)
	current.Set("X-Cached", "fresh")
	outputs, ok := revalidatedOutputs(previous, current)
	ready, valueOK := outputs["value"].(map[string]any)["ready"].(bool)
	if !ok || !valueOK || ready || outputs["status"] != nethttp.StatusNotModified || outputs["body"] != previous.Outputs["body"] {
		t.Fatalf("revalidated outputs = %#v, ok = %v", outputs, ok)
	}
	merged := outputs["headers"].(map[string]any)
	if outputHeader(merged, "ETag") != `"release-43"` || outputHeader(merged, "Last-Modified") != modified || outputHeader(merged, "X-Cached") != "fresh" {
		t.Fatalf("merged headers = %#v", merged)
	}
	if _, ok := revalidatedOutputs(nil, current); ok {
		t.Fatal("304 without a previous representation was reusable")
	}
}
