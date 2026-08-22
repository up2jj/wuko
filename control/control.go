// Package control expands and schedules workflow fan-out controls without depending on the
// workflow or execution engine packages.
package control

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"time"

	"github.com/expr-lang/expr"
	wukoexpr "github.com/up2jj/wuko/expression"
	"golang.org/x/sync/errgroup"
)

const (
	// DefaultMaxIterations bounds fan-out expansion when a workflow does not choose a lower limit.
	DefaultMaxIterations = 10_000
	// MaxIterations is the largest fan-out expansion a workflow may explicitly request.
	MaxIterations = 1_000_000
)

// Policy controls scheduling of independent iterations.
type Policy struct {
	MaxConcurrency int
	Timeout        time.Duration
	FailFast       bool
}

// Validate checks the fan-out execution limits.
func (policy Policy) Validate() error {
	if policy.MaxConcurrency < 1 || policy.MaxConcurrency > 100 {
		return fmt.Errorf("max_concurrency must be between 1 and 100")
	}
	if policy.Timeout < 0 {
		return fmt.Errorf("timeout cannot be negative")
	}
	return nil
}

// Iteration is one zero-based execution with template and expression root bindings.
type Iteration struct {
	Index    int
	Bindings map[string]any
}

// Axis is one ordered matrix dimension.
type Axis struct {
	Name   string
	Values []any
}

// Batch expands an ordered collection into fixed-size batch bindings.
func Batch(items any, size int) ([]Iteration, error) {
	return BatchContext(context.Background(), items, size, DefaultMaxIterations)
}

// BatchContext expands fixed-size chunks up to maxIterations while honoring cancellation.
func BatchContext(ctx context.Context, items any, size, maxIterations int) ([]Iteration, error) {
	if err := validateMaxIterations(maxIterations); err != nil {
		return nil, err
	}
	if size < 1 {
		return nil, fmt.Errorf("batch size must be a positive integer")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	values, ok := asSlice(items)
	if !ok {
		return nil, fmt.Errorf("items returned %T, want list or array", items)
	}
	count := len(values) / size
	if len(values)%size != 0 {
		count++
	}
	if count > maxIterations {
		return nil, fmt.Errorf("batch iteration count %d exceeds max_iterations %d", count, maxIterations)
	}
	iterations := make([]Iteration, 0, count)
	for start := 0; start < len(values); start += size {
		end := min(start+size, len(values))
		items := make([]any, end-start)
		for i, item := range values[start:end] {
			cloned, err := cloneContext(ctx, item)
			if err != nil {
				return nil, err
			}
			items[i] = cloned
		}
		index := len(iterations)
		iterations = append(iterations, Iteration{Index: index, Bindings: map[string]any{
			"batch": map[string]any{"index": index, "items": items},
		}})
	}
	return iterations, nil
}

// Foreach expands an ordered collection into foreach bindings.
func Foreach(items any) ([]Iteration, error) {
	return ForeachContext(context.Background(), items, DefaultMaxIterations)
}

// ForeachContext expands an ordered collection up to maxIterations while honoring cancellation.
func ForeachContext(ctx context.Context, items any, maxIterations int) ([]Iteration, error) {
	if err := validateMaxIterations(maxIterations); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	values, ok := asSlice(items)
	if !ok {
		return nil, fmt.Errorf("items returned %T, want list or array", items)
	}
	if len(values) > maxIterations {
		return nil, fmt.Errorf("foreach iteration count %d exceeds max_iterations %d", len(values), maxIterations)
	}
	iterations := make([]Iteration, len(values))
	for i, item := range values {
		cloned, err := cloneContext(ctx, item)
		if err != nil {
			return nil, err
		}
		iterations[i] = Iteration{Index: i, Bindings: map[string]any{
			"foreach": map[string]any{"index": i, "item": cloned},
		}}
	}
	return iterations, nil
}

// Matrix expands ordered axes into a Cartesian product. The rightmost axis changes fastest.
func Matrix(axes []Axis) ([]Iteration, error) {
	return MatrixContext(context.Background(), axes, DefaultMaxIterations)
}

// MatrixContext expands an ordered Cartesian product up to maxIterations while honoring cancellation.
func MatrixContext(ctx context.Context, axes []Axis, maxIterations int) ([]Iteration, error) {
	if err := validateMaxIterations(maxIterations); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(axes) == 0 {
		return nil, fmt.Errorf("matrix requires at least one axis")
	}
	seen := make(map[string]struct{}, len(axes))
	empty := false
	for _, axis := range axes {
		if axis.Name == "" {
			return nil, fmt.Errorf("matrix axis name must not be empty")
		}
		if len(axis.Values) == 0 {
			empty = true
		}
		if _, exists := seen[axis.Name]; exists {
			return nil, fmt.Errorf("duplicate matrix axis %q", axis.Name)
		}
		seen[axis.Name] = struct{}{}
	}
	if empty {
		return []Iteration{}, nil
	}
	count := 1
	for _, axis := range axes {
		if len(axis.Values) > maxIterations/count {
			return nil, fmt.Errorf("matrix combination count exceeds max_iterations %d", maxIterations)
		}
		count *= len(axis.Values)
	}
	iterations := make([]Iteration, 0, count)
	var expand func(int, map[string]any) error
	expand = func(axisIndex int, binding map[string]any) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if axisIndex == len(axes) {
			index := len(iterations)
			cloned, err := cloneMapContext(ctx, binding)
			if err != nil {
				return err
			}
			iterations = append(iterations, Iteration{Index: index, Bindings: map[string]any{"matrix": cloned}})
			return nil
		}
		axis := axes[axisIndex]
		for _, value := range axis.Values {
			cloned, err := cloneContext(ctx, value)
			if err != nil {
				return err
			}
			binding[axis.Name] = cloned
			if err := expand(axisIndex+1, binding); err != nil {
				return err
			}
		}
		delete(binding, axis.Name)
		return nil
	}
	if err := expand(0, make(map[string]any, len(axes))); err != nil {
		return nil, err
	}
	return iterations, nil
}

