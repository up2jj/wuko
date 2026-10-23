package transform

import (
	"bytes"
	"encoding"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"
	"unicode/utf8"
)

// maxNormalizeDepth bounds the direct walk. Deeper values, which in practice only a cyclic
// structure produces, go through encoding/json, which reports the cycle instead of
// overflowing the stack.
const maxNormalizeDepth = 1000

var (
	jsonMarshalerType = reflect.TypeFor[json.Marshaler]()
	textMarshalerType = reflect.TypeFor[encoding.TextMarshaler]()
	stringType        = reflect.TypeFor[string]()
)

// normalizeResult gives a result the shape of decoded JSON: objects with string keys, lists,
// strings, booleans, null, int for integral numbers and float64 otherwise. It matches a JSON
// round trip (normalizeViaJSON) exactly but walks the value directly, so a large result is
// not held a second and third time as encoded bytes and a decoded copy. The result never
// shares a list or object with the value it came from, because operations hand items of the
// source and literal operands through by reference.
func normalizeResult(value any) (any, error) {
	if err := validateStringMapKeys(reflect.ValueOf(value)); err != nil {
		return nil, err
	}
	return normalizeValue(value, 0)
}

func normalizeValue(value any, depth int) (any, error) {
	if depth > maxNormalizeDepth {
		return normalizeViaJSON(value)
	}
	switch value := value.(type) {
	case nil:
		return nil, nil
	case bool:
		return value, nil
	case string:
		if utf8.ValidString(value) {
			return value, nil
		}
		// encoding/json replaces every invalid byte; reuse it rather than mirror its rules.
		return normalizeViaJSON(value)
	case int:
		return value, nil
	case float64:
		return normalizeFloat(value)
	case json.Number:
		if !validJSONNumber(value.String()) {
			// An invalid literal fails to encode; let encoding/json report it.
			return normalizeViaJSON(value)
		}
		return normalizeNumber(value.String())
	case []any:
		if value == nil {
			return nil, nil
		}
		result := make([]any, len(value))
		for index, item := range value {
			normalized, err := normalizeValue(item, depth+1)
			if err != nil {
				return nil, err
			}
			result[index] = normalized
		}
		return result, nil
	case map[string]any:
		if value == nil {
			return nil, nil
		}
		result := make(map[string]any, len(value))
		for key, item := range value {
			if !utf8.ValidString(key) {
				return normalizeViaJSON(value)
			}
			normalized, err := normalizeValue(item, depth+1)
			if err != nil {
				return nil, err
			}
			result[key] = normalized
		}
		return result, nil
	}
	return normalizeReflected(reflect.ValueOf(value), depth)
}

// normalizeReflected handles typed values: named scalars, typed lists and objects, and
// pointers. Anything that encodes itself or that JSON represents specially falls back to
// encoding/json for that subtree.
func normalizeReflected(value reflect.Value, depth int) (any, error) {
	if value.Type().Implements(jsonMarshalerType) || value.Type().Implements(textMarshalerType) {
		return normalizeViaJSON(value.Interface())
	}
	switch value.Kind() {
	case reflect.Pointer, reflect.Interface:
		if value.IsNil() {
			return nil, nil
		}
		return normalizeValue(value.Elem().Interface(), depth+1)
	case reflect.Bool:
		return value.Bool(), nil
	case reflect.String:
		return normalizeValue(value.String(), depth)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		integer := value.Int()
		if int64(int(integer)) != integer {
			return nil, fmt.Errorf("integer %q is out of range", strconv.FormatInt(integer, 10))
		}
		return int(integer), nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		unsigned := value.Uint()
		if unsigned > math.MaxInt64 || uint64(int(unsigned)) != unsigned {
			return nil, fmt.Errorf("integer %q is out of range", strconv.FormatUint(unsigned, 10))
		}
		return int(unsigned), nil
	case reflect.Float64:
		return normalizeFloat(value.Float())
	case reflect.Slice:
		if value.IsNil() {
			return nil, nil
		}
		if value.Type().Elem().Kind() == reflect.Uint8 {
			// encoding/json writes byte slices as base64 strings.
			return normalizeViaJSON(value.Interface())
		}
		return normalizeReflectedList(value, depth)
	case reflect.Array:
		return normalizeReflectedList(value, depth)
	case reflect.Map:
		if value.Type().Key() != stringType {
			return normalizeViaJSON(value.Interface())
		}
		if value.IsNil() {
			return nil, nil
		}
		result := make(map[string]any, value.Len())
		iterator := value.MapRange()
		for iterator.Next() {
			key := iterator.Key().String()
			if !utf8.ValidString(key) {
				return normalizeViaJSON(value.Interface())
			}
			normalized, err := normalizeValue(iterator.Value().Interface(), depth+1)
			if err != nil {
				return nil, err
			}
			result[key] = normalized
		}
		return result, nil
	}
	// Structs, float32, and values JSON cannot represent.
	return normalizeViaJSON(value.Interface())
}

