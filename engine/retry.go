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

// retryableError decides whether a failed pass is worth repeating. Classification comes from the
// error rather than from a declared step type: an attempt body is a sequence, so there is no one
// step whose type could gate this. A failure that carries HTTP request metadata is filtered by
// method and status; every other failure is eligible, which is what the old non-http default did.
func retryableError(control *workflow.AttemptControl, err error) bool {
	var retryErr httpRetryError
	if !errors.As(err, &retryErr) {
		return true
	}
	methods := control.Methods
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
	statuses := control.Statuses
	if len(statuses) == 0 {
		statuses = defaultHTTPRetryStatuses
	}
	return slices.ContainsFunc(statuses, func(status workflow.StatusRange) bool {
		return retryErr.HTTPStatusCode() >= status.From && retryErr.HTTPStatusCode() <= status.To
	})
}

func retryDelayForError(policy workflow.ResolvedAttempt, failedAttempt int, err error) time.Duration {
	delay := retryDelay(policy, failedAttempt)
	var retryErr httpRetryError
	if errors.As(err, &retryErr) {
		delay = max(delay, retryErr.HTTPRetryAfter())
	}
	delay = min(delay, policy.MaxDelay)
	return max(0, delay)
}

// executionContextError keeps parent cancellation ahead of the control's own budget, so a
// Ctrl-C is never reported as an exhausted max_elapsed_time.
func executionContextError(parent, runCtx context.Context, maxElapsed time.Duration) error {
	if err := parent.Err(); err != nil {
		return err
	}
	if err := runCtx.Err(); err != nil {
		if maxElapsed > 0 {
			return fmt.Errorf("attempt max_elapsed_time %s exceeded: %w", maxElapsed, err)
		}
		return err
	}
	return nil
}

func retryDelay(policy workflow.ResolvedAttempt, failedAttempt int) time.Duration {
	if policy.InitialDelay == 0 {
		return 0
	}
	delay := float64(policy.InitialDelay) * math.Pow(policy.BackoffMultiplier, float64(failedAttempt-1))
	delay = min(delay, float64(policy.MaxDelay))
	if policy.Jitter > 0 {
		delay *= 1 + ((randv2.Float64()*2)-1)*policy.Jitter
	}
	delay = min(delay, float64(policy.MaxDelay))
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

// executionOperationID derives the idempotency key a step or attempt exposes as
// WUKO_STEP_OPERATION_ID. An explicit template wins; inside an action the key is derived so it is
// stable across the caller's passes; otherwise it is random.
func executionOperationID(definition *workflow.Definition, stepID, template string, options Options, state *State) (string, error) {
	if template != "" {
		operationID, err := options.renderer.Render(template, templateData(definition, options.RunDir, state))
		if err != nil {
			return "", fmt.Errorf("rendering attempt operation_id: %w", err)
		}
		if strings.TrimSpace(operationID) == "" {
			return "", fmt.Errorf("rendered attempt operation_id is empty")
		}
		return operationID, nil
	}
	if options.operationPrefix != "" {
		digest := sha256.Sum256([]byte(options.operationPrefix + "\x00" + definition.Name + "\x00" + stepID))
		return hex.EncodeToString(digest[:]), nil
	}
	random := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, random); err != nil {
		return "", fmt.Errorf("generating step operation ID: %w", err)
	}
	return hex.EncodeToString(random), nil
}

// executionPolicySuffix renders a step's execution policy for dry runs and the tree view. Only an
// attempt control carries one now.
func executionPolicySuffix(workflowStep workflow.Step) string {
	description := attemptPolicyDescription(workflowStep.Attempt)
	if description == "" {
		return ""
	}
	return " [" + description + "]"
}
