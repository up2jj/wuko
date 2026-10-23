package transform

import (
	"context"
	"fmt"
)

// streamStage turns an operation's input stream into its output stream. Operands are
// resolved when the stage is built; items are evaluated only as the output is pulled.
type streamStage func(ctx context.Context, request *evaluation, upstream itemStream, operation operation) (itemStream, error)

// streamSink consumes a stream into an operation's final value, pulling only as many items
// as the result depends on.
type streamSink func(ctx context.Context, request *evaluation, upstream itemStream, operation operation) (any, error)

// Each implementation mirrors its list counterpart in list_ops.go, list_set.go,
// list_search.go and list_structure.go, including error text; TestStreamingMatchesListOperations
// runs both on the same inputs.
var streamStages map[string]streamStage

var streamSinks map[string]streamSink

func init() {
	streamStages = map[string]streamStage{
		"filter": filterStage, "reject": filterStage,
		"map": mapStage, "flat_map": flatMapStage,
		"filter_map": filterMapStage, "reject_map": filterMapStage,
		"flatten": flattenStage, "compact": compactStage, "clone": cloneStage,
		"take": takeDropStage, "drop": takeDropStage,
		"take_while": whileStage, "drop_while": whileStage,
		"take_filter": takeFilterStage, "slice": sliceStage,
		"unique": uniqueStage, "unique_by": uniqueStage, "unique_map": uniqueStage,
		"without": withoutStage, "without_by": withoutStage,
		"concat": concatStage,
	}
	streamSinks = map[string]streamSink{
		"first": prefixSink(1), "first_or": prefixSink(1), "first_or_empty": prefixSink(1),
		"find": findSink, "find_index": findSink,
		"contains_by": quantifierSink, "some_by": quantifierSink, "none_by": quantifierSink, "every_by": quantifierSink,
		"contains": matchSink, "index_of": matchSink, "count": matchSink,
		"count_by": countBySink,
	}
}

// indexed adapts a per-item function to a stream, supplying each item's position in the
// stage's input as index.
func indexed(upstream itemStream, each func(item any, index int, yield func(any, error) bool) bool) itemStream {
	return func(yield func(any, error) bool) {
		index := 0
		for item, err := range upstream {
			if err != nil {
				yield(nil, err)
				return
			}
			if !each(item, index, yield) {
				return
			}
			index++
		}
	}
}

func filterStage(ctx context.Context, request *evaluation, upstream itemStream, operation operation) (itemStream, error) {
	return indexed(upstream, func(item any, index int, yield func(any, error) bool) bool {
		matches, err := evaluateItemBool(ctx, request, operation, "expr", item, index)
		if err != nil {
			return yield(nil, err) && false
		}
		if operation.name == "reject" {
			matches = !matches
		}
		return !matches || yield(item, nil)
	}), nil
}

func mapStage(ctx context.Context, request *evaluation, upstream itemStream, operation operation) (itemStream, error) {
	return indexed(upstream, func(item any, index int, yield func(any, error) bool) bool {
		value, err := evaluateItem(ctx, request, operation, "expr", item, index)
		if err != nil {
			return yield(nil, err) && false
		}
		return yield(value, nil)
	}), nil
}

func flatMapStage(ctx context.Context, request *evaluation, upstream itemStream, operation operation) (itemStream, error) {
	return indexed(upstream, func(item any, index int, yield func(any, error) bool) bool {
		value, err := evaluateItem(ctx, request, operation, "expr", item, index)
		if err != nil {
			return yield(nil, err) && false
		}
		children, err := toList(value)
		if err != nil {
			return yield(nil, fmt.Errorf("item %d: expression must return a list: %w", index, err)) && false
		}
		for _, child := range children {
			if !yield(child, nil) {
				return false
			}
		}
		return true
	}), nil
}

func filterMapStage(ctx context.Context, request *evaluation, upstream itemStream, operation operation) (itemStream, error) {
	return indexed(upstream, func(item any, index int, yield func(any, error) bool) bool {
		matches, err := evaluateItemBool(ctx, request, operation, "when", item, index)
		if err != nil {
			return yield(nil, err) && false
		}
		if (operation.name == "filter_map") != matches {
			return true
		}
		value, err := evaluateItem(ctx, request, operation, "value", item, index)
		if err != nil {
			return yield(nil, err) && false
		}
		return yield(value, nil)
	}), nil
}

func flattenStage(_ context.Context, _ *evaluation, upstream itemStream, _ operation) (itemStream, error) {
	return indexed(upstream, func(item any, index int, yield func(any, error) bool) bool {
		children, err := toList(item)
		if err != nil {
			return yield(nil, fmt.Errorf("item %d: %w", index, err)) && false
		}
		for _, child := range children {
			if !yield(child, nil) {
				return false
			}
		}
		return true
	}), nil
}

