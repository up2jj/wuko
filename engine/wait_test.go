package engine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/up2jj/wuko/step"
	httpstep "github.com/up2jj/wuko/steps/http"
	luastep "github.com/up2jj/wuko/steps/lua"
	"github.com/up2jj/wuko/workflow"
)

type pollResult struct {
	result step.Result
	err    error
}

type sequencePollRunner struct {
	results []pollResult
	calls   int
}

func (runner *sequencePollRunner) Run(context.Context, step.Request) (step.Result, error) {
	index := min(runner.calls, len(runner.results)-1)
	runner.calls++
	return runner.results[index].result, runner.results[index].err
}

type completedObservationError string

func (err completedObservationError) Error() string          { return string(err) }
func (completedObservationError) ObservationAvailable() bool { return true }

func waitTimeout(duration time.Duration) *workflow.Duration {
	value := workflow.Duration(duration)
	return &value
}

func waitDefinition(with map[string]any, timeout *workflow.Duration) *workflow.Definition {
	return &workflow.Definition{
		Version: 1, Name: "wait", Dir: "/workflow", Vars: map[string]any{}, Env: workflow.Environment{},
		Steps: []workflow.Step{{ID: "ready", Type: "wait", Timeout: timeout, With: with}},
	}
}

func TestWaitRejectsInvalidConfiguration(t *testing.T) {
	registry := newTestRegistry(t, nil)
	if err := registry.Register("probe", func(map[string]any) (step.Runner, error) {
		return &sequencePollRunner{results: []pollResult{{result: step.Result{}}}}, nil
	}); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		with    map[string]any
		timeout *workflow.Duration
		want    string
	}{
		{name: "empty", with: map[string]any{}, want: "exactly one"},
		{name: "both modes", with: map[string]any{"duration": "1s", "step": map[string]any{"type": "probe"}, "until": "true"}, timeout: waitTimeout(time.Second), want: "exactly one"},
		{name: "zero duration", with: map[string]any{"duration": "0s"}, want: "greater than zero"},
		{name: "missing timeout", with: map[string]any{"step": map[string]any{"type": "probe"}, "until": "true"}, want: "top-level timeout"},
		{name: "missing step", with: map[string]any{"until": "true"}, timeout: waitTimeout(time.Second), want: "step is required"},
		{name: "missing until", with: map[string]any{"step": map[string]any{"type": "probe"}}, timeout: waitTimeout(time.Second), want: "until is required"},
		{name: "zero interval", with: map[string]any{"step": map[string]any{"type": "probe"}, "until": "true", "interval": "0s"}, timeout: waitTimeout(time.Second), want: "interval"},
		{name: "nested wait", with: map[string]any{"step": map[string]any{"type": "wait"}, "until": "true"}, timeout: waitTimeout(time.Second), want: "nested wait"},
		{name: "nested policy", with: map[string]any{"step": map[string]any{"type": "probe", "retry": map[string]any{}}, "until": "true"}, timeout: waitTimeout(time.Second), want: "field retry"},
		{name: "unknown nested type", with: map[string]any{"step": map[string]any{"type": "missing"}, "until": "true"}, timeout: waitTimeout(time.Second), want: "unknown step type"},
		{name: "invalid predicate", with: map[string]any{"step": map[string]any{"type": "probe"}, "until": "result +"}, timeout: waitTimeout(time.Second), want: "compiling until"},
		{name: "non-boolean predicate", with: map[string]any{"step": map[string]any{"type": "probe"}, "until": "poll"}, timeout: waitTimeout(time.Second), want: "expected bool"},
		{name: "unknown field", with: map[string]any{"duration": "1s", "bogus": true}, want: "field bogus"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := New(registry).Validate(t.Context(), waitDefinition(tt.with, tt.timeout), Options{})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

func TestWaitConditionUsesBatchBinding(t *testing.T) {
	program, err := compileWaitCondition(`batch.index == 1 && len(batch.items) == 2`)
	if err != nil {
		t.Fatal(err)
	}
	matched, err := evaluateWaitCondition(program, step.Request{Bindings: map[string]any{
		"batch": map[string]any{"index": 1, "items": []any{"api", "worker"}},
	}}, map[string]any{}, nil, 1)
	if err != nil || !matched {
		t.Fatalf("matched = %v, error = %v", matched, err)
	}
}

func TestFixedWaitCompletesAndHonorsTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		state, err := New(newTestRegistry(t, nil)).Run(t.Context(), waitDefinition(map[string]any{"duration": "2s"}, nil), Options{})
		if err != nil {
			t.Fatal(err)
		}
		if len(state.Steps["ready"].(map[string]any)) != 0 || state.Stats.Polls != 0 {
			t.Fatalf("state = %#v", state)
		}
	})

	synctest.Test(t, func(t *testing.T) {
		_, err := New(newTestRegistry(t, nil)).Run(t.Context(), waitDefinition(map[string]any{"duration": "2s"}, waitTimeout(time.Second)), Options{})
		if err == nil || !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("error = %v", err)
		}
	})
}

