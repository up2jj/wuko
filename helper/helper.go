// Package helper defines workflow-scoped extension functions shared by Expr,
// Go templates, and Lua.
package helper

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"text/template"
)

// Call invokes one helper with JSON-compatible arguments and result.
type Call func(context.Context, []any) (any, error)

// Set is an immutable-by-convention collection keyed by exposed helper name.
type Set map[string]Call

// Clone returns a shallow copy. Call implementations are safe to share.
func (set Set) Clone() Set { return maps.Clone(set) }

// Names returns exposed helper names in deterministic order.
func (set Set) Names() []string { return slices.Sorted(maps.Keys(set)) }

// Functions binds helpers to ctx for Expr runtime environments.
func (set Set) Functions(ctx context.Context) map[string]any {
	functions := make(map[string]any, len(set))
	for name, call := range set {
		name, call := name, call
		functions[name] = func(args ...any) (any, error) {
			value, err := call(ctx, args)
			if err != nil {
				return nil, fmt.Errorf("helper %s: %w", name, err)
			}
			return value, nil
		}
	}
	return functions
}

// TemplateFuncs binds helpers to ctx for Go templates.
func (set Set) TemplateFuncs(ctx context.Context) template.FuncMap {
	functions := make(template.FuncMap, len(set))
	for name, call := range set {
		name, call := name, call
		functions[name] = func(args ...any) (any, error) {
			value, err := call(ctx, args)
			if err != nil {
				return nil, fmt.Errorf("helper %s: %w", name, err)
			}
			return value, nil
		}
	}
	return functions
}
