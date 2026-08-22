// Package extract implements typed text extraction with friendly formats or named regular-expression captures.
package extract

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/up2jj/wuko/step"
)

const (
	typeString  = "string"
	typeInteger = "integer"
	typeNumber  = "number"
	typeBoolean = "boolean"
	typeJSON    = "json"
)

var identifierPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

type Config struct {
	Text      string            `yaml:"text,omitempty"`
	From      string            `yaml:"from,omitempty"`
	Format    string            `yaml:"format,omitempty"`
	Pattern   string            `yaml:"pattern,omitempty"`
	Types     map[string]string `yaml:"types,omitempty"`
	Variables map[string]string `yaml:"variables,omitempty"`
}

type capture struct {
	name  string
	kind  string
	index int
}

type Runner struct {
	config   Config
	regexp   *regexp.Regexp
	captures []capture
	format   bool
}

func Register(registry *step.Registry) error { return registry.Register("extract", New) }

func New(raw map[string]any) (step.Runner, error) {
	var config Config
	if err := step.DecodeConfig(raw, &config); err != nil {
		return nil, err
	}
	_, hasText := raw["text"]
	_, hasFrom := raw["from"]
	if hasText == hasFrom {
		return nil, fmt.Errorf("exactly one of text or from is required")
	}
	if hasFrom {
		if strings.TrimSpace(config.From) == "" {
			return nil, fmt.Errorf("from must not be empty")
		}
		if !templated(config.From) {
			parts := strings.Split(config.From, ".")
			if len(parts) < 2 || parts[1] == "" || (parts[0] != "vars" && parts[0] != "steps") {
				return nil, fmt.Errorf("from must be a dotted path rooted at vars or steps")
			}
		}
	}

	_, hasFormat := raw["format"]
	_, hasPattern := raw["pattern"]
	if hasFormat == hasPattern {
		return nil, fmt.Errorf("exactly one of format or pattern is required")
	}
	if hasFormat && config.Types != nil {
		return nil, fmt.Errorf("types is only supported with pattern")
	}
	for name, kind := range config.Types {
		if !identifierPattern.MatchString(name) {
			return nil, fmt.Errorf("invalid capture name %q in types", name)
		}
		if err := validateType(kind); err != nil {
			return nil, fmt.Errorf("types.%s: %w", name, err)
		}
	}
	if err := validateVariables(config.Variables); err != nil {
		return nil, err
	}

	runner := &Runner{config: config, format: hasFormat}
	matcher := config.Pattern
	if hasFormat {
		matcher = config.Format
	}
	if templated(matcher) {
		return runner, nil
	}
	if err := runner.compile(); err != nil {
		return nil, err
	}
	return runner, nil
}

func (r *Runner) Run(ctx context.Context, request step.Request) (step.Result, error) {
	if err := ctx.Err(); err != nil {
		return step.Result{}, err
	}
	if r.regexp == nil {
		return step.Result{}, fmt.Errorf("extract matcher was not resolved before execution")
	}
	text := r.config.Text
	if r.config.From != "" {
		value, err := step.Lookup(request, r.config.From)
		if err != nil {
			return step.Result{}, fmt.Errorf("resolving input: %w", err)
		}
		var ok bool
		text, ok = value.(string)
		if !ok {
			return step.Result{}, fmt.Errorf("from %q resolved to %T, want string", r.config.From, value)
		}
	}

	indices, count, err := r.findMatch(ctx, text)
	if err != nil {
		return step.Result{}, err
	}
	if count != 1 {
		return step.Result{}, fmt.Errorf("extraction found %d matches, want exactly one", count)
	}
	if err := ctx.Err(); err != nil {
		return step.Result{}, err
	}

	outputs := make(map[string]any, len(r.captures))
	for _, item := range r.captures {
		start, end := indices[item.index*2], indices[item.index*2+1]
		if start < 0 || end < 0 {
			return step.Result{}, fmt.Errorf("capture %q did not participate in the match", item.name)
		}
		value, err := convert(text[start:end], item.kind)
		if err != nil {
			return step.Result{}, fmt.Errorf("converting capture %q to %s: %w", item.name, item.kind, err)
		}
		outputs[item.name] = value
	}
	variables := make(map[string]any, len(r.config.Variables))
	for source, target := range r.config.Variables {
		variables[target] = outputs[source]
	}
	return step.Result{Outputs: outputs, Variables: variables}, nil
}

