package engine

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"maps"
	"math"
	randv2 "math/rand/v2"
	"slices"
	"strings"
	"time"

	"github.com/expr-lang/expr/vm"
	"github.com/up2jj/wuko/diagnostic"
	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/workflow"
)

type stepExecutor func(context.Context, step.Request) (step.Result, error)

type stepExecution struct {
	result    step.Result
	attempts  []AttemptStats
	retryWait time.Duration
	err       error
}

type httpRetryError interface {
	error
	HTTPRequestMethod() string
	HTTPStatusCode() int
	HTTPRetryAfter() time.Duration
}

// retryOutputProvider lets a failure describe the outputs a retry condition should see when the
// step itself produced none.
type retryOutputProvider interface {
	error
	RetryConditionOutputs() map[string]any
}

type attemptTimeoutError struct {
	duration time.Duration
	cause    error
}

func (err attemptTimeoutError) Error() string {
	return fmt.Sprintf("timed out after %s: %v", err.duration, context.DeadlineExceeded)
}

func (err attemptTimeoutError) Unwrap() []error {
	if err.cause == nil {
		return []error{context.DeadlineExceeded}
	}
	return []error{context.DeadlineExceeded, err.cause}
}

var defaultHTTPRetryMethods = []string{"GET", "HEAD", "OPTIONS", "PUT", "DELETE", "TRACE"}

var defaultHTTPRetryStatuses = []workflow.StatusRange{
	{From: 408, To: 408},
	{From: 425, To: 425},
	{From: 429, To: 429},
	{From: 500, To: 599},
}