func compactStage(_ context.Context, _ *evaluation, upstream itemStream, _ operation) (itemStream, error) {
	return indexed(upstream, func(item any, _ int, yield func(any, error) bool) bool {
		return zeroValue(item) || yield(item, nil)
	}), nil
}

func cloneStage(_ context.Context, _ *evaluation, upstream itemStream, _ operation) (itemStream, error) {
	return upstream, nil
}

func takeDropStage(_ context.Context, request *evaluation, upstream itemStream, operation operation) (itemStream, error) {
	value, err := resolveNamedOperand(request, operation, "value")
	if err != nil {
		return nil, err
	}
	count, err := toInt(value, operation.name)
	if err != nil || count < 0 {
		return nil, fmt.Errorf("%s must be a non-negative integer", operation.name)
	}
	if operation.name == "take" {
		return func(yield func(any, error) bool) {
			if count == 0 {
				return
			}
			taken := 0
			for item, err := range upstream {
				if !yield(item, err) || err != nil {
					return
				}
				if taken++; taken == count {
					return
				}
			}
		}, nil
	}
	return indexed(upstream, func(item any, index int, yield func(any, error) bool) bool {
		return index < count || yield(item, nil)
	}), nil
}

func whileStage(ctx context.Context, request *evaluation, upstream itemStream, operation operation) (itemStream, error) {
	take := operation.name == "take_while"
	return func(yield func(any, error) bool) {
		matching := true
		index := 0
		for item, err := range upstream {
			if err != nil {
				yield(nil, err)
				return
			}
			if matching {
				matches, err := evaluateItemBool(ctx, request, operation, "expr", item, index)
				if err != nil {
					yield(nil, err)
					return
				}
				matching = matches
			}
			index++
			switch {
			case take && !matching:
				return
			case take == matching:
				if !yield(item, nil) {
					return
				}
			}
		}
	}, nil
}

func takeFilterStage(ctx context.Context, request *evaluation, upstream itemStream, operation operation) (itemStream, error) {
	countValue, err := resolveNamedOperand(request, operation, "count")
	if err != nil {
		return nil, err
	}
	count, err := toInt(countValue, "count")
	if err != nil || count < 0 {
		return nil, fmt.Errorf("count must be a non-negative integer")
	}
	return func(yield func(any, error) bool) {
		taken := 0
		index := 0
		for item, err := range upstream {
			if err != nil {
				yield(nil, err)
				return
			}
			matches, err := evaluateItemBool(ctx, request, operation, "when", item, index)
			if err != nil {
				yield(nil, err)
				return
			}
			index++
			if matches {
				if !yield(item, nil) {
					return
				}
				if taken++; taken == count {
					return
				}
			}
		}
	}, nil
}

func sliceStage(_ context.Context, request *evaluation, upstream itemStream, operation operation) (itemStream, error) {
	startValue, err := resolveNamedOperand(request, operation, "start")
	if err != nil {
		return nil, err
	}
	endValue, err := resolveNamedOperand(request, operation, "end")
	if err != nil {
		return nil, err
	}
	start, err := toInt(startValue, "start")
	if err != nil {
		return nil, err
	}
	end, err := toInt(endValue, "end")
	if err != nil {
		return nil, err
	}
	// Negative bounds clamp to the start of the list rather than counting from its end.
	start, end = max(0, start), max(0, end)
	return func(yield func(any, error) bool) {
		if start >= end {
			return
		}
		index := 0
		for item, err := range upstream {
			if err != nil {
				yield(nil, err)
				return
			}
			if index >= start && !yield(item, nil) {
				return
			}
			if index++; index == end {
				return
			}
		}
	}, nil
}

func uniqueStage(ctx context.Context, request *evaluation, upstream itemStream, operation operation) (itemStream, error) {
	return func(yield func(any, error) bool) {
		seen := make(map[string]struct{})
		index := 0
		for item, err := range upstream {
			if err != nil {
				yield(nil, err)
				return
			}
			value := item
			if operation.name != "unique" {
				value, err = evaluateItem(ctx, request, operation, "expr", item, index)
				if err != nil {
					yield(nil, err)
					return
				}
			}
			key, err := structuralKey(value)
			if err != nil {
				yield(nil, fmt.Errorf("item %d: %w", index, err))
				return
			}
			index++
			if _, duplicate := seen[key]; duplicate {
				continue
			}
			seen[key] = struct{}{}
			if operation.name != "unique_map" {
				value = item
			}
			if !yield(value, nil) {
				return
			}
		}
	}, nil
}