func (r *Runner) compile() error {
	var (
		compiled *regexp.Regexp
		captures []capture
		err      error
	)
	if r.format {
		compiled, captures, err = compileFormat(r.config.Format)
	} else {
		compiled, captures, err = compilePattern(r.config.Pattern, r.config.Types)
	}
	if err != nil {
		return err
	}
	available := make(map[string]struct{}, len(captures))
	for _, item := range captures {
		available[item.name] = struct{}{}
	}
	for source := range r.config.Variables {
		if _, ok := available[source]; !ok {
			return fmt.Errorf("variables references unknown capture %q", source)
		}
	}
	r.regexp = compiled
	r.captures = captures
	return nil
}

func (r *Runner) findMatch(ctx context.Context, text string) ([]int, int, error) {
	if !r.format {
		matches := r.regexp.FindAllStringSubmatchIndex(text, 2)
		return singleMatch(matches), len(matches), ctx.Err()
	}
	lines := strings.Split(text, "\n")
	if len(lines) > 1 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	var match []int
	count := 0
	offset := 0
	for _, rawLine := range lines {
		if err := ctx.Err(); err != nil {
			return nil, 0, err
		}
		line := strings.TrimSuffix(rawLine, "\r")
		if indices := r.regexp.FindStringSubmatchIndex(line); indices != nil {
			count++
			if count == 1 {
				match = make([]int, len(indices))
				for i, index := range indices {
					if index >= 0 {
						match[i] = index + offset
					} else {
						match[i] = -1
					}
				}
			}
			if count == 2 {
				return match, count, nil
			}
		}
		offset += len(rawLine) + 1
	}
	return match, count, nil
}

func singleMatch(matches [][]int) []int {
	if len(matches) == 0 {
		return nil
	}
	return matches[0]
}

func compilePattern(pattern string, types map[string]string) (*regexp.Regexp, []capture, error) {
	compiled, err := regexp.Compile(pattern)
	if err != nil {
		return nil, nil, fmt.Errorf("compiling pattern: %w", err)
	}
	seen := make(map[string]struct{})
	captures := make([]capture, 0)
	for index, name := range compiled.SubexpNames() {
		if index == 0 || name == "" {
			continue
		}
		if !identifierPattern.MatchString(name) {
			return nil, nil, fmt.Errorf("invalid capture name %q", name)
		}
		if _, exists := seen[name]; exists {
			return nil, nil, fmt.Errorf("duplicate capture name %q", name)
		}
		seen[name] = struct{}{}
		kind := types[name]
		if kind == "" {
			kind = typeString
		}
		captures = append(captures, capture{name: name, kind: kind, index: index})
	}
	if len(captures) == 0 {
		return nil, nil, fmt.Errorf("pattern must contain at least one named capture")
	}
	for name := range types {
		if _, exists := seen[name]; !exists {
			return nil, nil, fmt.Errorf("types references unknown capture %q", name)
		}
	}
	return compiled, captures, nil
}

func compileFormat(format string) (*regexp.Regexp, []capture, error) {
	if strings.ContainsAny(format, "\r\n") {
		return nil, nil, fmt.Errorf("format must be a single line")
	}
	var pattern strings.Builder
	pattern.WriteString("^")
	captures := make([]capture, 0)
	seen := make(map[string]struct{})
	for i := 0; i < len(format); {
		switch format[i] {
		case '\\':
			if i+1 >= len(format) || (format[i+1] != '{' && format[i+1] != '}' && format[i+1] != '\\') {
				return nil, nil, fmt.Errorf("format has unsupported escape at byte %d", i)
			}
			pattern.WriteString(regexp.QuoteMeta(format[i+1 : i+2]))
			i += 2
		case '{':
			end := strings.IndexByte(format[i+1:], '}')
			if end < 0 {
				return nil, nil, fmt.Errorf("format placeholder at byte %d is not closed", i)
			}
			end += i + 1
			name, kind, err := parsePlaceholder(format[i+1 : end])
			if err != nil {
				return nil, nil, fmt.Errorf("format placeholder at byte %d: %w", i, err)
			}
			if _, exists := seen[name]; exists {
				return nil, nil, fmt.Errorf("duplicate capture name %q", name)
			}
			seen[name] = struct{}{}
			pattern.WriteString("(?P<")
			pattern.WriteString(name)
			pattern.WriteString(">")
			pattern.WriteString(formatTypePattern(kind))
			pattern.WriteString(")")
			captures = append(captures, capture{name: name, kind: kind, index: len(captures) + 1})
			i = end + 1
		case '}':
			return nil, nil, fmt.Errorf("format has unescaped closing brace at byte %d", i)
		case ' ', '\t':
			for i < len(format) && (format[i] == ' ' || format[i] == '\t') {
				i++
			}
			pattern.WriteString(`[ \t]+`)
		default:
			start := i
			for i < len(format) && format[i] != '\\' && format[i] != '{' && format[i] != '}' && format[i] != ' ' && format[i] != '\t' {
				i++
			}
			pattern.WriteString(regexp.QuoteMeta(format[start:i]))
		}
	}
	if len(captures) == 0 {
		return nil, nil, fmt.Errorf("format must contain at least one placeholder")
	}
	pattern.WriteString("$")
	compiled, err := regexp.Compile(pattern.String())
	if err != nil {
		return nil, nil, fmt.Errorf("compiling format: %w", err)
	}
	return compiled, captures, nil
}