func (e *Engine) runWithRetry(ctx context.Context, definition *workflow.Definition, workflowStep workflow.Step, options Options, state *State, execute stepExecutor) stepExecution {
	var execution stepExecution
	var previousAttempt *step.Result
	var retryWhen *vm.Program
	if workflowStep.Retry != nil && workflowStep.Retry.When != "" {
		var err error
		retryWhen, err = e.compileCondition(workflowStep.Retry.When)
		if err != nil {
			execution.err = fmt.Errorf("compiling retry when: %w", err)
			return execution
		}
	}
	operationID, err := executionOperationID(definition, workflowStep, options, state)
	if err != nil {
		traceStep(options, definition, workflowStep, diagnostic.PhaseAttempt, diagnostic.StatusFailed, time.Time{}, "preparing operation", err)
		execution.err = err
		return execution
	}
	maximum := maxAttempts(workflowStep)
	runCtx := ctx
	cancelRun := func() {}
	if workflowStep.Retry != nil && workflowStep.Retry.MaxElapsedTime.Value() > 0 {
		runCtx, cancelRun = context.WithTimeout(ctx, workflowStep.Retry.MaxElapsedTime.Value())
	}
	defer cancelRun()

	for attempt := 1; attempt <= maximum; attempt++ {
		if err := executionContextError(ctx, runCtx, workflowStep); err != nil {
			execution.err = err
			return execution
		}
		attemptCtx := runCtx
		cancelAttempt := func() {}
		if workflowStep.Timeout != nil {
			attemptCtx, cancelAttempt = context.WithTimeout(runCtx, workflowStep.Timeout.Value())
		}
		attemptStartedAt := time.Now()
		traceStep(options, definition, workflowStep, diagnostic.PhaseAttempt, diagnostic.StatusStarted, time.Time{}, "executing step", nil, attemptAttr(attempt, maximum))
		report(options, ProgressEvent{
			Kind: AttemptStarted, Status: StatusRunning, Time: attemptStartedAt,
			WorkflowName: definition.Name, Depth: options.depth, StepID: workflowStep.ID,
			StepType: executionKind(workflowStep), Attempt: attempt, MaxAttempts: maximum,
		})
		request := makeRequest(definition, workflowStep.ID, options, state, attempt, maximum, operationID)
		request.PreviousAttempt = previousAttempt
		result, runErr := execute(attemptCtx, request)
		attemptContextErr := attemptCtx.Err()
		cancelAttempt()

		if contextErr := executionContextError(ctx, runCtx, workflowStep); contextErr != nil {
			runErr = contextErr
		} else if attemptContextErr == context.DeadlineExceeded && workflowStep.Timeout != nil {
			runErr = attemptTimeoutError{duration: workflowStep.Timeout.Value(), cause: runErr}
		}
		attemptStats := AttemptStats{
			Number: attempt, Status: statusFromError(runErr), StartedAt: attemptStartedAt,
			Duration: time.Since(attemptStartedAt), Error: runErr,
		}
		execution.attempts = append(execution.attempts, attemptStats)
		diagnosticStatus := diagnostic.StatusSucceeded
		if runErr != nil {
			diagnosticStatus = diagnostic.StatusFailed
		}
		traceStep(options, definition, workflowStep, diagnostic.PhaseAttempt, diagnosticStatus, attemptStartedAt, "", runErr, attemptAttr(attempt, maximum))
		report(options, ProgressEvent{
			Kind: AttemptFinished, Status: attemptStats.Status, Time: attemptStartedAt.Add(attemptStats.Duration),
			WorkflowName: definition.Name, Depth: options.depth, StepID: workflowStep.ID,
			StepType: executionKind(workflowStep), Attempt: attempt, MaxAttempts: maximum,
			Duration: attemptStats.Duration, Error: runErr,
		})
		if runErr == nil {
			execution.result = result
			return execution
		}
		if ctx.Err() != nil || runCtx.Err() != nil {
			execution.err = runErr
			return execution
		}
		if attempt == maximum {
			execution.err = fmt.Errorf("attempt %d/%d failed: %w", attempt, maximum, runErr)
			return execution
		}
		if retryWhen != nil {
			environment := makeConditionEnvironment(definition, options.RunDir, state)
			environment["error"] = retryErrorValue(attemptStats.Status, workflowStep.ID, executionKind(workflowStep), runErr, retryConditionOutputs(runErr, result.Outputs))
			retry, err := evaluateConditionProgram(retryWhen, environment)
			if err != nil {
				execution.err = fmt.Errorf("evaluating retry when after %v: %w", runErr, err)
				return execution
			}
			if !retry {
				execution.err = runErr
				return execution
			}
		} else if !shouldRetry(workflowStep, runErr) {
			execution.err = runErr
			return execution
		}
		if result.Outputs != nil {
			completed := result
			previousAttempt = &completed
		}

		delay := retryDelayForError(workflowStep.Retry, attempt, runErr)
		traceStep(options, definition, workflowStep, diagnostic.PhaseRetry, diagnostic.StatusDetail, time.Time{}, "retry scheduled", nil,
			attemptAttr(attempt+1, maximum), diagnostic.Attr("delay", delay.String()))
		report(options, ProgressEvent{
			Kind: RetryScheduled, Status: StatusRunning, Time: time.Now(),
			WorkflowName: definition.Name, Depth: options.depth, StepID: workflowStep.ID,
			StepType: executionKind(workflowStep), Attempt: attempt + 1, MaxAttempts: maximum,
			RetryDelay: delay, Error: runErr,
		})
		waitStartedAt := time.Now()
		if err := waitForRetry(runCtx, delay); err != nil {
			execution.retryWait += time.Since(waitStartedAt)
			if contextErr := executionContextError(ctx, runCtx, workflowStep); contextErr != nil {
				execution.err = contextErr
				return execution
			}
			execution.err = err
			return execution
		}
		execution.retryWait += time.Since(waitStartedAt)
	}
	panic("unreachable")
}

// retryConditionOutputs exposes a failed attempt's outputs to a retry condition. A failure that
// produces no outputs at all can still describe itself through retryOutputProvider, so conditions
// see the same shape whether or not the step got far enough to report anything. Reported outputs
// always win over the fallback.
func retryConditionOutputs(err error, outputs map[string]any) map[string]any {
	var provider retryOutputProvider
	if !errors.As(err, &provider) {
		return outputs
	}
	fallback := provider.RetryConditionOutputs()
	if len(fallback) == 0 {
		return outputs
	}
	merged := make(map[string]any, len(outputs)+len(fallback))
	maps.Copy(merged, fallback)
	maps.Copy(merged, outputs)
	return merged
}