func TestPollCommitsOnlyMatchingObservationAndReportsProgress(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		runner := &sequencePollRunner{results: []pollResult{
			{result: step.Result{Outputs: map[string]any{"value": 1}, Variables: map[string]any{"leaked": 1}}, err: errors.New("not ready")},
			{result: step.Result{Outputs: map[string]any{"value": 2}, Variables: map[string]any{"leaked": 2}}},
			{result: step.Result{Outputs: map[string]any{"value": 3}, Variables: map[string]any{"committed": true}}},
		}}
		registry := newTestRegistry(t, nil)
		if err := registry.Register("probe", func(map[string]any) (step.Runner, error) { return runner, nil }); err != nil {
			t.Fatal(err)
		}
		definition := waitDefinition(map[string]any{
			"interval": "1s", "step": map[string]any{"type": "probe"},
			"until": `error == nil && hasKey(result, "value") && result.value == 3 && poll == 3 && workflow.name == "wait"`,
		}, waitTimeout(10*time.Second))
		var events []ProgressEvent
		state, err := New(registry).Run(t.Context(), definition, Options{
			Stdout: io.Discard, Stderr: io.Discard,
			Progress: func(event ProgressEvent) { events = append(events, event) },
		})
		if err != nil {
			t.Fatal(err)
		}
		if runner.calls != 3 || state.Steps["ready"].(map[string]any)["value"] != 3 {
			t.Fatalf("calls = %d, state = %#v", runner.calls, state)
		}
		if _, exists := state.Vars["leaked"]; exists || state.Vars["committed"] != true {
			t.Fatalf("variables = %#v", state.Vars)
		}
		if state.Stats.Polls != 3 || state.Stats.PollWait != 2*time.Second || state.Stats.Attempts != 1 || state.Stats.Retries != 0 {
			t.Fatalf("stats = %#v", state.Stats)
		}
		var kinds []ProgressKind
		for _, event := range events {
			if event.Kind == PollStarted || event.Kind == PollFinished || event.Kind == PollScheduled {
				kinds = append(kinds, event.Kind)
			}
		}
		want := []ProgressKind{PollStarted, PollFinished, PollScheduled, PollStarted, PollFinished, PollScheduled, PollStarted, PollFinished}
		if len(kinds) != len(want) {
			t.Fatalf("poll events = %#v", kinds)
		}
		for i := range want {
			if kinds[i] != want[i] {
				t.Fatalf("poll event %d = %q, want %q", i, kinds[i], want[i])
			}
		}
	})
}

func TestPollHTTPDistinguishesResponsesFromFatalErrors(t *testing.T) {
	tests := []struct {
		name    string
		pollErr error
		wantErr bool
	}{
		{name: "completed status", pollErr: completedObservationError("HTTP request returned status 404")},
		{name: "transport failure", pollErr: errors.New("connection refused"), wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := &sequencePollRunner{results: []pollResult{{
				result: step.Result{Outputs: map[string]any{"status": 404}}, err: tt.pollErr,
			}}}
			registry := newTestRegistry(t, nil)
			if err := registry.Register("http", func(map[string]any) (step.Runner, error) { return runner, nil }); err != nil {
				t.Fatal(err)
			}
			definition := waitDefinition(map[string]any{
				"step": map[string]any{"type": "http"}, "until": `result.status == 404 && error != nil`,
			}, waitTimeout(time.Second))
			state, err := New(registry).Run(t.Context(), definition, Options{})
			if tt.wantErr {
				if err == nil || !strings.Contains(err.Error(), "connection refused") {
					t.Fatalf("error = %v", err)
				}
				return
			}
			if err != nil || state.Steps["ready"].(map[string]any)["status"] != 404 {
				t.Fatalf("state = %#v, error = %v", state, err)
			}
		})
	}
}

func TestWaitPollsRealHTTPResponses(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		if calls.Add(1) == 1 {
			writer.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprint(writer, `{"status":"starting"}`)
			return
		}
		fmt.Fprint(writer, `{"status":"ready"}`)
	}))
	defer server.Close()
	registry := newTestRegistry(t, nil)
	if err := httpstep.Register(registry); err != nil {
		t.Fatal(err)
	}
	definition := waitDefinition(map[string]any{
		"interval": "1ms",
		"step": map[string]any{"type": "http", "with": map[string]any{
			"url": server.URL, "response": "json",
		}},
		"until": `result.status == 200 && error == nil && result.value.status == "ready"`,
	}, waitTimeout(time.Second))
	state, err := New(registry).Run(t.Context(), definition, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 || state.Steps["ready"].(map[string]any)["status"] != 200 {
		t.Fatalf("calls = %d, state = %#v", calls.Load(), state)
	}
}

