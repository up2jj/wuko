package expression

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"strconv"

	"github.com/NimbleMarkets/ntcharts/v2/sparkline"
)

func templateSparkline(width, height int, value any) (string, error) {
	if width <= 0 {
		return "", fmt.Errorf("sparkline width must be positive, got %d", width)
	}
	if height <= 0 {
		return "", fmt.Errorf("sparkline height must be positive, got %d", height)
	}

	values, err := sparklineValues(value)
	if err != nil {
		return "", err
	}
	if len(values) == 0 {
		return "", nil
	}
	if len(values) > width {
		values = values[len(values)-width:]
	}

	chart := sparkline.New(width, height)
	maximum := 0.0
	for _, value := range values {
		maximum = max(maximum, value)
	}
	if maximum > 0 {
		chart.SetMax(maximum)
	}
	chart.PushAll(values)
	chart.Draw()
	return chart.View(), nil
}

func sparklineValues(value any) ([]float64, error) {
	sequence := reflect.ValueOf(value)
	if !sequence.IsValid() || (sequence.Kind() != reflect.Slice && sequence.Kind() != reflect.Array) {
		return nil, fmt.Errorf("sparkline values must be a numeric list, got %T", value)
	}

	values := make([]float64, sequence.Len())
	for index := range sequence.Len() {
		number, err := sparklineNumber(sequence.Index(index))
		if err != nil {
			return nil, fmt.Errorf("sparkline value at index %d: %w", index, err)
		}
		if number < 0 {
			return nil, fmt.Errorf("sparkline value at index %d must not be negative", index)
		}
		if math.IsNaN(number) || math.IsInf(number, 0) {
			return nil, fmt.Errorf("sparkline value at index %d must be finite", index)
		}
		values[index] = number
	}
	return values, nil
}

func sparklineNumber(value reflect.Value) (float64, error) {
	for value.IsValid() && (value.Kind() == reflect.Interface || value.Kind() == reflect.Pointer) {
		if value.IsNil() {
			return 0, fmt.Errorf("must be numeric, got null")
		}
		value = value.Elem()
	}
	if !value.IsValid() {
		return 0, fmt.Errorf("must be numeric, got null")
	}
	if value.CanInterface() {
		if number, ok := value.Interface().(json.Number); ok {
			parsed, err := strconv.ParseFloat(string(number), 64)
			if err != nil {
				return 0, fmt.Errorf("invalid number %q: %w", number, err)
			}
			return parsed, nil
		}
	}

	switch value.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return float64(value.Int()), nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return float64(value.Uint()), nil
	case reflect.Float32, reflect.Float64:
		return value.Float(), nil
	default:
		return 0, fmt.Errorf("must be numeric, got %s", value.Type())
	}
}