func parsePlaceholder(value string) (string, string, error) {
	if strings.Count(value, ":") > 1 {
		return "", "", fmt.Errorf("placeholder must use {name} or {name:type}")
	}
	name, kind, typed := strings.Cut(value, ":")
	if !identifierPattern.MatchString(name) {
		return "", "", fmt.Errorf("invalid capture name %q", name)
	}
	if !typed {
		kind = typeString
	}
	if err := validateType(kind); err != nil {
		return "", "", err
	}
	return name, kind, nil
}

func formatTypePattern(kind string) string {
	switch kind {
	case typeInteger:
		return `[+-]?[0-9]+`
	case typeNumber:
		return `[+-]?(?:[0-9]+(?:\.[0-9]*)?|\.[0-9]+)(?:[eE][+-]?[0-9]+)?`
	case typeBoolean:
		return `(?:true|false)`
	default:
		return `.*?`
	}
}

func validateType(kind string) error {
	switch kind {
	case typeString, typeInteger, typeNumber, typeBoolean, typeJSON:
		return nil
	default:
		return fmt.Errorf("type must be string, integer, number, boolean, or json")
	}
}

func validateVariables(variables map[string]string) error {
	targets := make(map[string]struct{}, len(variables))
	for source, target := range variables {
		if !identifierPattern.MatchString(source) {
			return fmt.Errorf("invalid capture name %q in variables", source)
		}
		if !identifierPattern.MatchString(target) {
			return fmt.Errorf("invalid variable name %q", target)
		}
		if _, exists := targets[target]; exists {
			return fmt.Errorf("duplicate variable target %q", target)
		}
		targets[target] = struct{}{}
	}
	return nil
}

func convert(value, kind string) (any, error) {
	switch kind {
	case typeString:
		return value, nil
	case typeInteger:
		result, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return nil, err
		}
		return result, nil
	case typeNumber:
		result, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return nil, err
		}
		if math.IsNaN(result) || math.IsInf(result, 0) {
			return nil, fmt.Errorf("number must be finite")
		}
		return result, nil
	case typeBoolean:
		if value == "true" {
			return true, nil
		}
		if value == "false" {
			return false, nil
		}
		return nil, fmt.Errorf("boolean must be true or false")
	case typeJSON:
		decoder := json.NewDecoder(strings.NewReader(value))
		decoder.UseNumber()
		var result any
		if err := decoder.Decode(&result); err != nil {
			return nil, err
		}
		if err := ensureJSONEnd(decoder); err != nil {
			return nil, err
		}
		return normalizeJSON(result), nil
	default:
		panic("validated capture type")
	}
}

func ensureJSONEnd(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("multiple JSON values")
		}
		return err
	}
	return nil
}

func normalizeJSON(value any) any {
	switch typed := value.(type) {
	case json.Number:
		if integer, err := typed.Int64(); err == nil {
			return integer
		}
		if unsigned, err := strconv.ParseUint(string(typed), 10, 64); err == nil {
			return unsigned
		}
		number, err := typed.Float64()
		if err != nil {
			return typed
		}
		return number
	case []any:
		result := make([]any, len(typed))
		for i, item := range typed {
			result[i] = normalizeJSON(item)
		}
		return result
	case map[string]any:
		result := make(map[string]any, len(typed))
		for key, item := range typed {
			result[key] = normalizeJSON(item)
		}
		return result
	default:
		return value
	}
}

func templated(value string) bool { return strings.Contains(value, "{{") }