func normalizeReflectedList(value reflect.Value, depth int) (any, error) {
	result := make([]any, value.Len())
	for index := range value.Len() {
		normalized, err := normalizeValue(value.Index(index).Interface(), depth+1)
		if err != nil {
			return nil, err
		}
		result[index] = normalized
	}
	return result, nil
}

// normalizeFloat reproduces what encoding/json then normalizeNumber make of a float64: the
// encoder writes integral values below 1e21 without a fraction, which normalizeNumber then
// reads as an int, and everything else as a float that parses back to the same value.
func normalizeFloat(number float64) (any, error) {
	if math.IsNaN(number) || math.IsInf(number, 0) {
		return normalizeViaJSON(number)
	}
	if number != math.Trunc(number) || math.Abs(number) >= 1e21 {
		return number, nil
	}
	if math.Abs(number) < 1<<53 && int64(int(int64(number))) == int64(number) {
		return int(number), nil
	}
	// Beyond 2^53 the encoder writes the shortest digits that parse back to the float, not its
	// exact value, so go through the same text.
	return normalizeNumber(strconv.FormatFloat(number, 'f', -1, 64))
}

// normalizeNumber converts a JSON number literal: one with a fraction or exponent becomes a
// float64, any other an int.
func normalizeNumber(text string) (any, error) {
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
}

// normalizeViaJSON is the reference normalization: encode, decode with exact numbers, then
// convert the numbers. The direct walk uses it for values it does not handle itself.
func normalizeViaJSON(value any) (any, error) {
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

// normalizeNumbers converts the numbers of a freshly decoded tree in place.
func normalizeNumbers(value any) (any, error) {
	switch value := value.(type) {
	case json.Number:
		return normalizeNumber(value.String())
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

// validJSONNumber reports whether text is a JSON number literal, which encoding/json requires
// of a json.Number and strconv does not: it also accepts forms such as "+1", ".5" and hex.
func validJSONNumber(text string) bool {
	if text == "" {
		return false
	}
	if text[0] == '-' {
		text = text[1:]
		if text == "" {
			return false
		}
	}
	switch {
	case text[0] == '0':
		text = text[1:]
	case '1' <= text[0] && text[0] <= '9':
		text = strings.TrimLeft(text, "0123456789")
	default:
		return false
	}
	if len(text) >= 2 && text[0] == '.' && isDigit(text[1]) {
		text = strings.TrimLeft(text[1:], "0123456789")
	}
	if len(text) >= 2 && (text[0] == 'e' || text[0] == 'E') {
		text = text[1:]
		if text[0] == '+' || text[0] == '-' {
			text = text[1:]
		}
		if text == "" || !isDigit(text[0]) {
			return false
		}
		text = strings.TrimLeft(text, "0123456789")
	}
	return text == ""
}

func isDigit(character byte) bool { return '0' <= character && character <= '9' }
