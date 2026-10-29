package step

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/expr-lang/expr"
	"github.com/expr-lang/expr/vm"
	wukoexpr "github.com/up2jj/wuko/expression"
)

// PathList is a configuration field holding paths: a literal list, or `{expr: ...}` that
// returns one when the step runs.
type PathList struct {
	field   string
	paths   []string
	program *vm.Program
}

// ParsePathList reads field from a step configuration. An absent field is the zero PathList,
// which reports false from Set.
func ParsePathList(raw map[string]any, field string) (PathList, error) {
	switch value := raw[field].(type) {
	case nil:
		return PathList{field: field}, nil
	case []any:
		paths := make([]string, len(value))
		for index, item := range value {
			path, ok := item.(string)
			if !ok || strings.TrimSpace(path) == "" {
				return PathList{}, fmt.Errorf("%s[%d] must be a non-empty path", field, index)
			}
			paths[index] = path
		}
		return PathList{field: field, paths: paths}, nil
	case map[string]any:
		source, ok := value["expr"].(string)
		if len(value) != 1 || !ok || strings.TrimSpace(source) == "" {
			return PathList{}, fmt.Errorf("%s must be a list or an object containing exactly a non-empty expr", field)
		}
		program, err := wukoexpr.Compile(source, expr.Env(ExpressionEnvironmentShape(nil)), expr.AllowUndefinedVariables())
		if err != nil {
			return PathList{}, fmt.Errorf("compiling %s expr: %w", field, err)
		}
		return PathList{field: field, program: program}, nil
	default:
		return PathList{}, fmt.Errorf("%s must be a list or an object containing expr", field)
	}
}

// PathListReference reports a PathList field's expression to workflow validation.
func PathListReference(raw map[string]any, field string) ExpressionReference {
	binding, _ := raw[field].(map[string]any)
	reference := ExpressionField(binding, "expr")
	reference.Label = field + " expr"
	return reference
}

// Set reports whether the field was configured.
func (list PathList) Set() bool { return list.paths != nil || list.program != nil }

// Resolve returns the configured paths, evaluating the expression form against request.
func (list PathList) Resolve(request Request) ([]string, error) {
	if list.program == nil {
		return list.paths, nil
	}
	value, err := expr.Run(list.program, request.ExpressionEnvironment(nil))
	if err != nil {
		return nil, fmt.Errorf("evaluating %s expr: %w", list.field, err)
	}
	items := reflect.ValueOf(value)
	if !items.IsValid() || (items.Kind() != reflect.Slice && items.Kind() != reflect.Array) {
		return nil, fmt.Errorf("%s expr must return a list of paths", list.field)
	}
	paths := make([]string, items.Len())
	for index := range items.Len() {
		path, ok := items.Index(index).Interface().(string)
		if !ok || strings.TrimSpace(path) == "" {
			return nil, fmt.Errorf("%s expr item %d must be a non-empty path", list.field, index)
		}
		paths[index] = path
	}
	return paths, nil
}
