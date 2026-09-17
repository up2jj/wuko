package transform

import (
	"context"
	"fmt"
	"math"
	"sort"

	"github.com/samber/lo"
	"github.com/up2jj/wuko/workflow"
)

func runListOperation(ctx context.Context, request *evaluation, list []any, operation operation) (any, error) {
	switch operation.name {
	case "filter", "reject":
		return lo.FilterErr(list, func(item any, index int) (bool, error) {
			matches, err := evaluateItemBool(ctx, request, operation, "expr", item, index)
			if operation.name == "reject" {
				matches = !matches
			}
			return matches, err
		})
	case "map":
		return lo.MapErr(list, func(item any, index int) (any, error) {
			return evaluateItem(ctx, request, operation, "expr", item, index)
		})
	case "flat_map":
		return lo.FlatMapErr(list, func(item any, index int) ([]any, error) {
			value, err := evaluateItem(ctx, request, operation, "expr", item, index)
			if err != nil {
				return nil, err
			}
			flattened, err := toList(value)
			if err != nil {
				return nil, fmt.Errorf("item %d: expression must return a list: %w", index, err)
			}
			return flattened, nil
		})
	case "filter_map", "reject_map":
		return filterMap(ctx, request, list, operation)
	case "filter_reject":
		return filterReject(ctx, request, list, operation)
	case "unique", "unique_by", "unique_map", "find_uniques", "find_uniques_by", "find_duplicates", "find_duplicates_by":
		return uniquenessOperation(ctx, request, list, operation)
	case "group_by", "group_by_map", "partition_by", "key_by", "associate", "filter_to_map", "keyify":
		return groupingOperation(ctx, request, list, operation)
	case "chunk", "window", "sliding", "flatten", "concat", "interleave", "fill":
		return segmentationOperation(ctx, request, list, operation)
	case "drop", "drop_right", "drop_while", "drop_right_while", "drop_by_index", "take", "take_while", "take_filter", "subset", "slice":
		return selectionOperation(ctx, request, list, operation)
	case "replace", "replace_all", "clone", "compact", "splice", "trim", "trim_left", "trim_prefix", "trim_right", "trim_suffix", "cut", "cut_prefix", "cut_suffix":
		return structureOperation(ctx, request, list, operation)
	case "union", "intersect", "intersect_by", "difference", "without", "without_by", "without_nth", "contains", "contains_by", "every", "every_by", "some", "some_by", "none", "none_by", "elements_match", "elements_match_by":
		return setOperation(ctx, request, list, operation)
	case "index_of", "last_index_of", "has_prefix", "has_suffix", "find", "find_index", "find_last_index", "find_or_else", "first", "first_or", "first_or_empty", "last", "last_or", "last_or_empty", "nth", "nth_or", "nth_or_empty":
		return searchOperation(ctx, request, list, operation)
	case "min", "min_by", "min_index", "min_index_by", "max", "max_by", "max_index", "max_index_by", "is_sorted", "is_sorted_by", "sort", "sort_by":
		return orderingOperation(ctx, request, list, operation)
	case "reduce", "reduce_right", "count", "count_by", "count_values", "count_values_by", "sum", "sum_by", "product", "product_by", "mean", "mean_by", "mode":
		return aggregationOperation(ctx, request, list, operation)
	case "zip", "zip_with", "unzip", "unzip_with", "cross_join", "cross_join_with":
		return tupleOperation(ctx, request, list, operation)
	case "from_entries":
		return fromEntries(list)
	default:
		return nil, fmt.Errorf("operation is not implemented")
	}
}

func evaluateItem(ctx context.Context, request *evaluation, operation operation, field string, item any, index int) (any, error) {
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	value, err := operation.expressions[field].evaluate(request, map[string]any{"item": item, "index": index})
	if err != nil {
		return nil, fmt.Errorf("item %d: %w", index, err)
	}
	return value, nil
}

func evaluateItemBool(ctx context.Context, request *evaluation, operation operation, field string, item any, index int) (bool, error) {
	value, err := evaluateItem(ctx, request, operation, field, item, index)
	if err != nil {
		return false, err
	}
	return boolValue(value, field)
}

