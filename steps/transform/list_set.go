package transform

import (
	"context"
	"fmt"
)

func setOperation(ctx context.Context, request *evaluation, list []any, operation operation) (any, error) {
	switch operation.name {
	case "union":
		return appendLists(request, list, operation, true)
	case "intersect", "intersect_by":
		return intersectLists(ctx, request, list, operation)
	case "difference":
		otherValue, err := resolveNamedOperand(request, operation, "with")
		if err != nil {
			return nil, err
		}
		other, err := toList(otherValue)
		if err != nil {
			return nil, fmt.Errorf("with: %w", err)
		}
		left, err := listDifference(list, other)
		if err != nil {
			return nil, err
		}
		right, err := listDifference(other, list)
		if err != nil {
			return nil, err
		}
		return map[string]any{"left": left, "right": right}, nil
	case "without", "without_by":
		values, err := resolveNamedOperand(request, operation, "values")
		if err != nil {
			return nil, err
		}
		excluded, err := toList(values)
		if err != nil {
			return nil, fmt.Errorf("values: %w", err)
		}
		return withoutValues(ctx, request, list, excluded, operation)
	case "without_nth":
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
	case "contains":
		wanted, err := resolveNamedOperand(request, operation, "value")
		if err != nil {
			return nil, err
		}
		return listContains(list, wanted)
	case "contains_by", "every_by", "some_by", "none_by":
		return predicateQuantifier(ctx, request, list, operation)
	case "every", "some", "none":
		value, err := resolveNamedOperand(request, operation, "value")
		if err != nil {
			return nil, err
		}
		subset, err := toList(value)
		if err != nil {
			return nil, fmt.Errorf("%s operand: %w", operation.name, err)
		}
		// The searched list is fixed, so it is marshalled once instead of once per
		// candidate, which made this quadratic in the two inputs.
		listKeys, err := newValueMatcherSet(list)
		if err != nil {
			return nil, err
		}
		matches := 0
		for index, candidate := range subset {
			key, err := structuralKey(candidate)
			if err != nil {
				return nil, fmt.Errorf("%s operand item %d: %w", operation.name, index, err)
			}
			if _, contains := listKeys[key]; contains {
				matches++
			}
		}
		switch operation.name {
		case "every":
			return matches == len(subset), nil
		case "some":
			return matches > 0, nil
		default:
			return matches == 0, nil
		}
	case "elements_match", "elements_match_by":
		otherValue, err := resolveNamedOperand(request, operation, "with")
		if err != nil {
			return nil, err
		}
		other, err := toList(otherValue)
		if err != nil {
			return nil, fmt.Errorf("with: %w", err)
		}
		return elementsMatch(ctx, request, list, other, operation)
	}
	return nil, fmt.Errorf("set operation is not implemented")
}

func intersectLists(ctx context.Context, request *evaluation, first []any, operation operation) ([]any, error) {
	others, err := resolveListOperands(request, operation)
	if err != nil {
		return nil, err
	}
	keyLists := make([]map[string]struct{}, len(others))
	for listIndex, items := range others {
		keys := make(map[string]struct{}, len(items))
		for index, item := range items {
			keyValue := item
			if operation.name == "intersect_by" {
				keyValue, err = evaluateItem(ctx, request, operation, "by", item, index)
				if err != nil {
					return nil, fmt.Errorf("with list %d: %w", listIndex+1, err)
				}
			}
			key, err := structuralKey(keyValue)
			if err != nil {
				return nil, err
			}
			keys[key] = struct{}{}
		}
		keyLists[listIndex] = keys
	}
	result := make([]any, 0)
	seen := make(map[string]struct{})
	for index, item := range first {
		keyValue := item
		if operation.name == "intersect_by" {
			keyValue, err = evaluateItem(ctx, request, operation, "by", item, index)
			if err != nil {
				return nil, err
			}
		}
		key, err := structuralKey(keyValue)
		if err != nil {
			return nil, err
		}
		if _, exists := seen[key]; exists {
			continue
		}
		present := true
		for _, keys := range keyLists {
			if _, exists := keys[key]; !exists {
				present = false
				break
			}
		}
		if present {
			seen[key] = struct{}{}
			result = append(result, item)
		}
	}
	return result, nil
}

func listDifference(left, right []any) ([]any, error) {
	rightKeys := make(map[string]struct{}, len(right))
	for _, item := range right {
		key, err := structuralKey(item)
		if err != nil {
			return nil, err
		}
		rightKeys[key] = struct{}{}
	}
	result := make([]any, 0)
	for _, item := range left {
		key, err := structuralKey(item)
		if err != nil {
			return nil, err
		}
		if _, exists := rightKeys[key]; !exists {
			result = append(result, item)
		}
	}
	return result, nil
}

func withoutValues(ctx context.Context, request *evaluation, list, excluded []any, operation operation) ([]any, error) {
	excludedKeys := make(map[string]struct{}, len(excluded))
	for _, value := range excluded {
		key, err := structuralKey(value)
		if err != nil {
			return nil, err
		}
		excludedKeys[key] = struct{}{}
	}
	result := make([]any, 0, len(list))
	for index, item := range list {
		keyValue := item
		if operation.name == "without_by" {
			var err error
			keyValue, err = evaluateItem(ctx, request, operation, "by", item, index)
			if err != nil {
				return nil, err
			}
		}
		key, err := structuralKey(keyValue)
		if err != nil {
			return nil, err
		}
		if _, excluded := excludedKeys[key]; !excluded {
			result = append(result, item)
		}
	}
	return result, nil
}

func listContains(list []any, wanted any) (bool, error) {
	matcher, err := newValueMatcher(wanted)
	if err != nil {
		return false, err
	}
	for _, item := range list {
		equal, err := matcher.matches(item)
		if err != nil || equal {
			return equal, err
		}
	}
	return false, nil
}

func predicateQuantifier(ctx context.Context, request *evaluation, list []any, operation operation) (bool, error) {
	matches := 0
	for index, item := range list {
		match, err := evaluateItemBool(ctx, request, operation, "expr", item, index)
		if err != nil {
			return false, err
		}
		if match {
			matches++
			if operation.name == "contains_by" || operation.name == "some_by" {
				return true, nil
			}
			if operation.name == "none_by" {
				return false, nil
			}
		} else if operation.name == "every_by" {
			return false, nil
		}
	}
	switch operation.name {
	case "every_by", "none_by":
		return true, nil
	default:
		return matches > 0, nil
	}
}

func elementsMatch(ctx context.Context, request *evaluation, left, right []any, operation operation) (bool, error) {
	if len(left) != len(right) {
		return false, nil
	}
	counts := make(map[string]int, len(left))
	for index, item := range left {
		keyValue := item
		if operation.name == "elements_match_by" {
			var err error
			keyValue, err = evaluateItem(ctx, request, operation, "by", item, index)
			if err != nil {
				return false, err
			}
		}
		key, err := structuralKey(keyValue)
		if err != nil {
			return false, err
		}
		counts[key]++
	}
	for index, item := range right {
		keyValue := item
		if operation.name == "elements_match_by" {
			var err error
			keyValue, err = evaluateItem(ctx, request, operation, "by", item, index)
			if err != nil {
				return false, err
			}
		}
		key, err := structuralKey(keyValue)
		if err != nil {
			return false, err
		}
		counts[key]--
	}
	for _, count := range counts {
		if count != 0 {
			return false, nil
		}
	}
	return true, nil
}
