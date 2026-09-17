package transform

import (
	"context"
	"fmt"
)

func runObjectOperation(ctx context.Context, request *evaluation, object map[string]any, operation operation) (any, error) {
	keys := sortedKeys(object)
	switch operation.name {
	case "keys", "unique_keys":
		objects, err := objectOperands(request, object, operation)
		if err != nil {
			return nil, err
		}
		result := make([]any, 0)
		seen := make(map[string]struct{})
		for _, current := range objects {
			for _, key := range sortedKeys(current) {
				if operation.name == "unique_keys" {
					if _, exists := seen[key]; exists {
						continue
					}
					seen[key] = struct{}{}
				}
				result = append(result, key)
			}
		}
		return result, nil
	case "values", "unique_values":
		objects, err := objectOperands(request, object, operation)
		if err != nil {
			return nil, err
		}
		values := make([]any, 0)
		for _, current := range objects {
			for _, key := range sortedKeys(current) {
				values = append(values, current[key])
			}
		}
		if operation.name == "unique_values" {
			return uniqueValues(values)
		}
		return values, nil
	case "has_key":
		value, err := resolveNamedOperand(request, operation, "value")
		if err != nil {
			return nil, err
		}
		key, err := stringKey(value, "key")
		if err != nil {
			return nil, err
		}
		_, exists := object[key]
		return exists, nil
	case "value_or":
		keyValue, err := resolveNamedOperand(request, operation, "key")
		if err != nil {
			return nil, err
		}
		key, err := stringKey(keyValue, "key")
		if err != nil {
			return nil, err
		}
		if value, exists := object[key]; exists {
			return value, nil
		}
		return resolveNamedOperand(request, operation, "fallback")
	case "find_key":
		wanted, err := resolveNamedOperand(request, operation, "value")
		if err != nil {
			return nil, err
		}
		matcher, err := newValueMatcher(wanted)
		if err != nil {
			return nil, err
		}
		for _, key := range keys {
			equal, err := matcher.matches(object[key])
			if err != nil {
				return nil, err
			}
			if equal {
				return map[string]any{"key": key, "found": true}, nil
			}
		}
		return map[string]any{"key": "", "found": false}, nil
	case "find_key_by":
		for index, key := range keys {
			matches, err := evaluateObjectBool(ctx, request, operation, "expr", key, object[key], index)
			if err != nil {
				return nil, err
			}
			if matches {
				return map[string]any{"key": key, "found": true}, nil
			}
		}
		return map[string]any{"key": "", "found": false}, nil
	case "pick", "omit":
		result := make(map[string]any)
		for index, key := range keys {
			matches, err := evaluateObjectBool(ctx, request, operation, "expr", key, object[key], index)
			if err != nil {
				return nil, err
			}
			if (operation.name == "pick" && matches) || (operation.name == "omit" && !matches) {
				result[key] = object[key]
			}
		}
		return result, nil
	case "pick_keys", "omit_keys":
		value, err := resolveNamedOperand(request, operation, "value")
		if err != nil {
			return nil, err
		}
		selected, err := stringList(value, "keys")
		if err != nil {
			return nil, err
		}
		selection := make(map[string]struct{}, len(selected))
		for _, key := range selected {
			selection[key] = struct{}{}
		}
		result := make(map[string]any)
		for _, key := range keys {
			_, present := selection[key]
			if (operation.name == "pick_keys" && present) || (operation.name == "omit_keys" && !present) {
				result[key] = object[key]
			}
		}
		return result, nil
	case "pick_values", "omit_values":
		value, err := resolveNamedOperand(request, operation, "value")
		if err != nil {
			return nil, err
		}
		selected, err := toList(value)
		if err != nil {
			return nil, fmt.Errorf("values: %w", err)
		}
		// The selection is fixed, so it is marshalled once rather than re-marshalled
		// for every key in the object.
		selectedKeys, err := newValueMatcherSet(selected)
		if err != nil {
			return nil, fmt.Errorf("values: %w", err)
		}
		result := make(map[string]any)
		for _, key := range keys {
			valueKey, err := structuralKey(object[key])
			if err != nil {
				return nil, err
			}
			_, present := selectedKeys[valueKey]
			if (operation.name == "pick_values" && present) || (operation.name == "omit_values" && !present) {
				result[key] = object[key]
			}
		}
		return result, nil
	case "entries":
		result := make([]any, 0, len(keys))
		for _, key := range keys {
			result = append(result, map[string]any{"key": key, "value": object[key]})
		}
		return result, nil
	case "invert":
		result := make(map[string]any, len(object))
		for _, key := range keys {
			inverted, err := stringKey(object[key], fmt.Sprintf("value for key %q", key))
			if err != nil {
				return nil, err
			}
			result[inverted] = key
		}
		return result, nil
	case "assign":
		result := make(map[string]any, len(object))
		for key, value := range object {
			result[key] = value
		}
		values, err := resolveOperandList(request, operation)
		if err != nil {
			return nil, err
		}
		for index, value := range values {
			other, err := toObject(value)
			if err != nil {
				return nil, fmt.Errorf("with[%d]: %w", index, err)
			}
			for key, child := range other {
				result[key] = child
			}
		}
		return result, nil
	case "chunk_entries":
		value, err := resolveNamedOperand(request, operation, "value")
		if err != nil {
			return nil, err
		}
		size, err := toInt(value, "size")
		if err != nil || size <= 0 {
			return nil, fmt.Errorf("size must be a positive integer")
		}
		capacity := 0
		if len(keys) > 0 {
			capacity = (len(keys)-1)/size + 1
		}
		result := make([]any, 0, capacity)
		for start := 0; start < len(keys); start += size {
			chunk := make(map[string]any, min(size, len(keys)-start))
			for _, key := range keys[start:min(len(keys), start+size)] {
				chunk[key] = object[key]
			}
			result = append(result, chunk)
		}
		return result, nil
	case "map_keys", "map_values", "map_entries":
		return transformObject(ctx, request, object, keys, operation)
	case "map_to_list", "filter_map_to_list", "filter_keys", "filter_values":
		return objectToList(ctx, request, object, keys, operation)
	}
	return nil, fmt.Errorf("object operation is not implemented")
}