func validateMaxIterations(maxIterations int) error {
	if maxIterations < 1 || maxIterations > MaxIterations {
		return fmt.Errorf("max_iterations must be between 1 and %d", MaxIterations)
	}
	return nil
}

// ValidateExpression compiles a collection expression without requiring runtime roots.
func ValidateExpression(expression string) error {
	if _, err := wukoexpr.Compile(expression, expr.AllowUndefinedVariables()); err != nil {
		return err
	}
	return nil
}

// EvaluateExpression evaluates a typed expression against workflow runtime roots.
func EvaluateExpression(expression string, environment map[string]any) (any, error) {
	program, err := wukoexpr.Compile(expression, expr.Env(environment))
	if err != nil {
		return nil, err
	}
	return expr.Run(program, environment)
}

// EvaluateList evaluates an expression and requires a list or array result.
func EvaluateList(expression string, environment map[string]any) ([]any, error) {
	value, err := EvaluateExpression(expression, environment)
	if err != nil {
		return nil, err
	}
	values, ok := asSlice(value)
	if !ok {
		return nil, fmt.Errorf("expression returned %T, want list or array", value)
	}
	return values, nil
}

// EventKind identifies an iteration lifecycle event.
type EventKind string

const (
	IterationStarted  EventKind = "iteration_started"
	IterationFinished EventKind = "iteration_finished"
)

// Event reports iteration lifecycle without exposing binding values.
type Event struct {
	Kind     EventKind
	Index    int
	Started  time.Time
	Duration time.Duration
	Err      error
}

// Observer receives serialized lifecycle events.
type Observer func(Event)

// Outcome records one iteration result in declaration order.
type Outcome[T any] struct {
	Iteration Iteration
	Value     T
	Started   bool
	StartedAt time.Time
	Duration  time.Duration
	Err       error
}

