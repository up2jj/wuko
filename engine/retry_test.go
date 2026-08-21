package engine

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/workflow"
)

type retryTestRunner struct {
	failures int
	requests *[]step.Request
}

func (runner retryTestRunner) Run(_ context.Context, request step.Request) (step.Result, error) {
	*runner.requests = append(*runner.requests, request)
	if request.Attempt <= runner.failures {
		return step.Result{Outputs: map[string]any{"failed": request.Attempt}, Variables: map[string]any{"leaked": true}}, errors.New("temporary failure")
	}
	return step.Result{Outputs: map[string]any{"attempt": request.Attempt}, Variables: map[string]any{"committed": true}}, nil
}

func immediateRetry(maximum int) *workflow.RetryPolicy {
	return &workflow.RetryPolicy{MaxAttempts: maximum, BackoffMultiplier: 1}
}

func TestRunRetriesAndCommitsOnlySuccessfulAttempt(t *testing.T) {
	registry := step.NewRegistry()
	var requests []step.Request
	if err := registry.Register("retry", func(map[string]any) (step.Runner, error) {
		return retryTestRunner{failures: 2, requests: &requests}, nil
	}); err != nil {
		t.Fatal(err)
	}
	definition := &workflow.Definition{Version: 1, Name: "retry", Dir: t.TempDir(), Vars: map[string]any{"operation": "release-42"}, Steps: []workflow.Step{{
		ID: "publish", Type: "retry", Retry: immediateRetry(3), With: map[string]any{},
	}}}
	definition.Steps[0].Retry.OperationID = "{{ .vars.operation }}"
	var diagnostics strings.Builder
	state, err := New(registry).Run(t.Context(), definition, Options{RunDir: t.TempDir(), Stdout: io.Discard, Stderr: &diagnostics})
	if err != nil {
		t.Fatal(err)
	}
	if len(requests) != 3 {
		t.Fatalf("attempts = %d, want 3", len(requests))
	}
	for i, request := range requests {
		if request.Attempt != i+1 || request.MaxAttempts != 3 || request.OperationID != "release-42" {
			t.Fatalf("request %d = %#v", i+1, request)
		}
		if i == 0 && request.PreviousAttempt != nil {
			t.Fatalf("first request previous attempt = %#v", request.PreviousAttempt)
		}
		if i > 0 && (request.PreviousAttempt == nil || request.PreviousAttempt.Outputs["failed"] != i) {
			t.Fatalf("request %d previous attempt = %#v", i+1, request.PreviousAttempt)
		}
	}
	if _, exists := state.Vars["leaked"]; exists {
		t.Fatal("failed-attempt variable was committed")
	}
	if state.Vars["committed"] != true || state.Steps["publish"].(map[string]any)["attempt"] != 3 {
		t.Fatalf("state = %#v", state)
	}
	if got := diagnostics.String(); !strings.Contains(got, "attempt 1/3 failed") || !strings.Contains(got, "retrying in 0s") {
		t.Fatalf("diagnostics = %q", got)
	}
}

type timeoutThenSuccessRunner struct{ attempts int }

func (runner *timeoutThenSuccessRunner) Run(ctx context.Context, _ step.Request) (step.Result, error) {
	runner.attempts++
	if runner.attempts == 1 {
		<-ctx.Done()
		return step.Result{}, ctx.Err()
	}
	return step.Result{Outputs: map[string]any{"ok": true}}, nil
}

func TestRunRetriesAttemptTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		registry := step.NewRegistry()
		runner := &timeoutThenSuccessRunner{}
		if err := registry.Register("timeout", func(map[string]any) (step.Runner, error) { return runner, nil }); err != nil {
			t.Fatal(err)
		}
		timeout := workflow.Duration(time.Second)
		definition := &workflow.Definition{Version: 1, Name: "timeout", Dir: t.TempDir(), Steps: []workflow.Step{{
			ID: "run", Type: "timeout", Timeout: &timeout, Retry: immediateRetry(2), With: map[string]any{},
		}}}
		state, err := New(registry).Run(t.Context(), definition, Options{Stdout: io.Discard, Stderr: io.Discard})
		if err != nil {
			t.Fatal(err)
		}
		if runner.attempts != 2 {
			t.Fatalf("attempts = %d", runner.attempts)
		}
		if state.Stats.TimedOut != 1 || state.Stats.Retries != 1 {
			t.Fatalf("stats = %#v", state.Stats)
		}
	})
}

type canceledThenSuccessRunner struct{ attempts int }

func (runner *canceledThenSuccessRunner) Run(_ context.Context, _ step.Request) (step.Result, error) {
	runner.attempts++
	if runner.attempts == 1 {
		return step.Result{}, context.Canceled
	}
	return step.Result{Outputs: map[string]any{"ok": true}}, nil
}

