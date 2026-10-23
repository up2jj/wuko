package transform

import (
	"context"
	"fmt"
	"io"

	"github.com/expr-lang/expr"
	"github.com/up2jj/wuko/step"
)

func (runner *Runner) Run(ctx context.Context, request step.Request) (step.Result, error) {
	if err := checkContext(ctx); err != nil {
		return step.Result{}, err
	}
	evaluation := newEvaluation(request)
	var current any
	var source itemStream
	var err error
	if runner.config.From.file != "" {
		var file io.Closer
		file, source, err = runner.openFileSource(ctx, request)
		if err != nil {
			return step.Result{}, err
		}
		defer file.Close()
	} else if current, err = runner.resolveSource(evaluation); err != nil {
		return step.Result{}, err
	}
	current, err = runner.runPipeline(ctx, evaluation, current, source)
	if err != nil {
		return step.Result{}, err
	}
	// normalizeResult builds new lists and objects, so current already shares nothing with
	// workflow state, and the engine clones outputs and variables again on commit.
	// Cloning here would deep-copy the whole result two more times.
	current, err = normalizeResult(current)
	if err != nil {
		return step.Result{}, err
	}
	result := step.Result{Outputs: map[string]any{"value": current}}
	if runner.config.Variable != "" {
		result.Variables = map[string]any{runner.config.Variable: current}
	}
	return result, nil
}

// resolveSource returns the source without copying it. The engine already gives each step
// request its own copy of workflow state, and no operation writes into a value it consumes:
// every operation builds new lists and objects, and normalizeResult copies the final result.
func (runner *Runner) resolveSource(request *evaluation) (any, error) {
	if runner.config.From.path != "" {
		value, err := step.Lookup(request.request, runner.config.From.path)
		if err != nil {
			return nil, fmt.Errorf("resolving from: %w", err)
		}
		return value, nil
	}
	if runner.sourceProgram == nil {
		return nil, fmt.Errorf("from.expr contains an unresolved template")
	}
	value, err := expr.Run(runner.sourceProgram, request.environment)
	if err != nil {
		return nil, fmt.Errorf("evaluating from.expr: %w", err)
	}
	return value, nil
}

func runOperation(ctx context.Context, request *evaluation, current any, operation operation) (any, error) {
	if operation.descriptor.accept&acceptList != 0 && kindOf(current) == kindList {
		list, err := toList(current)
		if err != nil {
			return nil, err
		}
		return runListOperation(ctx, request, list, operation)
	}
	if operation.descriptor.accept&acceptObject != 0 && kindOf(current) == kindObject {
		object, err := toObject(current)
		if err != nil {
			return nil, err
		}
		return runObjectOperation(ctx, request, object, operation)
	}
	return nil, fmt.Errorf("expected compatible list or object, got %T", current)
}