func shouldRetry(workflowStep workflow.Step, err error) bool {
	if workflowStep.Type != "http" {
		return true
	}
	var retryErr httpRetryError
	if !errors.As(err, &retryErr) {
		return false
	}
	methods := workflowStep.Retry.Methods
	if len(methods) == 0 {
		methods = defaultHTTPRetryMethods
	}
	methodAllowed := slices.ContainsFunc(methods, func(method string) bool {
		return strings.EqualFold(method, retryErr.HTTPRequestMethod())
	})
	if !methodAllowed {
		return false
	}
	if retryErr.HTTPStatusCode() == 0 {
		return true
	}
	statuses := workflowStep.Retry.Statuses
	if len(statuses) == 0 {
		statuses = defaultHTTPRetryStatuses
	}
	return slices.ContainsFunc(statuses, func(status workflow.StatusRange) bool {
		return retryErr.HTTPStatusCode() >= status.From && retryErr.HTTPStatusCode() <= status.To
	})
}

func retryDelayForError(policy *workflow.RetryPolicy, failedAttempt int, err error) time.Duration {
	delay := retryDelay(policy, failedAttempt)
	var retryErr httpRetryError
	if errors.As(err, &retryErr) {
		delay = max(delay, retryErr.HTTPRetryAfter())
	}
	if policy != nil {
		delay = min(delay, policy.MaxDelay.Value())
	}
	return max(0, delay)
}

func maxAttempts(workflowStep workflow.Step) int {
	if workflowStep.Retry == nil {
		return 1
	}
	return workflowStep.Retry.MaxAttempts
}

func executionContextError(parent, runCtx context.Context, workflowStep workflow.Step) error {
	if err := parent.Err(); err != nil {
		return err
	}
	if err := runCtx.Err(); err != nil {
		if workflowStep.Retry != nil && workflowStep.Retry.MaxElapsedTime.Value() > 0 {
			return fmt.Errorf("retry max_elapsed_time %s exceeded: %w", workflowStep.Retry.MaxElapsedTime, err)
		}
		return err
	}
	return nil
}

func retryDelay(policy *workflow.RetryPolicy, failedAttempt int) time.Duration {
	if policy == nil || policy.InitialDelay.Value() == 0 {
		return 0
	}
	delay := float64(policy.InitialDelay.Value()) * math.Pow(policy.BackoffMultiplier, float64(failedAttempt-1))
	delay = min(delay, float64(policy.MaxDelay.Value()))
	if policy.Jitter > 0 {
		delay *= 1 + ((randv2.Float64()*2)-1)*policy.Jitter
	}
	delay = min(delay, float64(policy.MaxDelay.Value()))
	return max(0, time.Duration(delay))
}

func waitForRetry(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func executionOperationID(definition *workflow.Definition, workflowStep workflow.Step, options Options, state *State) (string, error) {
	if workflowStep.Retry != nil && workflowStep.Retry.OperationID != "" {
		operationID, err := options.renderer.Render(workflowStep.Retry.OperationID, templateData(definition, options.RunDir, state))
		if err != nil {
			return "", fmt.Errorf("rendering retry operation_id: %w", err)
		}
		if strings.TrimSpace(operationID) == "" {
			return "", fmt.Errorf("rendered retry operation_id is empty")
		}
		return operationID, nil
	}
	if options.operationPrefix != "" {
		digest := sha256.Sum256([]byte(options.operationPrefix + "\x00" + definition.Name + "\x00" + workflowStep.ID))
		return hex.EncodeToString(digest[:]), nil
	}
	random := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, random); err != nil {
		return "", fmt.Errorf("generating step operation ID: %w", err)
	}
	return hex.EncodeToString(random), nil
}

func executionPolicySuffix(workflowStep workflow.Step) string {
	var parts []string
	if workflowStep.Type == "wait" {
		if description := waitPolicyDescription(workflowStep); description != "" {
			parts = append(parts, description)
		}
	}
	if workflowStep.Timeout != nil {
		parts = append(parts, "timeout "+workflowStep.Timeout.String())
	}
	if workflowStep.Retry != nil {
		retry := fmt.Sprintf("%d attempts", workflowStep.Retry.MaxAttempts)
		if workflowStep.Retry.MaxElapsedTime.Value() > 0 {
			retry += " within " + workflowStep.Retry.MaxElapsedTime.String()
		}
		parts = append(parts, retry)
	}
	if len(parts) == 0 {
		return ""
	}
	return " [" + strings.Join(parts, ", ") + "]"
}
