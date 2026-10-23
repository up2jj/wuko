package transform

import (
	"context"
	"errors"
	"fmt"
	"iter"
)

// itemStream yields the items of a list one at a time. A failure is yielded once, as the
// error of the last pair, and ends the stream.
type itemStream = iter.Seq2[any, error]

// stageError is an error a pipeline stage has already labelled with its operation, so the
// stages downstream of it pass it on unchanged.
type stageError struct{ err error }

func (err stageError) Error() string { return err.err.Error() }
func (err stageError) Unwrap() error { return err.err }

func labelStageError(position int, operation operation, err error) error {
	var labelled stageError
	if errors.As(err, &labelled) {
		return err
	}
	return stageError{fmt.Errorf("operation %d (%s): %w", position+1, operation.name, err)}
}

// unlabelStageError returns the labelled error a pipeline produced, without the marker.
func unlabelStageError(err error) error {
	var labelled stageError
	if errors.As(err, &labelled) {
		return labelled.err
	}
	return err
}

// listStream yields the items of an in-memory list.
func listStream(ctx context.Context, list []any) itemStream {
	return func(yield func(any, error) bool) {
		for _, item := range list {
			if err := checkContext(ctx); err != nil {
				yield(nil, err)
				return
			}
			if !yield(item, nil) {
				return
			}
		}
	}
}

// collectAtMost gathers up to limit items, or every item when limit is negative, and stops
// pulling from the stream once it has them.
func collectAtMost(stream itemStream, limit int) ([]any, error) {
	result := make([]any, 0)
	if limit == 0 {
		return result, nil
	}
	for item, err := range stream {
		if err != nil {
			return nil, err
		}
		result = append(result, item)
		if limit > 0 && len(result) == limit {
			break
		}
	}
	return result, nil
}

func collect(stream itemStream) ([]any, error) { return collectAtMost(stream, -1) }

// runPipeline applies the operations to the source. While the value flowing between
// operations is a list, operations that can work item by item are chained lazily: no
// intermediate list is built, and an operation that needs only a prefix of its input, such
// as take or find, stops the operations before it from evaluating the rest. Any other
// operation receives the collected list and runs exactly as it always has.
func (runner *Runner) runPipeline(ctx context.Context, request *evaluation, source any) (any, error) {
	current := source
	var stream itemStream
	for position, operation := range runner.operations {
		if err := checkContext(ctx); err != nil {
			return nil, err
		}
		if stream == nil {
			if !operation.accepts(kindOf(current)) {
				return nil, fmt.Errorf("operation %d (%s): cannot consume %s", position+1, operation.name, kindOf(current))
			}
			if kindOf(current) == kindList {
				list, err := toList(current)
				if err != nil {
					return nil, err
				}
				stream = listStream(ctx, list)
			}
		} else if !operation.accepts(kindList) {
			return nil, fmt.Errorf("operation %d (%s): cannot consume %s", position+1, operation.name, kindList)
		}
		if stream != nil {
			if stage, ok := streamStages[operation.name]; ok {
				next, err := stage(ctx, request, stream, operation)
				if err != nil {
					return nil, labelStageError(position, operation, err)
				}
				stream = labelStream(next, position, operation)
				continue
			}
			if sink, ok := streamSinks[operation.name]; ok {
				value, err := sink(ctx, request, stream, operation)
				if err != nil {
					return nil, unlabelStageError(labelStageError(position, operation, err))
				}
				current, stream = value, nil
				continue
			}
			list, err := collect(stream)
			if err != nil {
				return nil, unlabelStageError(err)
			}
			current, stream = list, nil
		}
		next, err := runOperation(ctx, request, current, operation)
		if err != nil {
			return nil, fmt.Errorf("operation %d (%s): %w", position+1, operation.name, err)
		}
		current = next
	}
	if stream != nil {
		list, err := collect(stream)
		if err != nil {
			return nil, unlabelStageError(err)
		}
		current = list
	}
	return current, nil
}

func labelStream(stream itemStream, position int, operation operation) itemStream {
	return func(yield func(any, error) bool) {
		for item, err := range stream {
			if err != nil {
				yield(nil, labelStageError(position, operation, err))
				return
			}
			if !yield(item, nil) {
				return
			}
		}
	}
}
