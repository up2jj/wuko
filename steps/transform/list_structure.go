package transform

import (
	"context"
	"fmt"
)

func structureOperation(ctx context.Context, request *evaluation, list []any, operation operation) (any, error) {
	switch operation.name {
	case "clone":
		return append([]any(nil), list...), nil
	case "compact":
		result := make([]any, 0, len(list))
		for _, item := range list {
			if !zeroValue(item) {
				result = append(result, item)
			}
		}
		return result, nil
	case "replace", "replace_all":
		oldValue, err := resolveNamedOperand(request, operation, "old")
		if err != nil {
			return nil, err
		}
		newValue, err := resolveNamedOperand(request, operation, "new")
		if err != nil {
			return nil, err
		}
		remaining := -1
		if operation.name == "replace" {
			countValue, err := resolveNamedOperand(request, operation, "count")
			if err != nil {
				return nil, err
			}
			remaining, err = toInt(countValue, "count")
			if err != nil {
				return nil, err
			}
		}
		matcher, err := newValueMatcher(oldValue)
		if err != nil {
			return nil, err
		}
		result := append([]any(nil), list...)
		for index, item := range result {
			if remaining == 0 {
				break
			}
			equal, err := matcher.matches(item)
			if err != nil {
				return nil, fmt.Errorf("item %d: %w", index, err)
			}
			if equal {
				result[index] = newValue
				remaining--
			}
		}
		return result, nil
	case "splice":
		indexValue, err := resolveNamedOperand(request, operation, "index")
		if err != nil {
			return nil, err
		}
		index, err := toInt(indexValue, "index")
		if err != nil {
			return nil, err
		}
		elementsValue, err := resolveNamedOperand(request, operation, "elements")
		if err != nil {
			return nil, err
		}
		elements, err := toList(elementsValue)
		if err != nil {
			return nil, fmt.Errorf("elements: %w", err)
		}
		if index < 0 {
			index = len(list) + index
		}
		index = max(0, min(index, len(list)))
		result := make([]any, 0, len(list)+len(elements))
		result = append(result, list[:index]...)
		result = append(result, elements...)
		result = append(result, list[index:]...)
		return result, nil
	case "trim", "trim_left", "trim_right":
		cutsetValue, err := resolveNamedOperand(request, operation, "cutset")
		if err != nil {
			return nil, err
		}
		cutset, err := toList(cutsetValue)
		if err != nil {
			return nil, fmt.Errorf("cutset: %w", err)
		}
		return trimCutset(list, cutset, operation.name)
	case "trim_prefix", "trim_suffix":
		field := "prefix"
		if operation.name == "trim_suffix" {
			field = "suffix"
		}
		value, err := resolveNamedOperand(request, operation, field)
		if err != nil {
			return nil, err
		}
		part, err := toList(value)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", field, err)
		}
		result := append([]any(nil), list...)
		if len(part) == 0 {
			return result, nil
		}
		for {
			matches, err := listPrefixSuffix(result, part, operation.name == "trim_suffix")
			if err != nil {
				return nil, err
			}
			if !matches {
				return result, nil
			}
			if operation.name == "trim_prefix" {
				result = result[len(part):]
			} else {
				result = result[:len(result)-len(part)]
			}
		}
	case "cut":
		separatorValue, err := resolveNamedOperand(request, operation, "separator")
		if err != nil {
			return nil, err
		}
		separator, err := toList(separatorValue)
		if err != nil {
			return nil, fmt.Errorf("separator: %w", err)
		}
		return cutList(list, separator)
	case "cut_prefix", "cut_suffix":
		field := "prefix"
		suffix := false
		if operation.name == "cut_suffix" {
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
		matches, err := listPrefixSuffix(list, part, suffix)
		if err != nil {
			return nil, err
		}
		result := append([]any(nil), list...)
		if matches {
			if suffix {
				result = result[:len(result)-len(part)]
			} else {
				result = result[len(part):]
			}
		}
		return map[string]any{"value": result, "found": matches}, nil
	}
	return nil, fmt.Errorf("structure operation is not implemented")
}

func trimCutset(list, cutset []any, name string) ([]any, error) {
	// The cutset is fixed, so its keys are computed once instead of re-marshalled
	// for every list element that gets tested against it.
	cutsetKeys, err := newValueMatcherSet(cutset)
	if err != nil {
		return nil, fmt.Errorf("cutset: %w", err)
	}
	contains := func(item any) (bool, error) {
		key, err := structuralKey(item)
		if err != nil {
			return false, err
		}
		_, exists := cutsetKeys[key]
		return exists, nil
	}
	start, end := 0, len(list)
	if name != "trim_right" {
		for start < end {
			match, err := contains(list[start])
			if err != nil {
				return nil, err
			}
			if !match {
				break
			}
			start++
		}
	}
	if name != "trim_left" {
		for end > start {
			match, err := contains(list[end-1])
			if err != nil {
				return nil, err
			}
			if !match {
				break
			}
			end--
		}
	}
	return append([]any(nil), list[start:end]...), nil
}

func listPrefixSuffix(list, part []any, suffix bool) (bool, error) {
	if len(part) > len(list) {
		return false, nil
	}
	offset := 0
	if suffix {
		offset = len(list) - len(part)
	}
	for index := range part {
		equal, err := equalValues(list[offset+index], part[index])
		if err != nil || !equal {
			return false, err
		}
	}
	return true, nil
}

func cutList(list, separator []any) (map[string]any, error) {
	if len(separator) == 0 {
		return map[string]any{"before": []any{}, "after": append([]any(nil), list...), "found": true}, nil
	}
	// Each element is marshalled once. Comparing values directly inside the scan
	// re-marshalled both sides at every offset, which is quadratic in the inputs.
	listKeys, err := structuralKeys(list)
	if err != nil {
		return nil, err
	}
	separatorKeys, err := structuralKeys(separator)
	if err != nil {
		return nil, fmt.Errorf("separator: %w", err)
	}
	for start := 0; start+len(separator) <= len(list); start++ {
		matches := true
		for index := range separatorKeys {
			if listKeys[start+index] != separatorKeys[index] {
				matches = false
				break
			}
		}
		if matches {
			return map[string]any{
				"before": append([]any(nil), list[:start]...),
				"after":  append([]any(nil), list[start+len(separator):]...),
				"found":  true,
			}, nil
		}
	}
	return map[string]any{"before": append([]any(nil), list...), "after": []any{}, "found": false}, nil
}
