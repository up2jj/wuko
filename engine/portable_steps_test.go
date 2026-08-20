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
			{ID: "approval", Type: "confirm", With: map[string]any{"variable": "approved", "message": "Continue?"}},
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

	definition := &workflow.Definition{Version: 1, Name: "retry", Dir: t.TempDir(), Steps: []workflow.Step{{
		ID: "request", Type: "http", With: map[string]any{"url": server.URL},
		Retry: &workflow.RetryPolicy{MaxAttempts: 2, BackoffMultiplier: 1},
	}}}
	registry := step.NewRegistry()
	if err := httpstep.Register(registry); err != nil {
		t.Fatal(err)
	}
	state, err := engine.New(registry).Run(t.Context(), definition, engine.Options{Stdout: io.Discard, Stderr: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if attempts.Load() != 2 || state.Steps["request"].(map[string]any)["value"] != "ok" {
		t.Fatalf("attempts = %d, state = %#v", attempts.Load(), state)
	}
}

func portableRegistry(t *testing.T) *step.Registry {
	t.Helper()
	registry := step.NewRegistry()
	for _, register := range []func(*step.Registry) error{
		confirm.Register, httpstep.Register, setstep.Register, filestep.Register,
	} {
		if err := register(registry); err != nil {
			t.Fatal(err)
		}
	}
	return registry
}
