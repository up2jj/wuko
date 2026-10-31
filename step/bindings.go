package step

import (
	"fmt"
	"math"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// DeferredValue marks a binding whose value is unavailable during static validation.
// Builders use ConfigKnown to defer only checks that need such a value. It is never run.
type DeferredValue struct{}

// ConfigKnown reports whether all the supplied configuration paths are statically known.
// A missing field is known: it remains subject to required-field validation.
func ConfigKnown(raw map[string]any, paths ...string) bool {
	for _, path := range paths {
		if path == "" {
			if !knownValue(raw) {
				return false
			}
			continue
		}
		var value any = raw
		for _, part := range strings.Split(path, ".") {
			if _, unknown := value.(DeferredValue); unknown {
				return false
			}
			switch typed := value.(type) {
			case map[string]any:
				value = typed[part]
			case []any:
				i, err := strconv.Atoi(part)
				if err != nil || i < 0 || i >= len(typed) {
					value = nil
				} else {
					value = typed[i]
				}
			default:
				value = nil
			}
		}
		if !knownValue(value) {
			return false
		}
	}
	return true
}

func knownValue(value any) bool {
	switch value := value.(type) {
	case DeferredValue:
		return false
	case map[string]any:
		for _, child := range value {
			if !knownValue(child) {
				return false
			}
		}
	case []any:
		for _, child := range value {
			if !knownValue(child) {
				return false
			}
		}
	}
	return true
}

func validationValues(value any, target reflect.Type) any {
	for target != nil && target.Kind() == reflect.Pointer {
		target = target.Elem()
	}
	switch value := value.(type) {
	case DeferredValue:
		// String validators already understand unresolved templates. Keep that convention
		// for helper validators while ConfigKnown carries the actual unknown-field state.
		if target != nil && target.Kind() == reflect.String {
			return `{{ "" }}`
		}
		if target != nil && target.Kind() == reflect.Slice && target.Elem().Kind() == reflect.String {
			return []string{`{{ "" }}`}
		}
		return nil
	case map[string]any:
		result := make(map[string]any, len(value))
		for name, child := range value {
			var childType reflect.Type
			if target != nil {
				switch target.Kind() {
				case reflect.Struct:
					childType = configField(target, name)
				case reflect.Map:
					childType = target.Elem()
				}
			}
			result[name] = validationValues(child, childType)
		}
		return result
	case []any:
		result := make([]any, len(value))
		for i, child := range value {
			var itemType reflect.Type
			if target != nil && (target.Kind() == reflect.Slice || target.Kind() == reflect.Array) {
				itemType = target.Elem()
			}
			result[i] = validationValues(child, itemType)
		}
		return result
	default:
		return value
	}
}

// CheckBindingType checks an evaluated binding against its destination before YAML decoding,
// which would otherwise silently turn numbers and booleans into strings.
func CheckBindingType(shape any, path []string, value any) error {
	target := reflect.TypeOf(shape)
	for _, name := range path {
		for target != nil && target.Kind() == reflect.Pointer {
			target = target.Elem()
		}
		if target == nil || target.Kind() == reflect.Interface {
			return nil
		}
		switch target.Kind() {
		case reflect.Struct:
			target = configField(target, name)
			if target == nil {
				return fmt.Errorf("unknown configuration field %q", strings.Join(path, "."))
			}
		case reflect.Map, reflect.Slice, reflect.Array:
			target = target.Elem()
		default:
			return fmt.Errorf("invalid configuration path %q", strings.Join(path, "."))
		}
	}
	if err := checkValueType(target, value); err != nil {
		return fmt.Errorf("%s: %w", strings.Join(path, "."), err)
	}
	return nil
}

func configField(target reflect.Type, name string) reflect.Type {
	// Explicit fields take precedence over an embedded shape, allowing a registration
	// to describe a union accepted by a decoder (for example exit-code lists or "any").
	for i := range target.NumField() {
		field := target.Field(i)
		if field.PkgPath != "" {
			continue
		}
		tag := strings.Split(field.Tag.Get("yaml"), ",")
		if len(tag) > 1 && tag[1] == "inline" {
			continue
		}
		key := tag[0]
		if key == "" {
			key = strings.ToLower(field.Name)
		}
		if key == name {
			return field.Type
		}
	}
	for i := range target.NumField() {
		field := target.Field(i)
		if field.PkgPath != "" {
			continue
		}
		if slices.Contains(strings.Split(field.Tag.Get("yaml"), ","), "inline") {
			inner := field.Type
			for inner.Kind() == reflect.Pointer {
				inner = inner.Elem()
			}
			if inner.Kind() == reflect.Struct {
				if found := configField(inner, name); found != nil {
					return found
				}
			}
		}
	}
	return nil
}

func checkValueType(target reflect.Type, value any) error {
	if target == nil || target.Kind() == reflect.Interface {
		return nil
	}
	if value == nil {
		switch target.Kind() {
		case reflect.Pointer, reflect.Map, reflect.Slice:
			return nil
		default:
			return fmt.Errorf("expression returned null, want %s", target)
		}
	}
	for target.Kind() == reflect.Pointer {
		target = target.Elem()
	}
	// Custom YAML fields define their own accepted input types, including scalar
	// representations such as a string duration backed by an integer Go type.
	if _, custom := reflect.New(target).Interface().(interface{ UnmarshalYAML(*yaml.Node) error }); custom {
		return nil
	}
	actual := reflect.ValueOf(value)
	switch target.Kind() {
	case reflect.String:
		if actual.Kind() == reflect.String {
			return nil
		}
	case reflect.Bool:
		if actual.Kind() == reflect.Bool {
			return nil
		}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		if fitsInteger(target, actual) {
			return nil
		}
	case reflect.Float32, reflect.Float64:
		if actual.Kind() >= reflect.Int && actual.Kind() <= reflect.Float64 {
			return nil
		}
	case reflect.Slice, reflect.Array:
		if actual.Kind() == reflect.Slice || actual.Kind() == reflect.Array {
			if target.Kind() == reflect.Array && actual.Len() != target.Len() {
				return fmt.Errorf("expression returned %d items, want %d", actual.Len(), target.Len())
			}
			for i := range actual.Len() {
				if err := checkValueType(target.Elem(), actual.Index(i).Interface()); err != nil {
					return fmt.Errorf("item %d: %w", i, err)
				}
			}
			return nil
		}
	case reflect.Map:
		if actual.Kind() == reflect.Map && actual.Type().Key().Kind() == reflect.String {
			iter := actual.MapRange()
			for iter.Next() {
				if err := checkValueType(target.Elem(), iter.Value().Interface()); err != nil {
					return fmt.Errorf("field %s: %w", iter.Key().String(), err)
				}
			}
			return nil
		}
	case reflect.Struct:
		if actual.Kind() == reflect.Map && actual.Type().Key().Kind() == reflect.String {
			iter := actual.MapRange()
			for iter.Next() {
				name := iter.Key().String()
				field := configField(target, name)
				if field == nil {
					return fmt.Errorf("unknown field %q", name)
				}
				if err := checkValueType(field, iter.Value().Interface()); err != nil {
					return fmt.Errorf("field %s: %w", name, err)
				}
			}
			return nil
		}
	}
	return fmt.Errorf("expression returned %T, want %s", value, target)
}

// fitsInteger accepts any number that the integer destination represents exactly. JSON,
// Lua, and Expr division produce float64, so a whole float counts as an integer.
func fitsInteger(target reflect.Type, actual reflect.Value) bool {
	zero := reflect.New(target).Elem()
	signed := target.Kind() <= reflect.Int64
	switch {
	case actual.CanInt():
		if signed {
			return !zero.OverflowInt(actual.Int())
		}
		return actual.Int() >= 0 && !zero.OverflowUint(uint64(actual.Int()))
	case actual.CanUint():
		if signed {
			return actual.Uint() <= math.MaxInt64 && !zero.OverflowInt(int64(actual.Uint()))
		}
		return !zero.OverflowUint(actual.Uint())
	case actual.CanFloat():
		f := actual.Float()
		if f != math.Trunc(f) || math.IsInf(f, 0) {
			return false
		}
		if signed {
			return f >= math.MinInt64 && f < math.MaxInt64 && !zero.OverflowInt(int64(f))
		}
		return f >= 0 && f < math.MaxUint64 && !zero.OverflowUint(uint64(f))
	}
	return false
}