// Run executes iterations with bounded concurrency and deterministic outcomes and errors.
func Run[T any](ctx context.Context, iterations []Iteration, policy Policy, observer Observer, execute func(context.Context, Iteration) (T, error)) ([]Outcome[T], error) {
	if err := policy.Validate(); err != nil {
		return nil, err
	}
	if execute == nil {
		return nil, fmt.Errorf("iteration executor is required")
	}
	for i, iteration := range iterations {
		if iteration.Index != i {
			return nil, fmt.Errorf("iteration %d has index %d, want %d", i, iteration.Index, i)
		}
	}
	runCtx := ctx
	cancel := func() {}
	if policy.Timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, policy.Timeout)
	}
	defer cancel()

	outcomes := make([]Outcome[T], len(iterations))
	var observerMu sync.Mutex
	notify := func(event Event) {
		if observer == nil {
			return
		}
		observerMu.Lock()
		defer observerMu.Unlock()
		observer(event)
	}
	runOne := func(iterationCtx context.Context, iteration Iteration) error {
		if err := iterationCtx.Err(); err != nil {
			return err
		}
		started := time.Now()
		outcomes[iteration.Index] = Outcome[T]{Iteration: iteration, Started: true, StartedAt: started}
		notify(Event{Kind: IterationStarted, Index: iteration.Index, Started: started})
		value, err := execute(iterationCtx, iteration)
		outcome := &outcomes[iteration.Index]
		outcome.Value = value
		outcome.Err = err
		outcome.Duration = time.Since(started)
		notify(Event{Kind: IterationFinished, Index: iteration.Index, Started: started, Duration: outcome.Duration, Err: err})
		return err
	}

	if policy.FailFast {
		groupCtx, cancelGroup := context.WithCancel(runCtx)
		defer cancelGroup()
		var group errgroup.Group
		slots := make(chan struct{}, min(policy.MaxConcurrency, max(1, len(iterations))))
	failFastLoop:
		for _, iteration := range iterations {
			select {
			case slots <- struct{}{}:
				if groupCtx.Err() != nil {
					<-slots
					break failFastLoop
				}
			case <-groupCtx.Done():
				break failFastLoop
			}
			group.Go(func() error {
				err := runOne(groupCtx, iteration)
				if err != nil {
					cancelGroup()
				}
				<-slots
				return err
			})
		}
		_ = group.Wait()
	} else {
		var group errgroup.Group
		slots := make(chan struct{}, min(policy.MaxConcurrency, max(1, len(iterations))))
	collectLoop:
		for _, iteration := range iterations {
			select {
			case slots <- struct{}{}:
				if runCtx.Err() != nil {
					<-slots
					break collectLoop
				}
			case <-runCtx.Done():
				break collectLoop
			}
			group.Go(func() error {
				_ = runOne(runCtx, iteration)
				<-slots
				return nil
			})
		}
		_ = group.Wait()
	}

	if err := ctx.Err(); err != nil {
		return outcomes, err
	}
	if policy.Timeout > 0 && runCtx.Err() == context.DeadlineExceeded {
		return outcomes, fmt.Errorf("timed out after %s: %w", policy.Timeout, context.DeadlineExceeded)
	}
	iterationErrors := make([]error, 0, len(outcomes))
	for _, outcome := range outcomes {
		if outcome.Err != nil && !(policy.FailFast && errors.Is(outcome.Err, context.Canceled)) {
			iterationErrors = append(iterationErrors, fmt.Errorf("iteration %d: %w", outcome.Iteration.Index, outcome.Err))
		}
	}
	if len(iterationErrors) == 0 && policy.FailFast {
		for _, outcome := range outcomes {
			if outcome.Err != nil {
				iterationErrors = append(iterationErrors, fmt.Errorf("iteration %d: %w", outcome.Iteration.Index, outcome.Err))
			}
		}
	}
	return outcomes, errors.Join(iterationErrors...)
}

func asSlice(value any) ([]any, bool) {
	if values, ok := value.([]any); ok {
		return values, true
	}
	reflected := reflect.ValueOf(value)
	if !reflected.IsValid() || (reflected.Kind() != reflect.Slice && reflected.Kind() != reflect.Array) {
		return nil, false
	}
	values := make([]any, reflected.Len())
	for i := range reflected.Len() {
		values[i] = reflected.Index(i).Interface()
	}
	return values, true
}

func cloneMap(source map[string]any) map[string]any {
	result, _ := cloneMapContext(context.Background(), source)
	return result
}

func clone(value any) any {
	result, _ := cloneContext(context.Background(), value)
	return result
}

func cloneMapContext(ctx context.Context, source map[string]any) (map[string]any, error) {
	result := make(map[string]any, len(source))
	for key, value := range source {
		cloned, err := cloneContext(ctx, value)
		if err != nil {
			return nil, err
		}
		result[key] = cloned
	}
	return result, nil
}

func cloneContext(ctx context.Context, value any) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	switch typed := value.(type) {
	case map[string]any:
		return cloneMapContext(ctx, typed)
	case []any:
		result := make([]any, len(typed))
		for i, item := range typed {
			cloned, err := cloneContext(ctx, item)
			if err != nil {
				return nil, err
			}
			result[i] = cloned
		}
		return result, nil
	default:
		return value, nil
	}
}
