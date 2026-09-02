package observe

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/up2jj/wuko/engine"
	"github.com/up2jj/wuko/process"
	"github.com/up2jj/wuko/workflow"
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

func TestSchedulerGivesUpOnASourceThatOnlyChurns(t *testing.T) {
	source := &churningSource{err: errors.New("event channel closed unexpectedly")}
	tolerated := 0
	_, err := Scheduler{
		Source: source, SourceType: "test", OnError: workflow.ObserveContinue, FailurePace: time.Millisecond,
	}.Run(t.Context(), engine.BackgroundControlRuntime{
		RunIteration: func(context.Context, map[string]any) error { return nil },
		Report: func(event engine.BackgroundControlEvent) {
			if event.Kind == engine.BackgroundSourceFailure {
				tolerated++
			}
		},
	})
	if err == nil || !strings.Contains(err.Error(), "without doing any work") {
		t.Fatalf("run error = %v", err)
	}
	if tolerated != failureWindowPaces {
		t.Fatalf("tolerated %d failures before giving up, want %d", tolerated, failureWindowPaces)
	}
}

func TestSchedulerPacesInstantFailuresAndKeepsObserving(t *testing.T) {
	const failures = 10
	pace := 20 * time.Millisecond
	source := &churningSource{err: errors.New("transient"), recoverAfter: failures}
	runs := make(chan struct{}, 4)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	started := time.Now()
	go func() {
		<-runs // the initial run
		<-runs // the run the recovered source triggered
		cancel()
	}()
	_, err := Scheduler{
		Source: source, SourceType: "test", OnError: workflow.ObserveContinue, FailurePace: pace,
	}.Run(ctx, engine.BackgroundControlRuntime{
		RunIteration: func(context.Context, map[string]any) error {
			runs <- struct{}{}
			return nil
		},
		Report: func(engine.BackgroundControlEvent) {},
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("run error = %v", err)
	}
	// Failing instantly is paced rather than retried in a spin, and a source that recovers
	// inside the window keeps observing.
	if elapsed := time.Since(started); elapsed < failures*pace {
		t.Fatalf("%d instant failures took %s, want at least %s", failures, elapsed, failures*pace)
	}
}

type churningSource struct {
	err          error
	recoverAfter int
	calls        int
}

func (source *churningSource) Initial() any { return nil }

func (source *churningSource) Next(ctx context.Context) (any, error) {
	source.calls++
	if source.recoverAfter > 0 && source.calls > source.recoverAfter {
		if source.calls > source.recoverAfter+1 {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return map[string]any{"recovered": true}, nil
	}
	return nil, source.err
}

func (*churningSource) NewBatch() Batch          { return &latestBatch{root: "test"} }
func (*churningSource) Metadata() map[string]any { return map[string]any{} }
func (*churningSource) Close() error             { return nil }

// Add and Merge retain what they are given, so Binding is the only copy standing between a
// source's observation and a body that could mutate it. Pin that: it is what makes dropping the
// copies in the scheduler and in the coalescing steps safe.
func TestLatestBatchBindingIsolatesRetainedObservation(t *testing.T) {
	observation := map[string]any{"value": map[string]any{"version": 1}}
	batch := &latestBatch{root: "http"}
	batch.Add(observation)

	binding := batch.Binding()
	held := binding["http"].(map[string]any)
	held["value"].(map[string]any)["version"] = 2
	held["added"] = true

	nested := observation["value"].(map[string]any)
	if nested["version"] != 1 {
		t.Fatalf("binding mutation reached the source observation: %#v", observation)
	}
	if _, present := observation["added"]; present {
		t.Fatalf("binding gained a key on the source observation: %#v", observation)
	}
	// A second binding is unaffected by what the first caller did to its copy.
	if again := batch.Binding()["http"].(map[string]any); again["value"].(map[string]any)["version"] != 1 {
		t.Fatalf("second binding = %#v", again)
	}
}

// Merge hands the newer observation over without copying, and the merged-from batch is one the
// scheduler is about to discard.
func TestLatestBatchMergeKeepsNewestObservation(t *testing.T) {
	older, newer := &latestBatch{root: "http"}, &latestBatch{root: "http"}
	older.Add(map[string]any{"value": "first"})
	newer.Add(map[string]any{"value": "second"})
	older.Merge(newer)
	if got := older.Binding()["http"].(map[string]any)["value"]; got != "second" {
		t.Fatalf("merged value = %v, want second", got)
	}
	empty := &latestBatch{root: "http"}
	older.Merge(empty)
	if got := older.Binding()["http"].(map[string]any)["value"]; got != "second" {
		t.Fatalf("merging an empty batch overwrote the value: %v", got)
	}
}