func TestWaitHTTPDecodeFailureStopsImmediately(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(writer, `{not-json`)
	}))
	defer server.Close()
	registry := newTestRegistry(t, nil)
	if err := httpstep.Register(registry); err != nil {
		t.Fatal(err)
	}
	definition := waitDefinition(map[string]any{
		"step": map[string]any{"type": "http", "with": map[string]any{
			"url": server.URL, "response": "json",
		}},
		"until": "false",
	}, waitTimeout(time.Second))
	_, err := New(registry).Run(t.Context(), definition, Options{})
	if err == nil || !strings.Contains(err.Error(), "decoding JSON response") {
		t.Fatalf("error = %v", err)
	}
}

func TestWaitPreservesNestedLuaSourceAndUntilText(t *testing.T) {
	registry := newTestRegistry(t, nil)
	if err := luastep.Register(registry); err != nil {
		t.Fatal(err)
	}
	definition := waitDefinition(map[string]any{
		"step": map[string]any{"type": "lua", "with": map[string]any{
			"source": `wuko.output("value", "{{ literal }}")`,
		}},
		"until": `result.value == "{{ literal }}"`,
	}, waitTimeout(time.Second))
	state, err := New(registry).Run(t.Context(), definition, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if state.Steps["ready"].(map[string]any)["value"] != "{{ literal }}" {
		t.Fatalf("state = %#v", state)
	}
}

func TestPollTimeoutCanBeRetried(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		runner := &sequencePollRunner{results: []pollResult{{result: step.Result{Outputs: map[string]any{"ready": false}}}}}
		registry := newTestRegistry(t, nil)
		if err := registry.Register("probe", func(map[string]any) (step.Runner, error) { return runner, nil }); err != nil {
			t.Fatal(err)
		}
		definition := waitDefinition(map[string]any{
			"interval": "2s", "step": map[string]any{"type": "probe"}, "until": "result.ready",
		}, waitTimeout(time.Second))
		definition.Steps[0].Retry = immediateRetry(2)
		var finished ProgressEvent
		_, err := New(registry).Run(t.Context(), definition, Options{
			Stdout: io.Discard, Stderr: io.Discard,
			Progress: func(event ProgressEvent) {
				if event.Kind == WorkflowFinished {
					finished = event
				}
			},
		})
		if err == nil || !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("error = %v", err)
		}
		if runner.calls != 2 || finished.Stats.Attempts != 2 || finished.Stats.Retries != 1 || finished.Stats.Polls != 2 {
			t.Fatalf("calls = %d, stats = %#v", runner.calls, finished.Stats)
		}
	})
}

func TestWaitDryRunShowsModeAndPolicy(t *testing.T) {
	registry := newTestRegistry(t, nil)
	if err := registry.Register("probe", func(map[string]any) (step.Runner, error) {
		return &sequencePollRunner{results: []pollResult{{result: step.Result{}}}}, nil
	}); err != nil {
		t.Fatal(err)
	}
	definition := waitDefinition(map[string]any{
		"step": map[string]any{"type": "probe"}, "until": "true",
	}, waitTimeout(time.Minute))
	var output bytes.Buffer
	if _, err := New(registry).Run(t.Context(), definition, Options{DryRun: true, Stdout: &output, Stderr: io.Discard}); err != nil {
		t.Fatal(err)
	}
	if got := output.String(); got != "1. ready (wait) [poll probe every 5s, timeout 1m0s]\n" {
		t.Fatalf("output = %q", got)
	}
}

func TestWaitRunsInsideConcurrentGroupAndCompositeAction(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		registry := newTestRegistry(t, nil)
		definition := &workflow.Definition{
			Version: 1, Name: "concurrent-wait", Dir: "/workflow", Vars: map[string]any{}, Env: workflow.Environment{},
			Steps: []workflow.Step{{Concurrent: &workflow.ConcurrentGroup{
				MaxConcurrency: 2, FailFast: true,
				Steps: []workflow.Step{
					{ID: "one", Type: "wait", With: map[string]any{"duration": "1s"}},
					{ID: "two", Type: "wait", With: map[string]any{"duration": "2s"}},
				},
			}}},
		}
		state, err := New(registry).Run(t.Context(), definition, Options{})
		if err != nil {
			t.Fatal(err)
		}
		if state.Stats.Succeeded != 2 {
			t.Fatalf("stats = %#v", state.Stats)
		}

		action := &workflow.Action{
			Version: 1, Name: "wait-action", Dir: "/action",
			Outputs: map[string]workflow.ActionOutput{"finished": {Value: `"pause" in steps`}},
			Steps:   []workflow.Step{{ID: "pause", Type: "wait", With: map[string]any{"duration": "1s"}}},
		}
		caller := &workflow.Definition{
			Version: 1, Name: "caller", Dir: "/workflow", Vars: map[string]any{}, Env: workflow.Environment{},
			Steps: []workflow.Step{{
				ID: "remote", Uses: workflow.ActionSource{URL: "https://example.test/action"}, Action: action, With: map[string]any{},
			}},
		}
		state, err = New(registry).Run(t.Context(), caller, Options{})
		if err != nil {
			t.Fatal(err)
		}
		if state.Steps["remote"].(map[string]any)["finished"] != true {
			t.Fatalf("state = %#v", state)
		}
	})
}
