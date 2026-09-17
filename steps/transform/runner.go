package transform

import (
	"context"
	"fmt"

	"github.com/expr-lang/expr"
	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/workflow"
)

func (runner *Runner) Run(ctx context.Context, request step.Request) (step.Result, error) {
	if err := checkContext(ctx); err != nil {
		return step.Result{}, err
	}
	evaluation := newEvaluation(request)
	current, err := runner.resolveSource(evaluation)
	if err != nil {
		return step.Result{}, err
	}
	for index := range runner.operations {
		if err := checkContext(ctx); err != nil {
			return step.Result{}, err
		}
		operation := runner.operations[index]
		if !operation.accepts(kindOf(current)) {
			return step.Result{}, fmt.Errorf("operation %d (%s): cannot consume %s", index+1, operation.name, kindOf(current))
		}
		current, err = runOperation(ctx, evaluation, current, operation)
		if err != nil {
			return step.Result{}, fmt.Errorf("operation %d (%s): %w", index+1, operation.name, err)
		}
	}
	// normalizeResult round-trips through JSON, so current already shares nothing with
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

func (runner *Runner) resolveSource(request *evaluation) (any, error) {
	if runner.config.From.path != "" {
		value, err := step.Lookup(request.request, runner.config.From.path)
		if err != nil {
			return nil, fmt.Errorf("resolving from: %w", err)
		}
		return workflow.Clone(value), nil
	}
	if runner.sourceProgram == nil {
		return nil, fmt.Errorf("from.expr contains an unresolved template")
	}
	value, err := expr.Run(runner.sourceProgram, request.environment)
	if err != nil {
		return nil, fmt.Errorf("evaluating from.expr: %w", err)
	}
	return workflow.Clone(value), nil
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
