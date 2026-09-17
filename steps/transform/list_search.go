package transform

import (
	"context"
	"fmt"
)

func searchOperation(ctx context.Context, request *evaluation, list []any, operation operation) (any, error) {
	switch operation.name {
	case "index_of", "last_index_of":
		wanted, err := resolveNamedOperand(request, operation, "value")
		if err != nil {
			return nil, err
		}
		matcher, err := newValueMatcher(wanted)
		if err != nil {
			return nil, err
		}
		start, end, delta := 0, len(list), 1
		if operation.name == "last_index_of" {
			start, end, delta = len(list)-1, -1, -1
		}
		for index := start; index != end; index += delta {
			equal, err := matcher.matches(list[index])
			if err != nil {
				return nil, err
			}
			if equal {
				return index, nil
			}
		}
		return -1, nil
	case "has_prefix", "has_suffix":
		field := "prefix"
		suffix := false
		if operation.name == "has_suffix" {
			field, suffix = "suffix", true
		}
		value, err := resolveNamedOperand(request, operation, field)
		if err != nil {
			return nil, err
		}
		part, err := toList(value)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", field, err)
		}
		return listPrefixSuffix(list, part, suffix)
	case "find", "find_index", "find_last_index":
		start, end, delta := 0, len(list), 1
		if operation.name == "find_last_index" {
			start, end, delta = len(list)-1, -1, -1
		}
		for index := start; index != end; index += delta {
			matches, err := evaluateItemBool(ctx, request, operation, "expr", list[index], index)
			if err != nil {
				return nil, err
			}
			if matches {
				result := map[string]any{"value": list[index], "found": true}
				if operation.name != "find" {
					result["index"] = index
				}
				return result, nil
			}
		}
		result := map[string]any{"value": nil, "found": false}
		if operation.name != "find" {
			result["index"] = -1
		}
		return result, nil
	case "find_or_else":
		for index, item := range list {
			matches, err := evaluateItemBool(ctx, request, operation, "when", item, index)
			if err != nil {
				return nil, err
			}
			if matches {
				return item, nil
			}
		}
		return resolveNamedOperand(request, operation, "fallback")
	case "first", "last":
		if len(list) == 0 {
			return map[string]any{"value": nil, "found": false}, nil
		}
		index := 0
		if operation.name == "last" {
			index = len(list) - 1
		}
		return map[string]any{"value": list[index], "found": true}, nil
	case "first_or_empty", "last_or_empty":
		if len(list) == 0 {
			return nil, nil
		}
		if operation.name == "last_or_empty" {
			return list[len(list)-1], nil
		}
		return list[0], nil
	case "first_or", "last_or":
		if len(list) == 0 {
			return resolveNamedOperand(request, operation, "value")
		}
		if operation.name == "last_or" {
			return list[len(list)-1], nil
		}
		return list[0], nil
	case "nth", "nth_or", "nth_or_empty":
		field := "value"
		if operation.name == "nth_or" {
			field = "nth"
		}
		value, err := resolveNamedOperand(request, operation, field)
		if err != nil {
			return nil, err
		}
		index, err := toInt(value, "nth")
		if err != nil {
			return nil, err
		}
		if index < 0 {
			index += len(list)
		}
		if index >= 0 && index < len(list) {
			return list[index], nil
		}
		if operation.name == "nth_or" {
			return resolveNamedOperand(request, operation, "fallback")
		}
		if operation.name == "nth_or_empty" {
			return nil, nil
		}
		return nil, fmt.Errorf("nth index is out of bounds")
	}
	return nil, fmt.Errorf("search operation is not implemented")
}
