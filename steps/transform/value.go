package transform

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"github.com/expr-lang/expr"
	"github.com/expr-lang/expr/vm"
	"github.com/up2jj/wuko/step"
)

// evaluation carries one Run's expression environment. The standard roots are built
// once and reused for every item: Request.ExpressionEnvironment allocates the whole
// root map plus one closure per helper, and transform evaluates callbacks once per
// element, so rebuilding it per item dominated the cost of a large pipeline. One
// evaluation belongs to one Run, which is single-goroutine, so the shared map is
// never touched concurrently.
type evaluation struct {
	request     step.Request
	environment map[string]any
}

func newEvaluation(request step.Request) *evaluation {
	return &evaluation{request: request, environment: request.ExpressionEnvironment(nil)}
}

// run evaluates one compiled expression with callback-local roots layered over the
// shared environment. Locals are removed again so they never leak into the next
// operation, whose callbacks declare a different local shape.
func (evaluation *evaluation) run(program *vm.Program, locals map[string]any) (any, error) {
	for name, value := range locals {
		evaluation.environment[name] = value
	}
	value, err := expr.Run(program, evaluation.environment)
	for name := range locals {
		delete(evaluation.environment, name)
	}
	return value, err
}

func (expression *compiledExpression) evaluate(request *evaluation, locals map[string]any) (any, error) {
	if expression == nil {
		return nil, fmt.Errorf("expression is missing")
	}
	if expression.program == nil {
		return nil, fmt.Errorf("%s contains an unresolved template", expression.label)
	}
	value, err := request.run(expression.program, locals)
	if err != nil {
		return nil, fmt.Errorf("evaluating %s: %w", expression.label, err)
	}
	return value, nil
}

func (operand operand) resolve(request *evaluation) (any, error) {
	if operand.expression != nil {
		return operand.expression.evaluate(request, nil)
	}
	return operand.literal, nil
}

func checkContext(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

func kindOf(value any) valueKind {
	if _, err := toList(value); err == nil {
		return kindList
	}
	if _, err := toObject(value); err == nil {
		return kindObject
	}
	return kindScalar
}

func toList(value any) ([]any, error) {
	if value == nil {
		return nil, fmt.Errorf("expected list, got null")
	}
	if list, ok := value.([]any); ok {
		return list, nil
	}
	reflected := reflect.ValueOf(value)
	if reflected.Kind() != reflect.Slice && reflected.Kind() != reflect.Array {
		return nil, fmt.Errorf("expected list, got %T", value)
	}
	result := make([]any, reflected.Len())
	for index := range reflected.Len() {
		result[index] = reflected.Index(index).Interface()
	}
	return result, nil
}

func toObject(value any) (map[string]any, error) {
	if value == nil {
		return nil, fmt.Errorf("expected object, got null")
	}
	if object, ok := value.(map[string]any); ok {
		return object, nil
	}
	reflected := reflect.ValueOf(value)
	if reflected.Kind() != reflect.Map || reflected.Type().Key().Kind() != reflect.String {
		return nil, fmt.Errorf("expected object with string keys, got %T", value)
	}
	result := make(map[string]any, reflected.Len())
	for iterator := reflected.MapRange(); iterator.Next(); {
		result[iterator.Key().String()] = iterator.Value().Interface()
	}
	return result, nil
}

func sortedKeys(object map[string]any) []string {
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

func structuralKey(value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("value is not JSON-compatible: %w", err)
	}
	return string(data), nil
}

// valueMatcher marshals one fixed value and compares candidates against that key.
// Structural equality marshals both sides, so comparing a single constant against a
// whole list re-marshalled the constant once per element.
type valueMatcher struct{ key string }

func newValueMatcher(value any) (valueMatcher, error) {
	key, err := structuralKey(value)
	return valueMatcher{key: key}, err
}

func (matcher valueMatcher) matches(candidate any) (bool, error) {
	key, err := structuralKey(candidate)
	if err != nil {
		return false, err
	}
	return key == matcher.key, nil
}

func structuralKeys(values []any) ([]string, error) {
	result := make([]string, len(values))
	for index, value := range values {
		key, err := structuralKey(value)
		if err != nil {
			return nil, fmt.Errorf("item %d: %w", index, err)
		}
		result[index] = key
	}
	return result, nil
}

func newValueMatcherSet(values []any) (map[string]struct{}, error) {
	result := make(map[string]struct{}, len(values))
	for index, value := range values {
		key, err := structuralKey(value)
		if err != nil {
			return nil, fmt.Errorf("item %d: %w", index, err)
		}
		result[key] = struct{}{}
	}
	return result, nil
}

func equalValues(left, right any) (bool, error) {
	leftKey, err := structuralKey(left)
	if err != nil {
		return false, err
	}
	rightKey, err := structuralKey(right)
	if err != nil {
		return false, err
	}
	return leftKey == rightKey, nil
}

func compareValues(left, right any) (int, error) {
	leftNumber, leftIsNumber, leftInteger := numberValue(left)
	rightNumber, rightIsNumber, rightInteger := numberValue(right)
	if leftIsNumber || rightIsNumber {
		if !leftIsNumber || !rightIsNumber {
			return 0, fmt.Errorf("cannot compare %T and %T", left, right)
		}
		if math.IsNaN(leftNumber) || math.IsNaN(rightNumber) {
			return 0, fmt.Errorf("cannot compare NaN")
		}
		if leftInteger && rightInteger {
			leftInt, leftOK := integerValue(left)
			rightInt, rightOK := integerValue(right)
			if !leftOK || !rightOK {
				return 0, fmt.Errorf("integer value is out of range")
			}
			switch {
			case leftInt < rightInt:
				return -1, nil
			case leftInt > rightInt:
				return 1, nil
			default:
				return 0, nil
			}
		}
		switch {
		case leftNumber < rightNumber:
			return -1, nil
		case leftNumber > rightNumber:
			return 1, nil
		default:
			return 0, nil
		}
	}
	leftString, leftOK := left.(string)
	rightString, rightOK := right.(string)
	if leftOK && rightOK {
		return bytes.Compare([]byte(leftString), []byte(rightString)), nil
	}
	return 0, fmt.Errorf("values of type %T and %T are not orderable", left, right)
}

func numberValue(value any) (float64, bool, bool) {
	switch value := value.(type) {
	case int:
		return float64(value), true, true
	case int8:
		return float64(value), true, true
	case int16:
		return float64(value), true, true
	case int32:
		return float64(value), true, true
	case int64:
		return float64(value), true, true
	case uint:
		return float64(value), true, true
	case uint8:
		return float64(value), true, true
	case uint16:
		return float64(value), true, true
	case uint32:
		return float64(value), true, true
	case uint64:
		return float64(value), true, true
	case float32:
		return float64(value), true, false
	case float64:
		return value, true, false
	default:
		return 0, false, false
	}
}

func integerValue(value any) (int64, bool) {
	switch value := value.(type) {
	case int:
		return int64(value), true
	case int8:
		return int64(value), true
	case int16:
		return int64(value), true
	case int32:
		return int64(value), true
	case int64:
		return value, true
	case uint:
		if uint64(value) <= math.MaxInt64 {
			return int64(value), true
		}
	case uint8:
		return int64(value), true
	case uint16:
		return int64(value), true
	case uint32:
		return int64(value), true
	case uint64:
		if value <= math.MaxInt64 {
			return int64(value), true
		}
	}
	return 0, false
}

func toInt(value any, label string) (int, error) {
	integer, ok := integerValue(value)
	if !ok || int64(int(integer)) != integer {
		return 0, fmt.Errorf("%s must be an integer", label)
	}
	return int(integer), nil
}

func boolValue(value any, label string) (bool, error) {
	boolean, ok := value.(bool)
	if !ok {
		return false, fmt.Errorf("%s must return a boolean, got %T", label, value)
	}
	return boolean, nil
}

func stringKey(value any, label string) (string, error) {
	key, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("%s must return a string object key, got %T", label, value)
	}
	return key, nil
}