func objectOperands(request *evaluation, first map[string]any, operation operation) ([]map[string]any, error) {
	result := []map[string]any{first}
	if len(operation.operandList) == 0 {
		return result, nil
	}
	values, err := resolveOperandList(request, operation)
	if err != nil {
		return nil, err
	}
	for index, value := range values {
		object, err := toObject(value)
		if err != nil {
			return nil, fmt.Errorf("with[%d]: %w", index, err)
		}
		result = append(result, object)
	}
	return result, nil
}

func evaluateObject(ctx context.Context, request *evaluation, operation operation, field, key string, value any, index int) (any, error) {
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	result, err := operation.expressions[field].evaluate(request, map[string]any{"key": key, "value": value, "index": index})
	if err != nil {
		return nil, fmt.Errorf("key %q at index %d: %w", key, index, err)
	}
	return result, nil
}

func evaluateObjectBool(ctx context.Context, request *evaluation, operation operation, field, key string, value any, index int) (bool, error) {
	result, err := evaluateObject(ctx, request, operation, field, key, value, index)
	if err != nil {
		return false, err
	}
	return boolValue(result, field)
}

func transformObject(ctx context.Context, request *evaluation, object map[string]any, keys []string, operation operation) (map[string]any, error) {
	result := make(map[string]any, len(object))
	for index, key := range keys {
		newKey := key
		newValue := object[key]
		if operation.name == "map_keys" || operation.name == "map_entries" {
			field := "expr"
			if operation.name == "map_entries" {
				field = "key"
			}
			value, err := evaluateObject(ctx, request, operation, field, key, object[key], index)
			if err != nil {
				return nil, err
			}
			newKey, err = stringKey(value, fmt.Sprintf("key %q expression", key))
			if err != nil {
				return nil, err
			}
		}
		if operation.name == "map_values" || operation.name == "map_entries" {
			field := "expr"
			if operation.name == "map_entries" {
				field = "value"
			}
			value, err := evaluateObject(ctx, request, operation, field, key, object[key], index)
			if err != nil {
				return nil, err
			}
			newValue = value
		}
		result[newKey] = newValue
	}
	return result, nil
}

func objectToList(ctx context.Context, request *evaluation, object map[string]any, keys []string, operation operation) ([]any, error) {
	result := make([]any, 0, len(object))
	for index, key := range keys {
		value := object[key]
		switch operation.name {
		case "map_to_list":
			mapped, err := evaluateObject(ctx, request, operation, "expr", key, value, index)
			if err != nil {
				return nil, err
			}
			result = append(result, mapped)
		case "filter_map_to_list":
			// when guards value, so it has to run before it.
			matches, err := evaluateObjectBool(ctx, request, operation, "when", key, value, index)
			if err != nil {
				return nil, err
			}
			if !matches {
				continue
			}
			mapped, err := evaluateObject(ctx, request, operation, "value", key, value, index)
			if err != nil {
				return nil, err
			}
			result = append(result, mapped)
		case "filter_keys", "filter_values":
			matches, err := evaluateObjectBool(ctx, request, operation, "expr", key, value, index)
			if err != nil {
				return nil, err
			}
			if matches {
				if operation.name == "filter_keys" {
					result = append(result, key)
				} else {
					result = append(result, value)
				}
			}
		}
	}
	return result, nil
}

func stringList(value any, label string) ([]string, error) {
	values, err := toList(value)
	if err != nil {
		return nil, fmt.Errorf("%s must be a list of strings", label)
	}
	result := make([]string, len(values))
	for index, value := range values {
		result[index], err = stringKey(value, fmt.Sprintf("%s[%d]", label, index))
		if err != nil {
			return nil, err
		}
	}
	return result, nil
}

func fromEntries(list []any) (map[string]any, error) {
	result := make(map[string]any, len(list))
	for index, item := range list {
		entry, err := toObject(item)
		if err != nil {
			return nil, fmt.Errorf("item %d: expected {key, value}: %w", index, err)
		}
		if len(entry) != 2 {
			return nil, fmt.Errorf("item %d must contain exactly key and value", index)
		}
		key, err := stringKey(entry["key"], fmt.Sprintf("item %d key", index))
		if err != nil {
			return nil, err
		}
		value, exists := entry["value"]
		if !exists {
			return nil, fmt.Errorf("item %d is missing value", index)
		}
		result[key] = value
	}
	return result, nil
}
