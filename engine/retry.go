package engine

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"math"
	randv2 "math/rand/v2"
	"strings"
	"time"

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

func (e *Engine) runWithRetry(ctx context.Context, definition *workflow.Definition, workflowStep workflow.Step, options Options, state *State, execute stepExecutor) stepExecution {
	var execution stepExecution
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
		result, runErr := execute(attemptCtx, request)
		attemptContextErr := attemptCtx.Err()
		cancelAttempt()

		if contextErr := executionContextError(ctx, runCtx, workflowStep); contextErr != nil {
			runErr = contextErr
		} else if attemptContextErr == context.DeadlineExceeded && workflowStep.Timeout != nil {
			runErr = fmt.Errorf("timed out after %s: %w", workflowStep.Timeout, context.DeadlineExceeded)
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

		delay := retryDelay(workflowStep.Retry, attempt)
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
		operationID, err := workflow.RenderString(workflowStep.Retry.OperationID, templateData(definition, options.RunDir, state))
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
