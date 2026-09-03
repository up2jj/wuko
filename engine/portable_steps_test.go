package engine_test

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/up2jj/wuko/engine"
	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/steps/confirm"
	filestep "github.com/up2jj/wuko/steps/file"
	httpstep "github.com/up2jj/wuko/steps/http"
	setstep "github.com/up2jj/wuko/steps/set"
	"github.com/up2jj/wuko/workflow"
)

func TestPortableStepsShareTypedState(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		fmt.Fprint(writer, `{"version":"v1"}`)
	}))
	defer server.Close()

	registry := portableRegistry(t)
	root := t.TempDir()
	definition := &workflow.Definition{
		Version: 1, Name: "portable", Dir: root, Vars: map[string]any{"approved": true, "target": "linux"},
		Steps: []workflow.Step{
			{ID: "approval", Type: "tui_confirm", With: map[string]any{"variable": "approved", "message": "Continue?"}},
			{ID: "release", Type: "http", With: map[string]any{"url": server.URL, "response": "json"}},
			{ID: "artifact", Type: "set", With: map[string]any{"variable": "artifact", "expr": `steps.release.value.version + "-" + vars.target`}},
			{ID: "write", Type: "file", If: "steps.approval.value", With: map[string]any{
				"operation": "write", "path": "artifact.txt", "content": "{{ .steps.artifact.value }}", "mode": "0600",
			}},
			{ID: "inspect", Type: "file", With: map[string]any{"operation": "stat", "path": "artifact.txt"}},
		},
	}
	state, err := engine.New(registry).Run(t.Context(), definition, engine.Options{
		RunDir: root, Stdout: io.Discard, Stderr: io.Discard, Interactive: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	if state.Vars["artifact"] != "v1-linux" || state.Steps["approval"].(map[string]any)["value"] != true {
		t.Fatalf("state = %#v", state)
	}
	if state.Steps["inspect"].(map[string]any)["mode"] != "0600" {
		t.Fatalf("inspect = %#v", state.Steps["inspect"])
	}
	data, err := os.ReadFile(filepath.Join(root, "artifact.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "v1-linux" {
		t.Fatalf("content = %q", data)
	}
}

func TestHTTPRetriesAndCommitsSuccessfulResponse(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		if attempts.Add(1) == 1 {
			writer.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprint(writer, "retry")
			return
		}
		fmt.Fprint(writer, "ok")
	}))
	defer server.Close()

	definition := testDefinition(t, "retry", workflow.Step{
		ID: "fetch",
		Attempt: &workflow.AttemptControl{
			MaxAttempts:       workflow.LiteralCount(2),
			BackoffMultiplier: workflow.LiteralFactor(1),
			Steps: []workflow.Step{
				{ID: "request", Type: "http", With: map[string]any{"url": server.URL}},
			},
		},
	})

	registry := newTestRegistry(t, nil)
	if err := httpstep.Register(registry); err != nil {
		t.Fatal(err)
	}
	state, err := engine.New(registry).Run(t.Context(), definition, engine.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if attempts.Load() != 2 || state.Steps["fetch"].(map[string]any)["steps"].(map[string]any)["request"].(map[string]any)["value"] != "ok" {
		t.Fatalf("attempts = %d, state = %#v", attempts.Load(), state)
	}
}

func TestHTTPRetryRevalidatesPreviousResponse(t *testing.T) {
	var attempts atomic.Int32
	modified := "Thu, 20 Aug 2026 10:00:00 GMT"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch attempts.Add(1) {
		case 1:
			writer.Header().Set("ETag", `"release-42"`)
			writer.Header().Set("Last-Modified", modified)
			writer.Header().Set("Retry-After", "0")
			writer.Header().Set("X-Version", "cached")
			writer.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprint(writer, "cached body")
		case 2:
			if request.Header.Get("If-None-Match") != `"release-42"` || request.Header.Get("If-Modified-Since") != modified {
				t.Errorf("conditional headers = %#v", request.Header)
			}
			writer.Header().Set("ETag", `"release-43"`)
			writer.Header().Set("X-Version", "fresh")
			writer.WriteHeader(http.StatusNotModified)
		default:
			t.Errorf("unexpected attempt %d", attempts.Load())
		}
	}))
	defer server.Close()

	definition := testDefinition(t, "revalidate", workflow.Step{
		ID: "revalidate",
		Attempt: &workflow.AttemptControl{
			MaxAttempts:       workflow.LiteralCount(2),
			BackoffMultiplier: workflow.LiteralFactor(1),
			Steps: []workflow.Step{
				{ID: "request", Type: "http", With: map[string]any{"url": server.URL}},
			},
		},
	})

	registry := newTestRegistry(t, nil)
	if err := httpstep.Register(registry); err != nil {
		t.Fatal(err)
	}
	state, err := engine.New(registry).Run(t.Context(), definition, engine.Options{})
	if err != nil {
		t.Fatal(err)
	}
	// The body is isolated, so the request's outputs are read through the control.
	outputs := state.Steps["revalidate"].(map[string]any)["steps"].(map[string]any)["request"].(map[string]any)
	if attempts.Load() != 2 || outputs["status"] != http.StatusNotModified || outputs["body"] != "cached body" || outputs["value"] != "cached body" {
		t.Fatalf("attempts = %d, outputs = %#v", attempts.Load(), outputs)
	}
	headers := outputs["headers"].(map[string]any)
	if headers["Etag"].([]any)[0] != `"release-43"` || headers["Last-Modified"].([]any)[0] != modified || headers["X-Version"].([]any)[0] != "fresh" {
		t.Fatalf("headers = %#v", headers)
	}
}

func portableRegistry(t *testing.T) *step.Registry {
	t.Helper()
	registry := newTestRegistry(t, nil)
	for _, register := range []func(*step.Registry) error{
		confirm.Register, httpstep.Register, setstep.Register, filestep.Register,
	} {
		if err := register(registry); err != nil {
			t.Fatal(err)
		}
	}
	return registry
}
