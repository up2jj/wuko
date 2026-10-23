package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/up2jj/wuko/workflow"
)

// scriptedTransport answers the first request with failure and every later one from next.
type scriptedTransport struct {
	mu      sync.Mutex
	calls   int
	failure func(*http.Request) (*http.Response, error)
	next    http.RoundTripper
}

func (transport *scriptedTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	transport.mu.Lock()
	transport.calls++
	first := transport.calls == 1
	transport.mu.Unlock()
	if first {
		return transport.failure(request)
	}
	return transport.next.RoundTrip(request)
}

// A failure that says nothing about the plugin itself must not be remembered: the next load,
// such as a retry or the cancellation-free cleanup scope, has to try again.
func TestTransientPluginLoadFailuresAreNotCached(t *testing.T) {
	t.Parallel()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	script := []byte("#!/bin/sh\nWUKO_PLUGIN_TEST_HELPER=1 exec \"" + executable + "\"\n")
	archive := makeArchive(t, "wuko-plugin-acme", 0755, script)
	manifestData, _ := json.Marshal(Manifest{Version: 1, Namespace: "acme", PluginVersion: "1.0.0", Protocol: Protocol, Artifacts: []Artifact{{OS: runtime.GOOS, Arch: runtime.GOARCH, Path: "plugin.tar.gz", Format: "tar.gz", Entry: "wuko-plugin-acme", SHA256: digest(archive)}}})
	good := memoryTransport{manifest: manifestData, archive: archive}
	status := func(code int) func(*http.Request) (*http.Response, error) {
		return func(request *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: code, Status: http.StatusText(code), Body: io.NopCloser(bytes.NewReader(nil)), Header: make(http.Header), Request: request}, nil
		}
	}
	tests := []struct {
		name      string
		failure   func(*http.Request) (*http.Response, error)
		firstCtx  func() context.Context
		wantCache bool
	}{
		{name: "canceled", failure: func(request *http.Request) (*http.Response, error) {
			return nil, context.Canceled
		}},
		{name: "connection reset", failure: func(*http.Request) (*http.Response, error) {
			return nil, errors.New("connection reset by peer")
		}},
		{name: "bad gateway", failure: status(http.StatusBadGateway)},
		{name: "corrupted manifest", failure: func(request *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Status: "200 OK", Body: io.NopCloser(strings.NewReader(`{"version":1}`)), Header: make(http.Header), Request: request}, nil
		}, wantCache: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			transport := &scriptedTransport{failure: test.failure, next: good}
			manager := NewManager(Config{CacheDir: testCache(t), HTTPClient: &http.Client{Transport: transport}, Stderr: io.Discard})
			t.Cleanup(func() { _ = manager.Close(context.Background(), "completed") })
			sources := map[string]workflow.ResolvedPluginSource{"acme": {Source: "https://plugins.test/plugin.json", SHA256: digest(manifestData), With: map[string]any{"mode": "helpers"}}}
			if _, err := manager.LoadHelpers(t.Context(), sources); err == nil {
				t.Fatal("first load succeeded, want the scripted failure")
			}
			_, err := manager.LoadHelpers(t.Context(), sources)
			if test.wantCache {
				if err == nil {
					t.Fatal("second load succeeded, want the deterministic failure cached")
				}
				return
			}
			if err != nil {
				t.Fatalf("second load error = %v, want a fresh successful attempt", err)
			}
		})
	}
}
