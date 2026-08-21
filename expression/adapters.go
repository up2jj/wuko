package expression

import (
	"strings"
	"text/template"

	"github.com/expr-lang/expr"
	"github.com/expr-lang/expr/vm"
)

// TemplateFuncs returns the functions exposed to Wuko Go templates.
func TemplateFuncs() template.FuncMap {
	return template.FuncMap{
		"lower":         strings.ToLower,
		"upper":         strings.ToUpper,
		"trim":          strings.TrimSpace,
		"trimPrefix":    func(prefix, value string) string { return strings.TrimPrefix(value, prefix) },
		"trimSuffix":    func(suffix, value string) string { return strings.TrimSuffix(value, suffix) },
		"contains":      func(substring, value string) bool { return strings.Contains(value, substring) },
		"hasPrefix":     func(prefix, value string) bool { return strings.HasPrefix(value, prefix) },
		"hasSuffix":     func(suffix, value string) bool { return strings.HasSuffix(value, suffix) },
		"replace":       func(old, replacement, value string) string { return strings.ReplaceAll(value, old, replacement) },
		"split":         func(separator, value string) []string { return strings.Split(value, separator) },
		"join":          join,
		"default":       defaultValue,
		"coalesce":      coalesce,
		"required":      required,
		"indent":        indent,
		"nindent":       nindent,
		"list":          list,
		"dict":          dict,
		"get":           get,
		"hasKey":        hasKey,
		"keys":          keys,
		"sortAlpha":     sortAlpha,
		"toJSON":        toJSON,
		"toJSONCompact": toJSONCompact,
		"toYAML":        toYAML,
	}
}

// Compile compiles an Expr expression with Wuko's shared helper functions.
func Compile(source string, options ...expr.Option) (*vm.Program, error) {
	allOptions := append(exprOptions(), options...)
	return expr.Compile(source, allOptions...)
}

// Eval compiles and evaluates an Expr expression with Wuko's shared helpers.
func Eval(source string, environment any) (any, error) {
	program, err := Compile(source, expr.Env(environment))
	if err != nil {
		return nil, err
	}
	return expr.Run(program, environment)
}

func exprOptions() []expr.Option {
	return []expr.Option{
		expr.Function("default", func(values ...any) (any, error) {
			return defaultValue(values[1], values[0]), nil
		}, new(func(any, any) any)),
		expr.Function("coalesce", func(values ...any) (any, error) {
			return coalesce(values...), nil
		}, new(func(...any) any)),
		expr.Function("required", func(values ...any) (any, error) {
			return required(values[1].(string), values[0])
		}, new(func(any, string) any)),
		expr.Function("indent", func(values ...any) (any, error) {
			return indent(values[1].(int), values[0].(string))
		}, new(func(string, int) string)),
		expr.Function("nindent", func(values ...any) (any, error) {
			return nindent(values[1].(int), values[0].(string))
		}, new(func(string, int) string)),
		expr.Function("list", func(values ...any) (any, error) {
			return list(values...), nil
		}, new(func(...any) []any)),
		expr.Function("dict", func(values ...any) (any, error) {
			return dict(values...)
		}, new(func(...any) map[string]any)),
		expr.Function("get", func(values ...any) (any, error) {
			return get(values[1].(string), values[0])
		}, new(func(any, string) any)),
		expr.Function("hasKey", func(values ...any) (any, error) {
			return hasKey(values[1].(string), values[0])
		}, new(func(any, string) bool)),
		expr.Function("keys", func(values ...any) (any, error) {
			return keys(values[0])
		}, new(func(any) []string)),
		expr.Function("sortAlpha", func(values ...any) (any, error) {
			return sortAlpha(values[0])
		}, new(func(any) []string)),
		expr.Function("toJSON", func(values ...any) (any, error) {
			return toJSON(values[0])
		}, new(func(any) string)),
		expr.Function("toJSONCompact", func(values ...any) (any, error) {
			return toJSONCompact(values[0])
		}, new(func(any) string)),
		expr.Function("toYAML", func(values ...any) (any, error) {
			return toYAML(values[0])
		}, new(func(any) string)),
	}
}