func TestRunRetriesRunnerCancellationWhileWorkflowActive(t *testing.T) {
	registry := step.NewRegistry()
	runner := &canceledThenSuccessRunner{}
	if err := registry.Register("local_cancel", func(map[string]any) (step.Runner, error) { return runner, nil }); err != nil {
		t.Fatal(err)
	}
	definition := &workflow.Definition{Version: 1, Name: "local-cancel", Dir: t.TempDir(), Steps: []workflow.Step{{
		ID: "run", Type: "local_cancel", Retry: immediateRetry(2), With: map[string]any{},
	}}}
	state, err := New(registry).Run(t.Context(), definition, Options{Stdout: io.Discard, Stderr: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if runner.attempts != 2 || state.Steps["run"].(map[string]any)["ok"] != true {
		t.Fatalf("attempts = %d, state = %#v", runner.attempts, state)
	}
}

type contextRunner struct{ calls int }

func (runner *contextRunner) Run(ctx context.Context, _ step.Request) (step.Result, error) {
	runner.calls++
	<-ctx.Done()
	return step.Result{}, ctx.Err()
}

func TestRunStopsAtMaxElapsedTime(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		registry := step.NewRegistry()
		runner := &contextRunner{}
		if err := registry.Register("blocking", func(map[string]any) (step.Runner, error) { return runner, nil }); err != nil {
			t.Fatal(err)
		}
		policy := immediateRetry(3)
		policy.MaxElapsedTime = workflow.Duration(2 * time.Second)
		definition := &workflow.Definition{Version: 1, Name: "budget", Dir: t.TempDir(), Steps: []workflow.Step{{ID: "wait", Type: "blocking", Retry: policy, With: map[string]any{}}}}
		_, err := New(registry).Run(t.Context(), definition, Options{Stdout: io.Discard, Stderr: io.Discard})
		if err == nil || !strings.Contains(err.Error(), "max_elapsed_time 2s exceeded") {
			t.Fatalf("error = %v", err)
		}
		if runner.calls != 1 {
			t.Fatalf("calls = %d, want 1", runner.calls)
		}
	})
}

func TestRunDoesNotRetryParentCancellation(t *testing.T) {
	registry := step.NewRegistry()
	var requests []step.Request
	if err := registry.Register("retry", func(map[string]any) (step.Runner, error) {
		return retryTestRunner{failures: 3, requests: &requests}, nil
	}); err != nil {
		t.Fatal(err)
	}
	definition := &workflow.Definition{Version: 1, Name: "cancel", Dir: t.TempDir(), Steps: []workflow.Step{{ID: "run", Type: "retry", Retry: immediateRetry(3), With: map[string]any{}}}}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := New(registry).Run(ctx, definition, Options{Stdout: io.Discard, Stderr: io.Discard})
	if !errors.Is(err, context.Canceled) || len(requests) != 0 {
		t.Fatalf("error = %v, attempts = %d", err, len(requests))
	}
}

type retryHTTPError struct {
	method     string
	status     int
	retryAfter time.Duration
}

func (err retryHTTPError) Error() string                 { return "HTTP failure" }
func (err retryHTTPError) HTTPRequestMethod() string     { return err.method }
func (err retryHTTPError) HTTPStatusCode() int           { return err.status }
func (err retryHTTPError) HTTPRetryAfter() time.Duration { return err.retryAfter }

func TestHTTPRetryEligibility(t *testing.T) {
	tests := []struct {
		name   string
		policy *workflow.RetryPolicy
		err    error
		want   bool
	}{
		{name: "default GET transport", policy: immediateRetry(2), err: retryHTTPError{method: "GET"}, want: true},
		{name: "default GET transient status", policy: immediateRetry(2), err: retryHTTPError{method: "GET", status: 503}, want: true},
		{name: "default GET permanent status", policy: immediateRetry(2), err: retryHTTPError{method: "GET", status: 404}},
		{name: "default POST", policy: immediateRetry(2), err: retryHTTPError{method: "POST", status: 503}},
		{name: "POST override", policy: &workflow.RetryPolicy{MaxAttempts: 2, Methods: []string{"POST"}, Statuses: []workflow.StatusRange{{From: 500, To: 504}}}, err: retryHTTPError{method: "POST", status: 502}, want: true},
		{name: "wrapped GET timeout", policy: immediateRetry(2), err: attemptTimeoutError{duration: time.Second, cause: retryHTTPError{method: "GET"}}, want: true},
		{name: "terminal HTTP error", policy: immediateRetry(2), err: errors.New("decoding response")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			workflowStep := workflow.Step{Type: "http", Retry: test.policy}
			if got := shouldRetry(workflowStep, test.err); got != test.want {
				t.Fatalf("shouldRetry() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestRetryAfterExtendsBackoffWithinMaxDelay(t *testing.T) {
	policy := &workflow.RetryPolicy{
		InitialDelay: workflow.Duration(time.Second), BackoffMultiplier: 1, MaxDelay: workflow.Duration(5 * time.Second),
	}
	if got := retryDelayForError(policy, 1, retryHTTPError{retryAfter: 3 * time.Second}); got != 3*time.Second {
		t.Fatalf("delay = %s, want 3s", got)
	}
	if got := retryDelayForError(policy, 1, retryHTTPError{retryAfter: 10 * time.Second}); got != 5*time.Second {
		t.Fatalf("capped delay = %s, want 5s", got)
	}
}
