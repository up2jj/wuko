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

func TestRunRetriesAndCommitsOnlySuccessfulAttempt(t *testing.T) {
	var requests []step.Request
	registry := newTestRegistry(t, map[string]step.Builder{"retry": func(map[string]any) (step.Runner, error) {
		return retryTestRunner{failures: 2, requests: &requests}, nil
	}})
	policy := immediateRetry(3)
	policy.OperationID = "{{ .vars.operation }}"
	definition := testDefinition(t, "retry", attemptStep("publish", policy, workflow.Step{Type: "retry"}))
	definition.Vars = map[string]any{"operation": "release-42"}
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
	if _, exists := attemptVars(state, "publish")["leaked"]; exists {
		t.Fatal("failed-attempt variable was committed")
	}
	if attemptVars(state, "publish")["committed"] != true || attemptBody(state, "publish", "publish_body")["attempt"] != 3 {
		t.Fatalf("state = %#v", state.Steps)
	}
	if got := diagnostics.String(); !strings.Contains(got, "attempt 1/3 failed") || !strings.Contains(got, "retrying in 0s") {
		t.Fatalf("diagnostics = %q", got)
	}
}

type conditionalRetryRunner struct {
	attempts int
	outputs  map[string]any
	err      error
}

func (runner *conditionalRetryRunner) Run(_ context.Context, request step.Request) (step.Result, error) {
	runner.attempts++
	if request.Attempt == 1 {
		return step.Result{Outputs: runner.outputs, Variables: map[string]any{"leaked": true}}, runner.err
	}
	return step.Result{Outputs: map[string]any{"attempt": request.Attempt}, Variables: map[string]any{"committed": true}}, nil
}

