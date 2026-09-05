// Package provider discovers and carries read-only execution-provider contexts.
package provider

import (
	"context"
	"fmt"
	"maps"
	"reflect"
	"regexp"
	"slices"
)

var namePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Schema describes the statically known shape of a provider context. A zero Schema is a scalar
// leaf. Open objects accept every field below them; closed objects accept only Fields.
type Schema struct {
	Open   bool
	Fields map[string]Schema
}

// Scalar returns a scalar schema leaf.
func Scalar() Schema { return Schema{} }

// Object returns a closed object schema containing fields.
func Object(fields map[string]Schema) Schema {
	return Schema{Fields: cloneSchemaFields(fields)}
}

// OpenObject returns an object whose nested shape is intentionally unspecified.
func OpenObject() Schema { return Schema{Open: true} }

// Provider detects and loads one top-level execution context.
type Provider interface {
	Name() string
	Schema() Schema
	Load(context.Context, map[string]string) (value map[string]any, active bool, err error)
}

// Set carries every registered provider schema and the values of active providers.
type Set struct {
	Schemas map[string]Schema
	Values  map[string]map[string]any
}

// Clone returns a recursively independent provider set.
func (set Set) Clone() Set {
	result := Set{
		Schemas: make(map[string]Schema, len(set.Schemas)),
		Values:  make(map[string]map[string]any, len(set.Values)),
	}
	for name, schema := range set.Schemas {
		result.Schemas[name] = cloneSchema(schema)
	}
	for name, value := range set.Values {
		result.Values[name] = cloneMap(value)
	}
	return result
}

// ValidateNames rejects context roots that would overwrite a Wuko runtime root.
func (set Set) ValidateNames(reserved map[string]struct{}) error {
	for _, name := range set.Names() {
		if !namePattern.MatchString(name) {
			return fmt.Errorf("provider name %q is not a valid identifier", name)
		}
		if _, exists := reserved[name]; exists {
			return fmt.Errorf("provider name %q conflicts with a runtime root", name)
		}
	}
	for _, name := range slices.Sorted(maps.Keys(set.Values)) {
		if _, registered := set.Schemas[name]; !registered {
			return fmt.Errorf("provider value %q has no schema", name)
		}
	}
	return nil
}

// Registry owns an ordered set of execution-context providers. Its zero value is usable.
type Registry struct {
	providers []Provider
	names     map[string]struct{}
}

// Register adds one provider.
func (registry *Registry) Register(item Provider) error {
	if item == nil {
		return fmt.Errorf("provider is required")
	}
	name := item.Name()
	if !namePattern.MatchString(name) {
		return fmt.Errorf("provider name %q is not a valid identifier", name)
	}
	if registry.names == nil {
		registry.names = make(map[string]struct{})
	}
	if _, exists := registry.names[name]; exists {
		return fmt.Errorf("provider %q is already registered", name)
	}
	registry.names[name] = struct{}{}
	registry.providers = append(registry.providers, item)
	return nil
}

// Load detects every registered provider and returns one immutable invocation snapshot.
func (registry *Registry) Load(ctx context.Context, environment map[string]string) (Set, error) {
	set := Set{
		Schemas: make(map[string]Schema, len(registry.providers)),
		Values:  make(map[string]map[string]any),
	}
	for _, item := range registry.providers {
		if err := ctx.Err(); err != nil {
			return Set{}, err
		}
		name := item.Name()
		set.Schemas[name] = cloneSchema(item.Schema())
		value, active, err := item.Load(ctx, maps.Clone(environment))
		if err != nil {
			return Set{}, fmt.Errorf("loading provider %q: %w", name, err)
		}
		if active {
			set.Values[name] = cloneMap(value)
		}
	}
	return set, nil
}

// Names returns registered provider names in deterministic order.
func (set Set) Names() []string { return slices.Sorted(maps.Keys(set.Schemas)) }

func cloneSchema(schema Schema) Schema {
	return Schema{Open: schema.Open, Fields: cloneSchemaFields(schema.Fields)}
}

func cloneSchemaFields(fields map[string]Schema) map[string]Schema {
	if fields == nil {
		return nil
	}
	result := make(map[string]Schema, len(fields))
	for name, schema := range fields {
		result[name] = cloneSchema(schema)
	}
	return result
}

func cloneMap(source map[string]any) map[string]any {
	result := make(map[string]any, len(source))
	for key, value := range source {
		result[key] = clone(value)
	}
	return result
}

func clone(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneMap(typed)
	case []any:
		result := make([]any, len(typed))
		for index, item := range typed {
			result[index] = clone(item)
		}
		return result
	default:
		return cloneReflect(value)
	}
}

func cloneReflect(value any) any {
	source := reflect.ValueOf(value)
	switch source.Kind() {
	case reflect.Slice:
		if source.IsNil() {
			return value
		}
		result := reflect.MakeSlice(source.Type(), source.Len(), source.Len())
		for index := range source.Len() {
			setCloned(result.Index(index), source.Index(index))
		}
		return result.Interface()
	case reflect.Map:
		if source.IsNil() {
			return value
		}
		result := reflect.MakeMapWithSize(source.Type(), source.Len())
		for iterator := source.MapRange(); iterator.Next(); {
			element := reflect.New(source.Type().Elem()).Elem()
			setCloned(element, iterator.Value())
			result.SetMapIndex(iterator.Key(), element)
		}
		return result.Interface()
	default:
		return value
	}
}

func setCloned(target, source reflect.Value) {
	cloned := clone(source.Interface())
	if cloned == nil {
		target.SetZero()
		return
	}
	target.Set(reflect.ValueOf(cloned))
}
