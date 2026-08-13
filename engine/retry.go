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

	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/workflow"
)

type stepExecutor func(context.Context, step.Request) (step.Result, error)

func (e *Engine) runWithRetry(ctx context.Context, definition *workflow.Definition, workflowStep workflow.Step, options Options, state *State, execute stepExecutor) (step.Result, error) {
	operationID, err := executionOperationID(definition, workflowStep, options, state)
	if err != nil {
		return step.Result{}, err
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
			return step.Result{}, err
		}
		attemptCtx := runCtx
		cancelAttempt := func() {}
		if workflowStep.Timeout != nil {
			attemptCtx, cancelAttempt = context.WithTimeout(runCtx, workflowStep.Timeout.Value())
		}
		request := makeRequest(definition, workflowStep.ID, options, state, attempt, maximum, operationID)
		result, runErr := execute(attemptCtx, request)
		attemptContextErr := attemptCtx.Err()
		cancelAttempt()

		if err := executionContextError(ctx, runCtx, workflowStep); err != nil {
			return step.Result{}, err
		}
		if attemptContextErr == context.DeadlineExceeded && workflowStep.Timeout != nil {
			runErr = fmt.Errorf("timed out after %s: %w", workflowStep.Timeout, context.DeadlineExceeded)
		}
		if runErr == nil {
			return result, nil
		}
		if attempt == maximum {
			return step.Result{}, fmt.Errorf("attempt %d/%d failed: %w", attempt, maximum, runErr)
		}

		fmt.Fprintf(writerOrDiscard(options.Stderr), "%s: attempt %d/%d failed: %v\n", workflowStep.ID, attempt, maximum, runErr)
		delay := retryDelay(workflowStep.Retry, attempt)
		fmt.Fprintf(writerOrDiscard(options.Stderr), "%s: retrying in %s\n", workflowStep.ID, delay)
		if err := waitForRetry(runCtx, delay); err != nil {
			if contextErr := executionContextError(ctx, runCtx, workflowStep); contextErr != nil {
				return step.Result{}, contextErr
			}
			return step.Result{}, err
		}
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

func writerOrDiscard(writer io.Writer) io.Writer {
	if writer == nil {
		return io.Discard
	}
	return writer
}
