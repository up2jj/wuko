package observe

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/up2jj/wuko/process"
)

func TestRegistryAcceptsFutureSourceWithoutEngineChanges(t *testing.T) {
	builder := testBuilder{sourceType: "process"}
	registry := NewRegistry(builder)
	if err := registry.Validate("process", map[string]any{"command": "server"}); err != nil {
		t.Fatal(err)
	}
	source, err := registry.Open(t.Context(), "process", OpenRequest{RunDir: t.TempDir(), Config: map[string]any{"command": "server"}})
	if err != nil {
		t.Fatal(err)
	}
	if source.Metadata()["kind"] != "test" {
		t.Fatalf("metadata = %#v", source.Metadata())
	}
	if err := registry.Register(builder); err == nil || !strings.Contains(err.Error(), "already registered") {
		t.Fatalf("duplicate error = %v", err)
	}
}

func TestHTTPSourceEmitsOnlyChangedResponsesByDefault(t *testing.T) {
	var requests atomic.Int32
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		request := requests.Add(1)
		body := `{"version":1}`
		if request >= 3 {
			body = `{"version":2}`
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	builder := HTTPBuilder{Client: client}
	source, err := builder.Open(t.Context(), OpenRequest{Config: map[string]any{
		"every": "1ms", "request": map[string]any{"url": "https://example.test/status", "response": "json"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	initial := source.Initial().(map[string]any)
	if initial["status"] != http.StatusOK {
		t.Fatalf("initial = %#v", initial)
	}
	event, err := source.Next(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	value := event.(map[string]any)["value"].(map[string]any)
	if value["version"] != float64(2) || requests.Load() != 3 {
		t.Fatalf("event = %#v after %d requests", event, requests.Load())
	}
}

type testBuilder struct{ sourceType string }

func (builder testBuilder) Type() string          { return builder.sourceType }
func (testBuilder) Validate(map[string]any) error { return nil }
func (testBuilder) Open(context.Context, OpenRequest) (Source, error) {
	return testSource{}, nil
}

type testSource struct{}

func (testSource) Initial() any                          { return nil }
func (testSource) Next(ctx context.Context) (any, error) { <-ctx.Done(); return nil, ctx.Err() }
func (testSource) NewBatch() Batch                       { return &testBatch{} }
func (testSource) Metadata() map[string]any              { return map[string]any{"kind": "test"} }
func (testSource) Close() error                          { return nil }

type testBatch struct{ values []any }

func (batch *testBatch) Add(value any) { batch.values = append(batch.values, value) }
func (batch *testBatch) Merge(other Batch) {
	batch.values = append(batch.values, other.(*testBatch).values...)
}
func (batch *testBatch) Empty() bool { return len(batch.values) == 0 }
func (batch *testBatch) Binding() map[string]any {
	return map[string]any{"test": append([]any(nil), batch.values...)}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (roundTrip roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTrip(request)
}

func TestShellSourceEmitsOnlyChangedOutputByDefault(t *testing.T) {
	var runs atomic.Int32
	builder := ShellBuilder{Executor: shellExecutorFunc(func(_ context.Context, options process.Options) (process.Result, error) {
		run := runs.Add(1)
		if options.Command != "git" || len(options.Args) != 2 {
			return process.Result{}, fmt.Errorf("unexpected command %s %v", options.Command, options.Args)
		}
		if run >= 3 {
			return process.Result{Stdout: "second\n"}, nil
		}
		return process.Result{Stdout: "first\n", Stderr: fmt.Sprintf("run %d\n", run)}, nil
	})}
	source, err := builder.Open(t.Context(), OpenRequest{RunDir: t.TempDir(), Config: map[string]any{
		"command": "git", "args": []any{"rev-parse", "HEAD"}, "every": "1ms",
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	initial := source.Initial().(map[string]any)
	if initial["value"] != "first" || initial["exit_code"] != 0 {
		t.Fatalf("initial = %#v", initial)
	}
	event, err := source.Next(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	// Run 2 differs only on standard error, which stays out of the fingerprint.
	if value := event.(map[string]any)["value"]; value != "second" || runs.Load() != 3 {
		t.Fatalf("event = %#v after %d runs", event, runs.Load())
	}
}

func TestShellSourceObservesFailingCommandWithoutStopping(t *testing.T) {
	var runs atomic.Int32
	builder := ShellBuilder{Executor: shellExecutorFunc(func(context.Context, process.Options) (process.Result, error) {
		if runs.Add(1) == 1 {
			return process.Result{Stdout: "{}", ExitCode: 0}, nil
		}
		return process.Result{Stdout: "boom", Stderr: "no such target\n", ExitCode: 2},
			&process.ExitError{Command: "make", Code: 2}
	})}
	source, err := builder.Open(t.Context(), OpenRequest{RunDir: t.TempDir(), Config: map[string]any{
		"command": "make", "every": "1ms", "output": "json",
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	event, err := source.Next(t.Context())
	if err != nil {
		t.Fatalf("a failing command stopped observation: %v", err)
	}
	observation := event.(map[string]any)
	if observation["exit_code"] != 2 || observation["error"] != "command exited with status 2" {
		t.Fatalf("observation = %#v", observation)
	}
	// Undecodable output from a failed command is the command's failure, not the source's.
	if observation["value"] != nil || observation["stderr"] != "no such target\n" {
		t.Fatalf("observation = %#v", observation)
	}
}

func TestShellSourceFailsOnUnusableOutputAndTimeout(t *testing.T) {
	malformed := ShellBuilder{Executor: shellExecutorFunc(func(context.Context, process.Options) (process.Result, error) {
		return process.Result{Stdout: "not json"}, nil
	})}
	if _, err := malformed.Open(t.Context(), OpenRequest{Config: map[string]any{"command": "status", "output": "json"}}); err == nil ||
		!strings.Contains(err.Error(), "decoding shell observation JSON") {
		t.Fatalf("open error = %v", err)
	}

	slow := ShellBuilder{Executor: shellExecutorFunc(func(ctx context.Context, _ process.Options) (process.Result, error) {
		<-ctx.Done()
		return process.Result{}, ctx.Err()
	})}
	_, err := slow.Open(t.Context(), OpenRequest{Config: map[string]any{"command": "status", "timeout": "1ms"}})
	if err == nil || !strings.Contains(err.Error(), "timed out after 1ms") {
		t.Fatalf("open error = %v", err)
	}
}

func TestShellSourceRejectsInvalidConfiguration(t *testing.T) {
	for name, config := range map[string]map[string]any{
		"missing command": {"args": []any{"status"}},
		"blank command":   {"command": "  "},
		"every":           {"command": "status", "every": "0s"},
		"timeout":         {"command": "status", "timeout": "-1s"},
		"trigger":         {"command": "status", "trigger": "sometimes"},
		"output":          {"command": "status", "output": "yaml"},
		"unknown field":   {"command": "status", "shell": "bash"},
	} {
		t.Run(name, func(t *testing.T) {
			if err := (ShellBuilder{}).Validate(config); err == nil {
				t.Fatalf("%#v was accepted", config)
			}
		})
	}
	// Values that are still templates at validation time are checked after rendering.
	if err := (ShellBuilder{}).Validate(map[string]any{
		"command": "status", "every": "{{ .vars.every }}", "trigger": "{{ .vars.trigger }}",
	}); err != nil {
		t.Fatal(err)
	}
}

func TestShellSourceRunsHostCommandWithWorkflowEnvironment(t *testing.T) {
	root := t.TempDir()
	source, err := (ShellBuilder{}).Open(t.Context(), OpenRequest{
		RunDir: root,
		Env:    map[string]string{"PATH": os.Getenv("PATH"), "GREETING": "hello"},
		Config: map[string]any{"command": "sh", "args": []any{"-c", `printf '%s from %s' "$GREETING" "$(pwd)"`}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	initial := source.Initial().(map[string]any)
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if initial["value"] != "hello from "+resolved {
		t.Fatalf("initial = %#v, run directory %s", initial, resolved)
	}
	if source.Metadata()["working_directory"] != root {
		t.Fatalf("metadata = %#v", source.Metadata())
	}
}

type shellExecutorFunc func(context.Context, process.Options) (process.Result, error)

func (run shellExecutorFunc) Run(ctx context.Context, options process.Options) (process.Result, error) {
	return run(ctx, options)
}

func TestShellSourceKeepsAnEmptyWorkflowEnvironmentEmpty(t *testing.T) {
	t.Setenv("WUKO_OBSERVE_MARKER", "leaked")
	command := map[string]any{"command": " sh ", "args": []any{"-c", `printf '%s' "$WUKO_OBSERVE_MARKER"`}}

	// A non-nil empty environment is a deliberate override, not a missing one.
	overridden, err := (ShellBuilder{}).Open(t.Context(), OpenRequest{RunDir: t.TempDir(), Env: map[string]string{}, Config: command})
	if err != nil {
		t.Fatal(err)
	}
	defer overridden.Close()
	if value := overridden.Initial().(map[string]any)["value"]; value != "" {
		t.Fatalf("host environment leaked into an empty workflow environment: %#v", value)
	}
	// A trimmed command is the one that runs.
	if overridden.Metadata()["command"] != "sh" {
		t.Fatalf("metadata = %#v", overridden.Metadata())
	}

	inherited, err := (ShellBuilder{}).Open(t.Context(), OpenRequest{RunDir: t.TempDir(), Config: command})
	if err != nil {
		t.Fatal(err)
	}
	defer inherited.Close()
	if value := inherited.Initial().(map[string]any)["value"]; value != "leaked" {
		t.Fatalf("absent workflow environment did not fall back to the host: %#v", value)
	}
}

func TestShellSourceReportsTruncationBehindMalformedJSON(t *testing.T) {
	builder := ShellBuilder{Executor: shellExecutorFunc(func(context.Context, process.Options) (process.Result, error) {
		return process.Result{Stdout: `{"items":[1,2`, StdoutTruncated: true}, nil
	})}
	_, err := builder.Open(t.Context(), OpenRequest{Config: map[string]any{"command": "status", "output": "json"}})
	if err == nil || !strings.Contains(err.Error(), "stdout was truncated at 1048576 bytes") {
		t.Fatalf("open error = %v", err)
	}
}

func TestShellSourceRejectsInvalidEnvironmentName(t *testing.T) {
	err := (ShellBuilder{}).Validate(map[string]any{"command": "status", "env": map[string]any{"FOO=BAR": "value"}})
	if err == nil || !strings.Contains(err.Error(), `invalid environment name "FOO=BAR"`) {
		t.Fatalf("validation error = %v", err)
	}
}
