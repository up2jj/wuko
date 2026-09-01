package expression

import (
	"fmt"
	"strings"
	"text/template"

	"github.com/expr-lang/expr"
	"github.com/expr-lang/expr/vm"
)

// TemplateFuncs returns the functions exposed to Wuko Go templates.
func TemplateFuncs() template.FuncMap {
	return TemplateFuncsWithSecret(nil)
}

// SecretResolver is the capability exposed to template and expression adapters.
type SecretResolver interface {
	Resolve(string) (string, error)
}

// TemplateFuncsWithSecret returns the shared template functions with secret bound to one
// workflow occurrence. A nil resolver keeps parsing and static validation available, but calls
// fail at execution time.
func TemplateFuncsWithSecret(resolver SecretResolver) template.FuncMap {
	secret := func(reference string) (string, error) {
		if resolver == nil {
			return "", fmt.Errorf("secret resolver is unavailable")
		}
		return resolver.Resolve(reference)
	}
	return template.FuncMap{
		"secret":        secret,
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
		"slugify":       templateSlugify,
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
		"parseJSON":     parseJSON,
		"parseYAML":     parseYAML,
		"parseTime":     templateParseTime,
		"addTime":       templateAddTime,
		"formatTime":    templateFormatTime,
		"parseURI":      ParseURI,
		"buildURI":      BuildURI,
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
		// Expr's now() would let any expression read the clock, which would defeat the
		// time step's recordable, --var-overridable capture. Disabling it fails such an
		// expression at compile time with "unknown name now".
		expr.DisableBuiltin("now"),
		expr.Function("default", func(values ...any) (any, error) {
			return defaultValue(values[1], values[0]), nil
		}, new(func(any, any) any)),
		expr.Function("coalesce", func(values ...any) (any, error) {
			return coalesce(values...), nil
		}, new(func(...any) any)),
		expr.Function("slugify", exprSlugify, new(func(...any) string)),
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
		expr.Function("indexBy", func(values ...any) (any, error) {
			return indexBy(values[0], values[1].(string))
		}, new(func(any, string) map[string]any)),
		expr.Function("chunk", func(values ...any) (any, error) {
			return chunk(values[0], values[1].(int))
		}, new(func(any, int) [][]any)),
		expr.Function("toJSON", func(values ...any) (any, error) {
			return toJSON(values[0])
		}, new(func(any) string)),
		expr.Function("toJSONCompact", func(values ...any) (any, error) {
			return toJSONCompact(values[0])
		}, new(func(any) string)),
		expr.Function("toYAML", func(values ...any) (any, error) {
			return toYAML(values[0])
		}, new(func(any) string)),
		expr.Function("parseJSON", func(values ...any) (any, error) {
			return parseJSON(values[0].(string))
		}, new(func(string) any)),
		expr.Function("parseYAML", func(values ...any) (any, error) {
			return parseYAML(values[0].(string))
		}, new(func(string) any)),
		expr.Function("parseTime", exprParseTime, new(func(...any) string)),
		expr.Function("addTime", exprAddTime, new(func(...any) string)),
		expr.Function("formatTime", exprFormatTime, new(func(...any) string)),
		expr.Function("parseURI", func(values ...any) (any, error) {
			return ParseURI(values[0].(string))
		}, new(func(string) map[string]any)),
		expr.Function("buildURI", func(values ...any) (any, error) {
			return BuildURI(values[0].(map[string]any))
		}, new(func(map[string]any) string)),
	}
}

func templateParseTime(values ...any) (string, error) {
	value, layout, timezone, err := templateTimeArguments("parseTime", values)
	if err != nil {
		return "", err
	}
	return ParseTime(value, layout, timezone)
}

func templateFormatTime(values ...any) (string, error) {
	value, layout, timezone, err := templateTimeArguments("formatTime", values)
	if err != nil {
		return "", err
	}
	return FormatTime(value, layout, timezone)
}

func templateTimeArguments(name string, values []any) (value, layout, timezone string, err error) {
	if len(values) != 2 && len(values) != 3 {
		return "", "", "", fmt.Errorf("%s expects a layout, optional timezone, and piped time value", name)
	}
	stringsByPosition := make([]string, len(values))
	for i, candidate := range values {
		text, ok := candidate.(string)
		if !ok {
			return "", "", "", fmt.Errorf("%s argument %d must be a string, got %T", name, i+1, candidate)
		}
		stringsByPosition[i] = text
	}
	layout = stringsByPosition[0]
	value = stringsByPosition[len(stringsByPosition)-1]
	if len(stringsByPosition) == 3 {
		timezone = stringsByPosition[1]
	}
	return value, layout, timezone, nil
}

func templateAddTime(values ...any) (string, error) {
	if len(values) != 2 {
		return "", fmt.Errorf("addTime expects an adjustments object and piped time value")
	}
	adjustments, ok := values[0].(map[string]any)
	if !ok {
		return "", fmt.Errorf("addTime adjustments must be an object, got %T", values[0])
	}
	value, ok := values[1].(string)
	if !ok {
		return "", fmt.Errorf("addTime value must be a string, got %T", values[1])
	}
	return AddTime(value, adjustments)
}

func exprParseTime(values ...any) (any, error) {
	value, layout, timezone, err := exprTimeArguments("parseTime", values)
	if err != nil {
		return nil, err
	}
	return ParseTime(value, layout, timezone)
}

func exprFormatTime(values ...any) (any, error) {
	value, layout, timezone, err := exprTimeArguments("formatTime", values)
	if err != nil {
		return nil, err
	}
	return FormatTime(value, layout, timezone)
}

func exprTimeArguments(name string, values []any) (value, layout, timezone string, err error) {
	if len(values) != 2 && len(values) != 3 {
		return "", "", "", fmt.Errorf("%s expects a time value, layout, and optional timezone", name)
	}
	for i, candidate := range values {
		if _, ok := candidate.(string); !ok {
			return "", "", "", fmt.Errorf("%s argument %d must be a string, got %T", name, i+1, candidate)
		}
	}
	value = values[0].(string)
	layout = values[1].(string)
	if len(values) == 3 {
		timezone = values[2].(string)
	}
	return value, layout, timezone, nil
}

func exprAddTime(values ...any) (any, error) {
	if len(values) != 2 {
		return nil, fmt.Errorf("addTime expects a time value and adjustments object")
	}
	value, ok := values[0].(string)
	if !ok {
		return nil, fmt.Errorf("addTime value must be a string, got %T", values[0])
	}
	adjustments, ok := values[1].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("addTime adjustments must be an object, got %T", values[1])
	}
	return AddTime(value, adjustments)
}