func withoutStage(ctx context.Context, request *evaluation, upstream itemStream, operation operation) (itemStream, error) {
	values, err := resolveNamedOperand(request, operation, "values")
	if err != nil {
		return nil, err
	}
	excludedValues, err := toList(values)
	if err != nil {
		return nil, fmt.Errorf("values: %w", err)
	}
	excluded := make(map[string]struct{}, len(excludedValues))
	for _, value := range excludedValues {
		key, err := structuralKey(value)
		if err != nil {
			return nil, err
		}
		excluded[key] = struct{}{}
	}
	return indexed(upstream, func(item any, index int, yield func(any, error) bool) bool {
		keyValue := item
		if operation.name == "without_by" {
			var err error
			keyValue, err = evaluateItem(ctx, request, operation, "by", item, index)
			if err != nil {
				return yield(nil, err) && false
			}
		}
		key, err := structuralKey(keyValue)
		if err != nil {
			return yield(nil, err) && false
		}
		_, skip := excluded[key]
		return skip || yield(item, nil)
	}), nil
}

func concatStage(_ context.Context, request *evaluation, upstream itemStream, operation operation) (itemStream, error) {
	others, err := resolveListOperands(request, operation)
	if err != nil {
		return nil, err
	}
	return func(yield func(any, error) bool) {
		for item, err := range upstream {
			if !yield(item, err) || err != nil {
				return
			}
		}
		for _, items := range others {
			for _, item := range items {
				if !yield(item, nil) {
					return
				}
			}
		}
	}, nil
}

// prefixSink runs an operation that depends only on the first limit items through its list
// implementation, after pulling just those items.
func prefixSink(limit int) streamSink {
	return func(ctx context.Context, request *evaluation, upstream itemStream, operation operation) (any, error) {
		list, err := collectAtMost(upstream, limit)
		if err != nil {
			return nil, err
		}
		return runListOperation(ctx, request, list, operation)
	}
}

func findSink(ctx context.Context, request *evaluation, upstream itemStream, operation operation) (any, error) {
	index := 0
	for item, err := range upstream {
		if err != nil {
			return nil, err
		}
		matches, err := evaluateItemBool(ctx, request, operation, "expr", item, index)
		if err != nil {
			return nil, err
		}
		if matches {
			result := map[string]any{"value": item, "found": true}
			if operation.name == "find_index" {
				result["index"] = index
			}
			return result, nil
		}
		index++
	}
	result := map[string]any{"value": nil, "found": false}
	if operation.name == "find_index" {
		result["index"] = -1
	}
	return result, nil
}

func quantifierSink(ctx context.Context, request *evaluation, upstream itemStream, operation operation) (any, error) {
	matched := false
	index := 0
	for item, err := range upstream {
		if err != nil {
			return nil, err
		}
		match, err := evaluateItemBool(ctx, request, operation, "expr", item, index)
		if err != nil {
			return nil, err
		}
		index++
		switch {
		case match && (operation.name == "contains_by" || operation.name == "some_by"):
			return true, nil
		case match && operation.name == "none_by":
			return false, nil
		case !match && operation.name == "every_by":
			return false, nil
		}
		matched = matched || match
	}
	if operation.name == "every_by" || operation.name == "none_by" {
		return true, nil
	}
	return matched, nil
}

// matchSink handles the operations that compare items with one fixed value: contains and
// index_of stop at the first equal item, count tallies them.
func matchSink(_ context.Context, request *evaluation, upstream itemStream, operation operation) (any, error) {
	wanted, err := resolveNamedOperand(request, operation, "value")
	if err != nil {
		return nil, err
	}
	matcher, err := newValueMatcher(wanted)
	if err != nil {
		return nil, err
	}
	count, index := 0, 0
	for item, err := range upstream {
		if err != nil {
			return nil, err
		}
		equal, err := matcher.matches(item)
		if err != nil {
			return nil, err
		}
		if equal {
			switch operation.name {
			case "contains":
				return true, nil
			case "index_of":
				return index, nil
			}
			count++
		}
		index++
	}
	switch operation.name {
	case "contains":
		return false, nil
	case "index_of":
		return -1, nil
	}
	return count, nil
}

func countBySink(ctx context.Context, request *evaluation, upstream itemStream, operation operation) (any, error) {
	count, index := 0, 0
	for item, err := range upstream {
		if err != nil {
			return nil, err
		}
		matches, err := evaluateItemBool(ctx, request, operation, "expr", item, index)
		if err != nil {
			return nil, err
		}
		if matches {
			count++
		}
		index++
	}
	return count, nil
}
