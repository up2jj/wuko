package expression

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

func parseJSON(source string) (any, error) {
	decoder := json.NewDecoder(strings.NewReader(source))
	decoder.UseNumber()

	var decoded any
	if err := decoder.Decode(&decoded); err != nil {
		if err == io.EOF {
			return nil, fmt.Errorf("JSON input is empty")
		}
		return nil, fmt.Errorf("decoding JSON: %w", err)
	}

	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("multiple JSON values are not supported")
		}
		return nil, fmt.Errorf("invalid data after first JSON value: %w", err)
	}

	return normalizeParsedValue(decoded)
}

func parseYAML(source string) (any, error) {
	decoder := yaml.NewDecoder(strings.NewReader(source))

	var decoded any
	if err := decoder.Decode(&decoded); err != nil {
		if err == io.EOF {
			return nil, fmt.Errorf("YAML input is empty")
		}
		return nil, fmt.Errorf("decoding YAML: %w", err)
	}

	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("multiple YAML documents are not supported")
		}
		return nil, fmt.Errorf("invalid data after first YAML document: %w", err)
	}

	return normalizeParsedValue(decoded)
}

func normalizeParsedValue(value any) (any, error) {
	switch typed := value.(type) {
	case nil, string, bool, int64, uint64, float64:
		return typed, nil
	case json.Number:
		return normalizeJSONNumber(typed)
	case int:
		return int64(typed), nil
	case int8:
		return int64(typed), nil
	case int16:
		return int64(typed), nil
	case int32:
		return int64(typed), nil
	case uint:
		return uint64(typed), nil
	case uint8:
		return uint64(typed), nil
	case uint16:
		return uint64(typed), nil
	case uint32:
		return uint64(typed), nil
	case float32:
		return float64(typed), nil
	case []any:
		result := make([]any, len(typed))
		for i, item := range typed {
			normalized, err := normalizeParsedValue(item)
			if err != nil {
				return nil, fmt.Errorf("item %d: %w", i, err)
			}
			result[i] = normalized
		}
		return result, nil
	case map[string]any:
		return normalizeParsedMap(typed)
	case map[any]any:
		converted := make(map[string]any, len(typed))
		for key, item := range typed {
			name, ok := key.(string)
			if !ok {
				return nil, fmt.Errorf("mapping key must be a string, got %T", key)
			}
			converted[name] = item
		}
		return normalizeParsedMap(converted)
	default:
		return nil, fmt.Errorf("unsupported parsed value %T", value)
	}
}

func normalizeParsedMap(value map[string]any) (map[string]any, error) {
	result := make(map[string]any, len(value))
	for key, item := range value {
		normalized, err := normalizeParsedValue(item)
		if err != nil {
			return nil, fmt.Errorf("field %q: %w", key, err)
		}
		result[key] = normalized
	}
	return result, nil
}

func normalizeJSONNumber(value json.Number) (any, error) {
	text := value.String()
	if !strings.ContainsAny(text, ".eE") {
		if signed, err := value.Int64(); err == nil {
			return signed, nil
		}
		if unsigned, err := strconv.ParseUint(text, 10, 64); err == nil {
			return unsigned, nil
		}
		return nil, fmt.Errorf("JSON integer %q is out of range", text)
	}

	number, err := value.Float64()
	if err != nil {
		return nil, fmt.Errorf("converting JSON number %q: %w", text, err)
	}
	return number, nil
}