func zeroValue(value any) bool {
	if value == nil {
		return true
	}
	switch value := value.(type) {
	case bool:
		return !value
	case string:
		return value == ""
	}
	number, ok, _ := numberValue(value)
	return ok && number == 0
}

func normalizeResult(value any) (any, error) {
	if err := validateStringMapKeys(reflect.ValueOf(value)); err != nil {
		return nil, err
	}
	data, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("result is not JSON-compatible: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var normalized any
	if err := decoder.Decode(&normalized); err != nil {
		return nil, fmt.Errorf("normalizing result: %w", err)
	}
	return normalizeNumbers(normalized)
}

func validateStringMapKeys(value reflect.Value) error {
	if !value.IsValid() {
		return nil
	}
	for value.Kind() == reflect.Interface || value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return nil
		}
		value = value.Elem()
	}
	switch value.Kind() {
	case reflect.Map:
		if value.Type().Key().Kind() != reflect.String {
			return fmt.Errorf("result contains object with non-string keys of type %s", value.Type().Key())
		}
		iterator := value.MapRange()
		for iterator.Next() {
			if err := validateStringMapKeys(iterator.Value()); err != nil {
				return err
			}
		}
	case reflect.Slice, reflect.Array:
		for index := range value.Len() {
			if err := validateStringMapKeys(value.Index(index)); err != nil {
				return err
			}
		}
	}
	return nil
}

func normalizeNumbers(value any) (any, error) {
	switch value := value.(type) {
	case json.Number:
		text := value.String()
		if strings.ContainsAny(text, ".eE") {
			number, err := strconv.ParseFloat(text, 64)
			if err != nil || math.IsNaN(number) || math.IsInf(number, 0) {
				return nil, fmt.Errorf("invalid JSON number %q", text)
			}
			return number, nil
		}
		integer, err := strconv.ParseInt(text, 10, 64)
		if err != nil || int64(int(integer)) != integer {
			return nil, fmt.Errorf("integer %q is out of range", text)
		}
		return int(integer), nil
	case []any:
		for index := range value {
			normalized, err := normalizeNumbers(value[index])
			if err != nil {
				return nil, err
			}
			value[index] = normalized
		}
	case map[string]any:
		for key, child := range value {
			normalized, err := normalizeNumbers(child)
			if err != nil {
				return nil, err
			}
			value[key] = normalized
		}
	}
	return value, nil
}