func TestRetryWhenUsesStructuredFailureAndNormalRoots(t *testing.T) {
	runner := &conditionalRetryRunner{
		outputs: map[string]any{
			"exit_code": 75, "stderr": "rate limit", "status": 503,
			"message": "output message", "errors": "output errors", "outputs": "output outputs",
		},
		err: errors.New("temporary failure"),
	}
	registry := newTestRegistry(t, map[string]step.Builder{
		"conditional": func(map[string]any) (step.Runner, error) { return runner, nil },
	})
	policy := immediateRetry(2)
	policy.When = `vars.retry && error.exit_code == 75 && error.stderr contains "rate limit" && error.status == "failed" && error.message contains "temporary failure" && error.step == "run" && error.type == "attempt" && error.outputs.status == 503 && error.outputs.message == "output message" && error.outputs.errors == "output errors" && error.outputs.outputs == "output outputs" && len(error.errors) == 1`
	definition := testDefinition(t, "conditional", attemptStep("run", policy, workflow.Step{Type: "conditional", With: map[string]any{}}))
	definition.Vars = map[string]any{"retry": true}

	state, err := New(registry).Run(t.Context(), definition, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if runner.attempts != 2 || attemptBody(state, "run", "run_body")["attempt"] != 2 {
		t.Fatalf("attempts = %d, state = %#v", runner.attempts, state.Steps)
	}
	if _, exists := attemptVars(state, "run")["leaked"]; exists {
		t.Fatal("failed-attempt variable was committed")
	}
}

func TestRetryWhenFalseReturnsOriginalFailure(t *testing.T) {
	runner := &conditionalRetryRunner{err: errors.New("permanent failure")}
	registry := newTestRegistry(t, map[string]step.Builder{
		"conditional": func(map[string]any) (step.Runner, error) { return runner, nil },
	})
	policy := immediateRetry(3)
	policy.When = "false"
	definition := testDefinition(t, "conditional", attemptStep("run", policy, workflow.Step{Type: "conditional", With: map[string]any{}}))

	_, err := New(registry).Run(t.Context(), definition, Options{})
	if err == nil || !strings.Contains(err.Error(), "permanent failure") || strings.Contains(err.Error(), "evaluating attempt when") {
		t.Fatalf("error = %v", err)
	}
	if runner.attempts != 1 {
		t.Fatalf("attempts = %d, want 1", runner.attempts)
	}
}

func TestRetryWhenEvaluationFailureIsTerminal(t *testing.T) {
	runner := &conditionalRetryRunner{outputs: map[string]any{"values": []any{1}}, err: errors.New("attempt failure")}
	registry := newTestRegistry(t, map[string]step.Builder{
		"conditional": func(map[string]any) (step.Runner, error) { return runner, nil },
	})
	policy := immediateRetry(3)
	policy.When = `error.outputs.values[2] == 1`
	definition := testDefinition(t, "conditional", attemptStep("run", policy, workflow.Step{Type: "conditional", With: map[string]any{}}))

	_, err := New(registry).Run(t.Context(), definition, Options{})
	if err == nil || !strings.Contains(err.Error(), "evaluating attempt when after") {
		t.Fatalf("error = %v", err)
	}
	if runner.attempts != 1 {
		t.Fatalf("attempts = %d, want 1", runner.attempts)
	}
}

func TestRetryWhenValidationRejectsInvalidOrNonBooleanExpression(t *testing.T) {
	tests := []struct {
		name string
		when workflow.Condition
	}{
		{name: "invalid", when: `error.`},
		{name: "non boolean", when: `"message"`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			registry := newTestRegistry(t, map[string]step.Builder{
				"conditional": func(map[string]any) (step.Runner, error) {
					return runnerFunc(func(context.Context, step.Request) (step.Result, error) {
						t.Fatal("runner executed after retry condition validation failure")
						return step.Result{}, nil
					}), nil
				},
			})
			policy := immediateRetry(2)
			policy.When = test.when
			definition := testDefinition(t, "invalid", attemptStep("run", policy, workflow.Step{Type: "conditional", With: map[string]any{}}))

			_, err := New(registry).Run(t.Context(), definition, Options{})
			if err == nil || !strings.Contains(err.Error(), "attempt when") {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestRetryWhenShadowsOuterCatchError(t *testing.T) {
	inner := &conditionalRetryRunner{err: errors.New("inner failure")}
	registry := newTestRegistry(t, map[string]step.Builder{
		"outer_fail": func(map[string]any) (step.Runner, error) {
			return runnerFunc(func(context.Context, step.Request) (step.Result, error) {
				return step.Result{}, errors.New("outer failure")
			}), nil
		},
		"inner_flaky": func(map[string]any) (step.Runner, error) { return inner, nil },
	})
	policy := immediateRetry(2)
	policy.When = `error.message contains "inner failure" && error.step == "recover"`
	definition := testDefinition(t, "shadow", tryCatchStep(
		[]workflow.Step{{ID: "deploy", Type: "outer_fail", With: map[string]any{}}},
		[]workflow.Step{attemptStep("recover", policy, workflow.Step{Type: "inner_flaky", With: map[string]any{}})},
	))

	state, err := New(registry).Run(t.Context(), definition, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if inner.attempts != 2 || state.Steps["deployment"].(map[string]any)["recovered"] != true {
		t.Fatalf("attempts = %d, state = %#v", inner.attempts, state.Steps)
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
		registry := newTestRegistry(t, nil)
		runner := &timeoutThenSuccessRunner{}
		if err := registry.Register("timeout", func(map[string]any) (step.Runner, error) { return runner, nil }); err != nil {
			t.Fatal(err)
		}
		timeout := workflow.Duration(time.Second)
		definition := testDefinition(t, "timeout", attemptStep("run", attemptTimeout(immediateRetry(2), &timeout), workflow.Step{Type: "timeout", With: map[string]any{}}))

		state, err := New(registry).Run(t.Context(), definition, Options{})
		if err != nil {
			t.Fatal(err)
		}
		if runner.attempts != 2 {
			t.Fatalf("attempts = %d", runner.attempts)
		}
		if state.Stats.TimedOut != 2 || state.Stats.Retries != 1 {
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
	registry := newTestRegistry(t, nil)
	runner := &canceledThenSuccessRunner{}
	if err := registry.Register("local_cancel", func(map[string]any) (step.Runner, error) { return runner, nil }); err != nil {
		t.Fatal(err)
	}
	definition := testDefinition(t, "local-cancel", attemptStep("run", immediateRetry(2), workflow.Step{Type: "local_cancel", With: map[string]any{}}))

	state, err := New(registry).Run(t.Context(), definition, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if runner.attempts != 2 || attemptBody(state, "run", "run_body")["ok"] != true {
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
		registry := newTestRegistry(t, nil)
		runner := &contextRunner{}
		if err := registry.Register("blocking", func(map[string]any) (step.Runner, error) { return runner, nil }); err != nil {
			t.Fatal(err)
		}
		policy := immediateRetry(3)
		policy.MaxElapsedTime = workflow.LiteralDuration(workflow.Duration(2 * time.Second))
		definition := testDefinition(t, "budget", attemptStep("wait", policy, workflow.Step{Type: "blocking", With: map[string]any{}}))
		_, err := New(registry).Run(t.Context(), definition, Options{})
		if err == nil || !strings.Contains(err.Error(), "max_elapsed_time 2s exceeded") {
			t.Fatalf("error = %v", err)
		}
		if runner.calls != 1 {
			t.Fatalf("calls = %d, want 1", runner.calls)
		}
	})
}

func TestRunDoesNotRetryParentCancellation(t *testing.T) {
	registry := newTestRegistry(t, nil)
	var requests []step.Request
	if err := registry.Register("retry", func(map[string]any) (step.Runner, error) {
		return retryTestRunner{failures: 3, requests: &requests}, nil
	}); err != nil {
		t.Fatal(err)
	}
	definition := testDefinition(t, "cancel", attemptStep("run", immediateRetry(3), workflow.Step{Type: "retry", With: map[string]any{}}))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := New(registry).Run(ctx, definition, Options{})
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
		policy workflow.AttemptControl
		err    error
		want   bool
	}{
		{name: "default GET transport", policy: immediateRetry(2), err: retryHTTPError{method: "GET"}, want: true},
		{name: "default GET transient status", policy: immediateRetry(2), err: retryHTTPError{method: "GET", status: 503}, want: true},
		{name: "default GET permanent status", policy: immediateRetry(2), err: retryHTTPError{method: "GET", status: 404}},
		{name: "default POST", policy: immediateRetry(2), err: retryHTTPError{method: "POST", status: 503}},
		{name: "POST override", policy: workflow.AttemptControl{MaxAttempts: workflow.LiteralCount(2), Methods: []string{"POST"}, Statuses: []workflow.StatusRange{{From: 500, To: 504}}}, err: retryHTTPError{method: "POST", status: 502}, want: true},
		{name: "wrapped GET timeout", policy: immediateRetry(2), err: attemptTimeoutError{duration: time.Second, cause: retryHTTPError{method: "GET"}}, want: true},
		{name: "non-HTTP failure is eligible", policy: immediateRetry(2), err: errors.New("decoding response"), want: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			control := test.policy
			if got := retryableError(&control, test.err); got != test.want {
				t.Fatalf("retryableError() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestRetryWhenOverridesHTTPFailureEligibility(t *testing.T) {
	tests := []struct {
		name        string
		method      string
		status      int
		when        workflow.Condition
		wantSuccess bool
	}{
		{name: "default permanent status", method: "GET", status: 404},
		{name: "condition retries permanent status", method: "GET", status: 404, when: "true", wantSuccess: true},
		{name: "condition retries non-idempotent method", method: "POST", status: 503, when: "true", wantSuccess: true},
		{name: "condition rejects transient status", method: "GET", status: 503, when: "false"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner := &conditionalRetryRunner{
				outputs: map[string]any{"status": test.status},
				err:     retryHTTPError{method: test.method, status: test.status},
			}
			registry := newTestRegistry(t, map[string]step.Builder{
				"http": func(map[string]any) (step.Runner, error) { return runner, nil },
			})
			policy := immediateRetry(2)
			policy.When = test.when
			definition := testDefinition(t, "http", attemptStep("request", policy, workflow.Step{Type: "http", With: map[string]any{}}))

			state, err := New(registry).Run(t.Context(), definition, Options{})
			if test.wantSuccess {
				if err != nil {
					t.Fatal(err)
				}
				if runner.attempts != 2 || attemptBody(state, "request", "request_body")["attempt"] != 2 {
					t.Fatalf("attempts = %d, state = %#v", runner.attempts, state.Steps)
				}
				return
			}
			if err == nil || runner.attempts != 1 {
				t.Fatalf("error = %v, attempts = %d", err, runner.attempts)
			}
		})
	}
}

type retryTransportError struct{ retryHTTPError }

func (retryTransportError) RetryConditionOutputs() map[string]any { return map[string]any{"status": 0} }

func TestRetryConditionOutputsPrefersReportedOutputs(t *testing.T) {
	failure := retryTransportError{retryHTTPError{method: "GET"}}
	if got := retryConditionOutputs(failure, nil)["status"]; got != 0 {
		t.Fatalf("status = %#v, want 0", got)
	}
	if got := retryConditionOutputs(failure, map[string]any{"status": 503})["status"]; got != 503 {
		t.Fatalf("status = %#v, want 503", got)
	}
	outputs := map[string]any{"exit_code": 1}
	if got := retryConditionOutputs(errors.New("boom"), outputs); len(got) != 1 || got["exit_code"] != 1 {
		t.Fatalf("outputs = %#v", got)
	}
}

func TestRetryWhenReportsTransportFailureAsZeroStatus(t *testing.T) {
	runner := &conditionalRetryRunner{err: retryTransportError{retryHTTPError{method: "GET"}}}
	registry := newTestRegistry(t, map[string]step.Builder{
		"http": func(map[string]any) (step.Runner, error) { return runner, nil },
	})
	policy := immediateRetry(2)
	policy.When = "error.outputs.status >= 500 || error.outputs.status == 0"
	definition := testDefinition(t, "http", attemptStep("request", policy, workflow.Step{Type: "http", With: map[string]any{}}))

	state, err := New(registry).Run(t.Context(), definition, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if runner.attempts != 2 || attemptBody(state, "request", "request_body")["attempt"] != 2 {
		t.Fatalf("attempts = %d, state = %#v", runner.attempts, state.Steps)
	}
}

func TestRetryAfterExtendsBackoffWithinMaxDelay(t *testing.T) {
	policy := workflow.ResolvedAttempt{
		InitialDelay: time.Second, BackoffMultiplier: 1, MaxDelay: 5 * time.Second,
	}
	if got := retryDelayForError(policy, 1, retryHTTPError{retryAfter: 3 * time.Second}); got != 3*time.Second {
		t.Fatalf("delay = %s, want 3s", got)
	}
	if got := retryDelayForError(policy, 1, retryHTTPError{retryAfter: 10 * time.Second}); got != 5*time.Second {
		t.Fatalf("capped delay = %s, want 5s", got)
	}
}