func resolveNamedOperand(request *evaluation, operation operation, name string) (any, error) {
	value, exists := operation.operands[name]
	if !exists {
		return nil, fmt.Errorf("%s is missing", name)
	}
	resolved, err := value.resolve(request)
	if err != nil {
		return nil, fmt.Errorf("resolving %s: %w", name, err)
	}
	return resolved, nil
}

func resolveOperandList(request *evaluation, operation operation) ([]any, error) {
	result := make([]any, len(operation.operandList))
	for index := range operation.operandList {
		value, err := operation.operandList[index].resolve(request)
		if err != nil {
			return nil, fmt.Errorf("resolving with[%d]: %w", index, err)
		}
		result[index] = value
	}
	return result, nil
}

func filterMap(ctx context.Context, request *evaluation, list []any, operation operation) ([]any, error) {
	result := make([]any, 0, len(list))
	for index, item := range list {
		// when guards value: evaluating value first made a guard such as
		// {value: "item.tag.name", when: "item.tag != nil"} fail on the very
		// items it was written to exclude.
		matches, err := evaluateItemBool(ctx, request, operation, "when", item, index)
		if err != nil {
			return nil, err
		}
		if (operation.name == "filter_map") != matches {
			continue
		}
		value, err := evaluateItem(ctx, request, operation, "value", item, index)
		if err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, nil
}

func filterReject(ctx context.Context, request *evaluation, list []any, operation operation) (map[string]any, error) {
	kept := make([]any, 0, len(list))
	rejected := make([]any, 0, len(list))
	for index, item := range list {
		matches, err := evaluateItemBool(ctx, request, operation, "expr", item, index)
		if err != nil {
			return nil, err
		}
		if matches {
			kept = append(kept, item)
		} else {
			rejected = append(rejected, item)
		}
	}
	return map[string]any{"kept": kept, "rejected": rejected}, nil
}

func uniquenessOperation(ctx context.Context, request *evaluation, list []any, operation operation) ([]any, error) {
	keys := make([]string, len(list))
	values := list
	for index, item := range list {
		keyValue := item
		if operation.name == "unique_by" || operation.name == "find_uniques_by" || operation.name == "find_duplicates_by" {
			var err error
			keyValue, err = evaluateItem(ctx, request, operation, "expr", item, index)
			if err != nil {
				return nil, err
			}
		} else if operation.name == "unique_map" {
			var err error
			keyValue, err = evaluateItem(ctx, request, operation, "expr", item, index)
			if err != nil {
				return nil, err
			}
			if index == 0 {
				values = make([]any, len(list))
			}
			values[index] = keyValue
		}
		key, err := structuralKey(keyValue)
		if err != nil {
			return nil, fmt.Errorf("item %d: %w", index, err)
		}
		keys[index] = key
	}
	counts := make(map[string]int, len(keys))
	for _, key := range keys {
		counts[key]++
	}
	seen := make(map[string]struct{}, len(keys))
	result := make([]any, 0, len(values))
	for index, key := range keys {
		_, duplicate := seen[key]
		seen[key] = struct{}{}
		switch operation.name {
		case "find_uniques", "find_uniques_by":
			if counts[key] == 1 {
				result = append(result, values[index])
			}
		case "find_duplicates", "find_duplicates_by":
			if counts[key] > 1 && !duplicate {
				result = append(result, values[index])
			}
		default:
			if !duplicate {
				result = append(result, values[index])
			}
		}
	}
	return result, nil
}

func groupingOperation(ctx context.Context, request *evaluation, list []any, operation operation) (any, error) {
	if operation.name == "partition_by" {
		groups := make(map[string]int)
		result := make([]any, 0)
		for index, item := range list {
			keyValue, err := evaluateItem(ctx, request, operation, "expr", item, index)
			if err != nil {
				return nil, err
			}
			key, err := structuralKey(keyValue)
			if err != nil {
				return nil, fmt.Errorf("item %d: %w", index, err)
			}
			group, exists := groups[key]
			if !exists {
				groups[key] = len(result)
				result = append(result, []any{item})
				continue
			}
			result[group] = append(result[group].([]any), item)
		}
		return result, nil
	}
	result := make(map[string]any, len(list))
	for index, item := range list {
		if err := checkContext(ctx); err != nil {
			return nil, err
		}
		switch operation.name {
		case "keyify":
			key, err := stringKey(item, fmt.Sprintf("item %d", index))
			if err != nil {
				return nil, err
			}
			result[key] = map[string]any{}
		case "group_by", "key_by":
			keyValue, err := evaluateItem(ctx, request, operation, "expr", item, index)
			if err != nil {
				return nil, err
			}
			key, err := stringKey(keyValue, fmt.Sprintf("item %d expression", index))
			if err != nil {
				return nil, err
			}
			if operation.name == "group_by" {
				group, _ := result[key].([]any)
				result[key] = append(group, item)
			} else {
				result[key] = item
			}
		case "group_by_map", "associate", "filter_to_map":
			// when guards key and value, so it has to run before them.
			if operation.name == "filter_to_map" {
				matches, err := evaluateItemBool(ctx, request, operation, "when", item, index)
				if err != nil {
					return nil, err
				}
				if !matches {
					continue
				}
			}
			keyValue, err := evaluateItem(ctx, request, operation, "key", item, index)
			if err != nil {
				return nil, err
			}
			key, err := stringKey(keyValue, fmt.Sprintf("item %d key", index))
			if err != nil {
				return nil, err
			}
			value, err := evaluateItem(ctx, request, operation, "value", item, index)
			if err != nil {
				return nil, err
			}
			if operation.name == "group_by_map" {
				group, _ := result[key].([]any)
				result[key] = append(group, value)
			} else {
				result[key] = value
			}
		}
	}
	return result, nil
}

func segmentationOperation(ctx context.Context, request *evaluation, list []any, operation operation) (any, error) {
	switch operation.name {
	case "flatten":
		result := make([]any, 0)
		for index, item := range list {
			children, err := toList(item)
			if err != nil {
				return nil, fmt.Errorf("item %d: %w", index, err)
			}
			result = append(result, children...)
		}
		return result, nil
	case "concat":
		return appendLists(request, list, operation, false)
	case "interleave":
		values, err := resolveListOperands(request, operation)
		if err != nil {
			return nil, err
		}
		lists := append([][]any{list}, values...)
		maximum, total := 0, 0
		for _, items := range lists {
			maximum = max(maximum, len(items))
			total += len(items)
		}
		result := make([]any, 0, total)
		for index := range maximum {
			for _, items := range lists {
				if index < len(items) {
					result = append(result, items[index])
				}
			}
		}
		return result, nil
	case "fill":
		value, err := resolveNamedOperand(request, operation, "value")
		if err != nil {
			return nil, err
		}
		result := make([]any, len(list))
		for index := range result {
			result[index] = workflow.Clone(value)
		}
		return result, nil
	}
	size := 0
	if operation.name != "sliding" {
		value, err := resolveNamedOperand(request, operation, "value")
		if err != nil {
			return nil, err
		}
		size, err = toInt(value, operation.name)
		if err != nil || size <= 0 {
			return nil, fmt.Errorf("size must be a positive integer")
		}
	}
	if operation.name == "chunk" {
		capacity := 0
		if len(list) > 0 {
			capacity = (len(list)-1)/size + 1
		}
		result := make([]any, 0, capacity)
		for start := 0; start < len(list); start += size {
			end := min(start+size, len(list))
			result = append(result, append([]any(nil), list[start:end]...))
		}
		return result, nil
	}
	stepSize := 1
	if operation.name == "sliding" {
		sizeValue, err := resolveNamedOperand(request, operation, "size")
		if err != nil {
			return nil, err
		}
		size, err = toInt(sizeValue, "size")
		if err != nil || size <= 0 {
			return nil, fmt.Errorf("size must be a positive integer")
		}
		stepValue, err := resolveNamedOperand(request, operation, "step")
		if err != nil {
			return nil, err
		}
		stepSize, err = toInt(stepValue, "step")
		if err != nil || stepSize <= 0 {
			return nil, fmt.Errorf("step must be a positive integer")
		}
	}
	result := make([]any, 0)
	for start := 0; start+size <= len(list); start += stepSize {
		result = append(result, append([]any(nil), list[start:start+size]...))
	}
	return result, nil
}

func appendLists(request *evaluation, first []any, operation operation, unique bool) ([]any, error) {
	others, err := resolveListOperands(request, operation)
	if err != nil {
		return nil, err
	}
	result := append([]any(nil), first...)
	for _, items := range others {
		result = append(result, items...)
	}
	if !unique {
		return result, nil
	}
	return uniqueValues(result)
}

func resolveListOperands(request *evaluation, operation operation) ([][]any, error) {
	values, err := resolveOperandList(request, operation)
	if err != nil {
		return nil, err
	}
	result := make([][]any, len(values))
	for index, value := range values {
		list, err := toList(value)
		if err != nil {
			return nil, fmt.Errorf("with[%d]: %w", index, err)
		}
		result[index] = list
	}
	return result, nil
}

func uniqueValues(values []any) ([]any, error) {
	result := make([]any, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for index, value := range values {
		key, err := structuralKey(value)
		if err != nil {
			return nil, fmt.Errorf("item %d: %w", index, err)
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, value)
	}
	return result, nil
}

func selectionOperation(ctx context.Context, request *evaluation, list []any, operation operation) ([]any, error) {
	switch operation.name {
	case "drop_while", "drop_right_while", "take_while":
		start, end := 0, len(list)
		if operation.name == "drop_right_while" {
			for end > 0 {
				matches, err := evaluateItemBool(ctx, request, operation, "expr", list[end-1], end-1)
				if err != nil {
					return nil, err
				}
				if !matches {
					break
				}
				end--
			}
		} else {
			for start < len(list) {
				matches, err := evaluateItemBool(ctx, request, operation, "expr", list[start], start)
				if err != nil {
					return nil, err
				}
				if !matches {
					break
				}
				start++
			}
			if operation.name == "take_while" {
				end, start = start, 0
			}
		}
		return append([]any(nil), list[start:end]...), nil
	case "take_filter":
		countValue, err := resolveNamedOperand(request, operation, "count")
		if err != nil {
			return nil, err
		}
		count, err := toInt(countValue, "count")
		if err != nil || count < 0 {
			return nil, fmt.Errorf("count must be a non-negative integer")
		}
		result := make([]any, 0, min(count, len(list)))
		for index, item := range list {
			matches, err := evaluateItemBool(ctx, request, operation, "when", item, index)
			if err != nil {
				return nil, err
			}
			if matches {
				result = append(result, item)
				if len(result) == count {
					break
				}
			}
		}
		return result, nil
	case "subset":
		offsetValue, err := resolveNamedOperand(request, operation, "offset")
		if err != nil {
			return nil, err
		}
		lengthValue, err := resolveNamedOperand(request, operation, "length")
		if err != nil {
			return nil, err
		}
		offset, err := toInt(offsetValue, "offset")
		if err != nil {
			return nil, err
		}
		length, err := toInt(lengthValue, "length")
		if err != nil || length < 0 {
			return nil, fmt.Errorf("length must be a non-negative integer")
		}
		if offset < 0 {
			offset = max(0, len(list)+offset)
		}
		if offset >= len(list) {
			return []any{}, nil
		}
		end := len(list)
		if length <= len(list)-offset {
			end = offset + length
		}
		return append([]any(nil), list[offset:end]...), nil
	case "slice":
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
		start, end = max(0, min(start, len(list))), max(0, min(end, len(list)))
		if start >= end {
			return []any{}, nil
		}
		return append([]any(nil), list[start:end]...), nil
	case "drop_by_index":
		value, err := resolveNamedOperand(request, operation, "value")
		if err != nil {
			return nil, err
		}
		indexes, err := integerList(value, "indexes")
		if err != nil {
			return nil, err
		}
		removed := make(map[int]struct{}, len(indexes))
		for _, index := range indexes {
			if index < 0 {
				index += len(list)
			}
			if index >= 0 && index < len(list) {
				removed[index] = struct{}{}
			}
		}
		result := make([]any, 0, len(list)-len(removed))
		for index, item := range list {
			if _, exists := removed[index]; !exists {
				result = append(result, item)
			}
		}
		return result, nil
	}
	value, err := resolveNamedOperand(request, operation, "value")
	if err != nil {
		return nil, err
	}
	count, err := toInt(value, operation.name)
	if err != nil || count < 0 {
		return nil, fmt.Errorf("%s must be a non-negative integer", operation.name)
	}
	if operation.name == "take" {
		return append([]any(nil), list[:min(count, len(list))]...), nil
	}
	if operation.name == "drop" {
		return append([]any(nil), list[min(count, len(list)):]...), nil
	}
	return append([]any(nil), list[:max(0, len(list)-count)]...), nil
}

func integerList(value any, label string) ([]int, error) {
	values, err := toList(value)
	if err != nil {
		return nil, fmt.Errorf("%s must be a list of integers", label)
	}
	result := make([]int, len(values))
	for index, value := range values {
		result[index], err = toInt(value, fmt.Sprintf("%s[%d]", label, index))
		if err != nil {
			return nil, err
		}
	}
	return result, nil
}

func orderingOperation(ctx context.Context, request *evaluation, list []any, operation operation) (any, error) {
	switch operation.name {
	case "sort", "sort_by":
		result := append([]any(nil), list...)
		keys := result
		if operation.name == "sort_by" {
			keys = make([]any, len(result))
			for index, item := range result {
				value, err := evaluateItem(ctx, request, operation, "expr", item, index)
				if err != nil {
					return nil, err
				}
				keys[index] = value
			}
		}
		if err := validateComparableValues(keys); err != nil {
			return nil, err
		}
		type keyed struct{ value, key any }
		items := make([]keyed, len(result))
		for index := range result {
			items[index] = keyed{result[index], keys[index]}
		}
		descending := operation.argument == "desc"
		sort.SliceStable(items, func(left, right int) bool {
			compared, _ := compareValues(items[left].key, items[right].key)
			if descending {
				return compared > 0
			}
			return compared < 0
		})
		for index := range items {
			result[index] = items[index].value
		}
		return result, nil
	case "is_sorted", "is_sorted_by":
		keys := list
		if operation.name == "is_sorted_by" {
			keys = make([]any, len(list))
			for index, item := range list {
				value, err := evaluateItem(ctx, request, operation, "expr", item, index)
				if err != nil {
					return nil, err
				}
				keys[index] = value
			}
		}
		if err := validateComparableValues(keys); err != nil {
			return nil, err
		}
		for index := 1; index < len(keys); index++ {
			compared, _ := compareValues(keys[index-1], keys[index])
			if compared > 0 {
				return false, nil
			}
		}
		return true, nil
	}
	return minMaxOperation(ctx, request, list, operation)
}

func validateComparableValues(values []any) error {
	if len(values) < 2 {
		if len(values) == 1 {
			_, err := compareValues(values[0], values[0])
			return err
		}
		return nil
	}
	for index := 1; index < len(values); index++ {
		if _, err := compareValues(values[0], values[index]); err != nil {
			return fmt.Errorf("item %d: %w", index, err)
		}
	}
	return nil
}

func minMaxOperation(ctx context.Context, request *evaluation, list []any, operation operation) (any, error) {
	withIndex := operation.name == "min_index" || operation.name == "min_index_by" || operation.name == "max_index" || operation.name == "max_index_by"
	if len(list) == 0 {
		if withIndex {
			return map[string]any{"value": nil, "index": -1}, nil
		}
		return nil, nil
	}
	selected := 0
	by := operation.name == "min_by" || operation.name == "min_index_by" || operation.name == "max_by" || operation.name == "max_index_by"
	maximum := operation.name == "max" || operation.name == "max_by" || operation.name == "max_index" || operation.name == "max_index_by"
	for index := 1; index < len(list); index++ {
		choose := false
		if by {
			value, err := operation.expressions["expr"].evaluate(request, map[string]any{"left": list[index], "right": list[selected]})
			if err != nil {
				return nil, fmt.Errorf("item %d: %w", index, err)
			}
			choose, err = boolValue(value, "expr")
			if err != nil {
				return nil, err
			}
		} else {
			compared, err := compareValues(list[index], list[selected])
			if err != nil {
				return nil, fmt.Errorf("item %d: %w", index, err)
			}
			choose = (!maximum && compared < 0) || (maximum && compared > 0)
		}
		if choose {
			selected = index
		}
		if err := checkContext(ctx); err != nil {
			return nil, err
		}
	}
	if withIndex {
		return map[string]any{"value": list[selected], "index": selected}, nil
	}
	return list[selected], nil
}

func aggregationOperation(ctx context.Context, request *evaluation, list []any, operation operation) (any, error) {
	switch operation.name {
	case "reduce", "reduce_right":
		accumulator, err := resolveNamedOperand(request, operation, "initial")
		if err != nil {
			return nil, err
		}
		start, end, delta := 0, len(list), 1
		if operation.name == "reduce_right" {
			start, end, delta = len(list)-1, -1, -1
		}
		for index := start; index != end; index += delta {
			if err := checkContext(ctx); err != nil {
				return nil, err
			}
			accumulator, err = operation.expressions["expr"].evaluate(request, map[string]any{"acc": accumulator, "item": list[index], "index": index})
			if err != nil {
				return nil, fmt.Errorf("item %d: %w", index, err)
			}
		}
		return accumulator, nil
	case "count":
		wanted, err := resolveNamedOperand(request, operation, "value")
		if err != nil {
			return nil, err
		}
		matcher, err := newValueMatcher(wanted)
		if err != nil {
			return nil, err
		}
		count := 0
		for _, item := range list {
			equal, err := matcher.matches(item)
			if err != nil {
				return nil, err
			}
			if equal {
				count++
			}
		}
		return count, nil
	case "count_by":
		count := 0
		for index, item := range list {
			matches, err := evaluateItemBool(ctx, request, operation, "expr", item, index)
			if err != nil {
				return nil, err
			}
			if matches {
				count++
			}
		}
		return count, nil
	case "count_values", "count_values_by":
		counts := make(map[string]any)
		for index, item := range list {
			keyValue := item
			if operation.name == "count_values_by" {
				var err error
				keyValue, err = evaluateItem(ctx, request, operation, "expr", item, index)
				if err != nil {
					return nil, err
				}
			}
			key, err := stringKey(keyValue, fmt.Sprintf("item %d", index))
			if err != nil {
				return nil, err
			}
			count, _ := counts[key].(int)
			counts[key] = count + 1
		}
		return counts, nil
	case "mode":
		return numericMode(list)
	default:
		return numericAggregate(ctx, request, list, operation)
	}
}

func numericAggregate(ctx context.Context, request *evaluation, list []any, operation operation) (any, error) {
	values := make([]any, len(list))
	for index, item := range list {
		value := item
		if operation.name == "sum_by" || operation.name == "product_by" || operation.name == "mean_by" {
			var err error
			value, err = evaluateItem(ctx, request, operation, "expr", item, index)
			if err != nil {
				return nil, err
			}
		}
		values[index] = value
	}
	integerMode := true
	for index, value := range values {
		_, numeric, integer := numberValue(value)
		if !numeric {
			return nil, fmt.Errorf("item %d must be numeric, got %T", index, value)
		}
		if index > 0 && integer != integerMode {
			return nil, fmt.Errorf("item %d mixes integer and floating-point values", index)
		}
		integerMode = integer
	}
	product := operation.name == "product" || operation.name == "product_by"
	mean := operation.name == "mean" || operation.name == "mean_by"
	if integerMode {
		result := int64(0)
		if product {
			result = 1
		}
		for _, value := range values {
			integer, ok := integerValue(value)
			if !ok {
				return nil, fmt.Errorf("integer value %v is out of range", value)
			}
			if product {
				result *= integer
			} else {
				result += integer
			}
		}
		if mean && len(values) > 0 {
			result /= int64(len(values))
		}
		return int(result), nil
	}
	result := float64(0)
	if product {
		result = 1
	}
	for _, value := range values {
		number, _, _ := numberValue(value)
		if product {
			result *= number
		} else {
			result += number
		}
	}
	if mean && len(values) > 0 {
		result /= float64(len(values))
	}
	if math.IsNaN(result) || math.IsInf(result, 0) {
		return nil, fmt.Errorf("numeric result is not finite")
	}
	return result, nil
}

func numericMode(list []any) ([]any, error) {
	counts := make(map[string]int, len(list))
	maximum := 0
	integerMode := true
	result := make([]any, 0)
	for index, item := range list {
		_, numeric, integer := numberValue(item)
		if !numeric {
			return nil, fmt.Errorf("item %d must be numeric, got %T", index, item)
		}
		if index > 0 && integer != integerMode {
			return nil, fmt.Errorf("item %d mixes integer and floating-point values", index)
		}
		integerMode = integer
		key, _ := structuralKey(item)
		counts[key]++
		if counts[key] > maximum {
			maximum = counts[key]
			result = []any{item}
		} else if counts[key] == maximum {
			result = append(result, item)
		}
	}
	return result, nil
}
