// Package expression provides the pure functions shared by Wuko's Go templates
// and Expr expressions.
package expression

import (
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

func empty(value any) bool {
	if value == nil {
		return true
	}
	return emptyValue(reflect.ValueOf(value))
}

func emptyValue(value reflect.Value) bool {
	if !value.IsValid() {
		return true
	}
	switch value.Kind() {
	case reflect.Array, reflect.Map, reflect.Slice, reflect.String:
		return value.Len() == 0
	case reflect.Bool:
		return !value.Bool()
	case reflect.Complex64, reflect.Complex128:
		return value.Complex() == 0
	case reflect.Chan, reflect.Func, reflect.Pointer:
		return value.IsNil()
	case reflect.Interface:
		return value.IsNil() || emptyValue(value.Elem())
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return value.Int() == 0
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return value.Uint() == 0
	case reflect.Float32, reflect.Float64:
		return value.Float() == 0
	case reflect.Invalid:
		return true
	default:
		return false
	}
}

func defaultValue(fallback, value any) any {
	if empty(value) {
		return fallback
	}
	return value
}

func coalesce(values ...any) any {
	for _, value := range values {
		if !empty(value) {
			return value
		}
	}
	return nil
}

func required(message string, value any) (any, error) {
	if empty(value) {
		return nil, fmt.Errorf("%s", message)
	}
	return value, nil
}

func indent(spaces int, value string) (string, error) {
	if spaces < 0 {
		return "", fmt.Errorf("indent width must not be negative")
	}
	if value == "" || spaces == 0 {
		return value, nil
	}
	prefix := strings.Repeat(" ", spaces)
	lines := strings.Split(value, "\n")
	for i := range lines {
		if i == len(lines)-1 && lines[i] == "" {
			continue
		}
		lines[i] = prefix + lines[i]
	}
	return strings.Join(lines, "\n"), nil
}

func nindent(spaces int, value string) (string, error) {
	indented, err := indent(spaces, value)
	if err != nil {
		return "", err
	}
	return "\n" + indented, nil
}

func list(values ...any) []any {
	return values
}

func dict(values ...any) (map[string]any, error) {
	if len(values)%2 != 0 {
		return nil, fmt.Errorf("dict requires an even number of arguments")
	}
	result := make(map[string]any, len(values)/2)
	for i := 0; i < len(values); i += 2 {
		key, ok := values[i].(string)
		if !ok {
			return nil, fmt.Errorf("dict key at argument %d is %T, want string", i+1, values[i])
		}
		result[key] = values[i+1]
	}
	return result, nil
}

func get(key string, collection any) (any, error) {
	value, err := stringMapValue(collection)
	if err != nil {
		return nil, err
	}
	entry := value.MapIndex(reflect.ValueOf(key).Convert(value.Type().Key()))
	if !entry.IsValid() {
		return nil, nil
	}
	return entry.Interface(), nil
}

func hasKey(key string, collection any) (bool, error) {
	value, err := stringMapValue(collection)
	if err != nil {
		return false, err
	}
	entry := value.MapIndex(reflect.ValueOf(key).Convert(value.Type().Key()))
	return entry.IsValid(), nil
}

func keys(collection any) ([]string, error) {
	value, err := stringMapValue(collection)
	if err != nil {
		return nil, err
	}
	keys := make([]string, 0, value.Len())
	iterator := value.MapRange()
	for iterator.Next() {
		keys = append(keys, iterator.Key().String())
	}
	slices.Sort(keys)
	return keys, nil
}

func stringMapValue(collection any) (reflect.Value, error) {
	if collection == nil {
		return reflect.Value{}, fmt.Errorf("expected map with string keys, got <nil>")
	}
	value := reflect.ValueOf(collection)
	if value.Kind() != reflect.Map || value.Type().Key().Kind() != reflect.String {
		return reflect.Value{}, fmt.Errorf("expected map with string keys, got %T", collection)
	}
	return value, nil
}

func sortAlpha(collection any) ([]string, error) {
	if collection == nil {
		return nil, fmt.Errorf("expected list or array of strings, got <nil>")
	}
	value := reflect.ValueOf(collection)
	if value.Kind() != reflect.Array && value.Kind() != reflect.Slice {
		return nil, fmt.Errorf("expected list or array of strings, got %T", collection)
	}
	result := make([]string, value.Len())
	for i := range value.Len() {
		item := value.Index(i)
		if item.Kind() == reflect.Interface {
			if item.IsNil() {
				return nil, fmt.Errorf("item %d is <nil>, want string", i)
			}
			item = item.Elem()
		}
		if item.Kind() != reflect.String {
			return nil, fmt.Errorf("item %d is %s, want string", i, item.Type())
		}
		result[i] = item.String()
	}
	slices.Sort(result)
	return result, nil
}

func join(separator string, collection any) (string, error) {
	values, err := stringSlice(collection)
	if err != nil {
		return "", err
	}
	return strings.Join(values, separator), nil
}

func stringSlice(collection any) ([]string, error) {
	if collection == nil {
		return nil, fmt.Errorf("expected list or array of strings, got <nil>")
	}
	value := reflect.ValueOf(collection)
	if value.Kind() != reflect.Array && value.Kind() != reflect.Slice {
		return nil, fmt.Errorf("expected list or array of strings, got %T", collection)
	}
	result := make([]string, value.Len())
	for i := range value.Len() {
		item := value.Index(i)
		if item.Kind() == reflect.Interface {
			if item.IsNil() {
				return nil, fmt.Errorf("item %d is <nil>, want string", i)
			}
			item = item.Elem()
		}
		if item.Kind() != reflect.String {
			return nil, fmt.Errorf("item %d is %s, want string", i, item.Type())
		}
		result[i] = item.String()
	}
	return result, nil
}

func toJSON(value any) (string, error) {
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func toJSONCompact(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func toYAML(value any) (string, error) {
	encoded, err := yaml.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}
