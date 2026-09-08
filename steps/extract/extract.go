// Package extract implements typed text extraction with formats, regular expressions, and marker fields.
package extract

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"math"
	"regexp"
	"slices"
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

	matchOne = "one"
	matchAll = "all"

	markerPrefix = "WUKO_OUTPUT_V1"
)

var identifierPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

type Config struct {
	Text      string                 `yaml:"text,omitempty"`
	From      string                 `yaml:"from,omitempty"`
	Format    string                 `yaml:"format,omitempty"`
	Pattern   string                 `yaml:"pattern,omitempty"`
	Match     string                 `yaml:"match,omitempty"`
	Types     map[string]string      `yaml:"types,omitempty"`
	Fields    map[string]FieldConfig `yaml:"fields,omitempty"`
	Variables map[string]string      `yaml:"variables,omitempty"`
}

// FieldConfig declares one named output extracted from the shared input text.
type FieldConfig struct {
	Marker string `yaml:"marker,omitempty"`
	Regex  string `yaml:"regex,omitempty"`
	Type   string `yaml:"type,omitempty"`
	Match  string `yaml:"match,omitempty"`
}

type capture struct {
	name  string
	kind  string
	index int
}

type compiledField struct {
	name       string
	config     FieldConfig
	regexp     *regexp.Regexp
	valueIndex int
}

type Runner struct {
	config   Config
	regexp   *regexp.Regexp
	captures []capture
	fields   []compiledField
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

	if err := validateVariables(config.Variables); err != nil {
		return nil, err
	}

	_, hasFields := raw["fields"]
	_, hasFormat := raw["format"]
	_, hasPattern := raw["pattern"]
	runner := &Runner{config: config, format: hasFormat}
	if hasFields {
		if len(config.Fields) == 0 {
			return nil, fmt.Errorf("fields must contain at least one field")
		}
		for _, name := range []string{"format", "pattern", "types", "match"} {
			if _, exists := raw[name]; exists {
				return nil, fmt.Errorf("fields cannot be combined with %s", name)
			}
		}
		if err := runner.compileFields(raw); err != nil {
			return nil, err
		}
		return runner, nil
	}

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
	if config.Match == "" {
		runner.config.Match = matchOne
	} else if !templated(config.Match) {
		if err := validateMatch(config.Match); err != nil {
			return nil, err
		}
	}

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
	if len(r.fields) == 0 && r.regexp == nil {
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

	if len(r.fields) > 0 {
		return r.runFields(ctx, text)
	}
	if err := validateMatch(r.config.Match); err != nil {
		return step.Result{}, err
	}

	indices, err := r.findMatches(ctx, text, r.config.Match)
	if err != nil {
		return step.Result{}, err
	}
	if r.config.Match == matchOne {
		if len(indices) != 1 {
			return step.Result{}, fmt.Errorf("extraction found %d matches, want exactly one", len(indices))
		}
		outputs, err := r.convertMatch(text, indices[0])
		if err != nil {
			return step.Result{}, err
		}
		variables := make(map[string]any, len(r.config.Variables))
		for source, target := range r.config.Variables {
			variables[target] = outputs[source]
		}
		return step.Result{Outputs: outputs, Variables: variables}, nil
	}

	matches := make([]any, 0, len(indices))
	// Only captures a variable actually maps need a flattened list; building one per
	// capture doubled the peak memory of a large match: all extraction for nothing.
	values := make(map[string][]any, len(r.config.Variables))
	for source := range r.config.Variables {
		values[source] = make([]any, 0, len(indices))
	}
	for _, item := range indices {
		if err := ctx.Err(); err != nil {
			return step.Result{}, err
		}
		record, err := r.convertMatch(text, item)
		if err != nil {
			return step.Result{}, err
		}
		matches = append(matches, record)
		for source := range values {
			values[source] = append(values[source], record[source])
		}
	}
	variables := make(map[string]any, len(r.config.Variables))
	for source, target := range r.config.Variables {
		variables[target] = values[source]
	}
	return step.Result{Outputs: map[string]any{"matches": matches, "count": len(matches)}, Variables: variables}, nil
}

func (r *Runner) convertMatch(text string, indices []int) (map[string]any, error) {
	outputs := make(map[string]any, len(r.captures))
	for _, item := range r.captures {
		start, end := indices[item.index*2], indices[item.index*2+1]
		if start < 0 || end < 0 {
			return nil, fmt.Errorf("capture %q did not participate in the match", item.name)
		}
		value, err := convert(text[start:end], item.kind)
		if err != nil {
			return nil, fmt.Errorf("converting capture %q to %s: %w", item.name, item.kind, err)
		}
		outputs[item.name] = value
	}
	return outputs, nil
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
		// match: all replaces the per-capture outputs with matches and count, so a
		// capture of either name would be silently shadowed by the wrong value.
		if r.config.Match == matchAll && (item.name == "matches" || item.name == "count") {
			return fmt.Errorf("capture %q collides with the match: all output %q", item.name, item.name)
		}
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

func (r *Runner) compileFields(raw map[string]any) error {
	rawFields, _ := raw["fields"].(map[string]any)
	fields := make([]compiledField, 0, len(r.config.Fields))
	available := make(map[string]struct{}, len(r.config.Fields))
	for _, name := range slices.Sorted(maps.Keys(r.config.Fields)) {
		if !identifierPattern.MatchString(name) {
			return fmt.Errorf("invalid field name %q", name)
		}
		config := r.config.Fields[name]
		rawField, _ := rawFields[name].(map[string]any)
		_, hasMarker := rawField["marker"]
		_, hasRegex := rawField["regex"]
		if !hasMarker && !hasRegex {
			return fmt.Errorf("field %q requires marker or regex", name)
		}
		if hasMarker && strings.TrimSpace(config.Marker) == "" {
			return fmt.Errorf("field %q marker must not be empty", name)
		}
		if hasRegex && strings.TrimSpace(config.Regex) == "" {
			return fmt.Errorf("field %q regex must not be empty", name)
		}
		if config.Type != "" && !hasRegex {
			return fmt.Errorf("field %q type requires regex", name)
		}
		if config.Type == "" {
			config.Type = typeString
		} else if !templated(config.Type) {
			if err := validateType(config.Type); err != nil {
				return fmt.Errorf("field %q type: %w", name, err)
			}
		}
		if config.Match == "" {
			config.Match = matchOne
		} else if !templated(config.Match) {
			if err := validateMatch(config.Match); err != nil {
				return fmt.Errorf("field %q: %w", name, err)
			}
		}

		field := compiledField{name: name, config: config}
		if hasRegex && !templated(config.Regex) {
			compiled, index, err := compileFieldRegex(config.Regex)
			if err != nil {
				return fmt.Errorf("field %q: %w", name, err)
			}
			field.regexp = compiled
			field.valueIndex = index
		}
		fields = append(fields, field)
		available[name] = struct{}{}
	}
	for source := range r.config.Variables {
		if _, ok := available[source]; !ok {
			return fmt.Errorf("variables references unknown field %q", source)
		}
	}
	r.fields = fields
	return nil
}

func compileFieldRegex(pattern string) (*regexp.Regexp, int, error) {
	compiled, err := regexp.Compile(pattern)
	if err != nil {
		return nil, 0, fmt.Errorf("compiling regex: %w", err)
	}
	valueIndex := 0
	for index, name := range compiled.SubexpNames() {
		if index == 0 || name == "" {
			continue
		}
		if name != "value" {
			return nil, 0, fmt.Errorf("regex named capture must be value, found %q", name)
		}
		if valueIndex != 0 {
			return nil, 0, fmt.Errorf("regex must contain exactly one named value capture")
		}
		valueIndex = index
	}
	if valueIndex == 0 {
		return nil, 0, fmt.Errorf("regex must contain exactly one named value capture")
	}
	return compiled, valueIndex, nil
}

func (r *Runner) findMatches(ctx context.Context, text, cardinality string) ([][]int, error) {
	limit := -1
	if cardinality == matchOne {
		limit = 2
	}
	if !r.format {
		matches := r.regexp.FindAllStringSubmatchIndex(text, limit)
		return matches, ctx.Err()
	}
	lines := strings.Split(text, "\n")
	if len(lines) > 1 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	matches := make([][]int, 0)
	offset := 0
	for _, rawLine := range lines {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		line := strings.TrimSuffix(rawLine, "\r")
		if indices := r.regexp.FindStringSubmatchIndex(line); indices != nil {
			match := make([]int, len(indices))
			for i, index := range indices {
				if index >= 0 {
					match[i] = index + offset
				} else {
					match[i] = -1
				}
			}
			matches = append(matches, match)
			if limit > 0 && len(matches) == limit {
				return matches, nil
			}
		}
		offset += len(rawLine) + 1
	}
	return matches, nil
}

func (r *Runner) runFields(ctx context.Context, text string) (step.Result, error) {
	needsMarkers := false
	for _, field := range r.fields {
		if field.config.Marker != "" {
			needsMarkers = true
			break
		}
	}
	var markers map[string][]string
	if needsMarkers {
		var err error
		markers, err = parseMarkers(text)
		if err != nil {
			return step.Result{}, err
		}
	}

	outputs := make(map[string]any, len(r.fields))
	for _, field := range r.fields {
		if err := ctx.Err(); err != nil {
			return step.Result{}, err
		}
		value, err := field.extract(ctx, text, markers)
		if err != nil {
			return step.Result{}, fmt.Errorf("field %q: %w", field.name, err)
		}
		outputs[field.name] = value
	}
	variables := make(map[string]any, len(r.config.Variables))
	for source, target := range r.config.Variables {
		variables[target] = outputs[source]
	}
	return step.Result{Outputs: outputs, Variables: variables}, nil
}

func (field compiledField) extract(ctx context.Context, text string, markers map[string][]string) (any, error) {
	if err := validateMatch(field.config.Match); err != nil {
		return nil, err
	}
	if templated(field.config.Marker) {
		return nil, fmt.Errorf("marker was not resolved before execution")
	}
	if templated(field.config.Type) {
		return nil, fmt.Errorf("type was not resolved before execution")
	}
	if err := validateType(field.config.Type); err != nil {
		return nil, fmt.Errorf("type: %w", err)
	}

	sources := []string{text}
	if field.config.Marker != "" {
		sources = markers[field.config.Marker]
	}
	values := make([]any, 0)
	if field.config.Regex == "" {
		for _, source := range sources {
			values = append(values, source)
			if field.config.Match == matchOne && len(values) == 2 {
				break
			}
		}
	} else {
		if field.regexp == nil {
			return nil, fmt.Errorf("regex was not resolved before execution")
		}
		for _, source := range sources {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			limit := -1
			if field.config.Match == matchOne {
				limit = 2 - len(values)
			}
			for _, indices := range field.regexp.FindAllStringSubmatchIndex(source, limit) {
				start, end := indices[field.valueIndex*2], indices[field.valueIndex*2+1]
				if start < 0 || end < 0 {
					return nil, fmt.Errorf("value capture did not participate in the match")
				}
				value, err := convert(source[start:end], field.config.Type)
				if err != nil {
					return nil, fmt.Errorf("converting value to %s: %w", field.config.Type, err)
				}
				values = append(values, value)
			}
			if field.config.Match == matchOne && len(values) == 2 {
				break
			}
		}
	}
	if field.config.Match == matchAll {
		return values, nil
	}
	if len(values) != 1 {
		return nil, fmt.Errorf("extraction found %d matches, want exactly one", len(values))
	}
	return values[0], nil
}

type markerEvent struct {
	Event string `json:"event"`
	Key   string `json:"key"`
}

func parseMarkers(text string) (map[string][]string, error) {
	markers := make(map[string][]string)
	activeKey := ""
	payloadStart := 0
	for lineStart := 0; lineStart < len(text); {
		lineEnd := strings.IndexByte(text[lineStart:], '\n')
		next := len(text)
		if lineEnd >= 0 {
			lineEnd += lineStart
			next = lineEnd + 1
		} else {
			lineEnd = len(text)
		}
		line := text[lineStart:lineEnd]
		line = strings.TrimSuffix(line, "\r")
		if line == markerPrefix || strings.HasPrefix(line, markerPrefix+" ") {
			event, err := parseMarkerEvent(line)
			if err != nil {
				return nil, err
			}
			switch event.Event {
			case "begin":
				if activeKey != "" {
					return nil, fmt.Errorf("marker %q begins inside marker %q", event.Key, activeKey)
				}
				activeKey = event.Key
				payloadStart = next
			case "end":
				if activeKey == "" {
					return nil, fmt.Errorf("marker %q ends without a matching begin", event.Key)
				}
				if event.Key != activeKey {
					return nil, fmt.Errorf("marker %q ends marker %q", event.Key, activeKey)
				}
				markers[activeKey] = append(markers[activeKey], text[payloadStart:lineStart])
				activeKey = ""
			}
		}
		lineStart = next
	}
	if activeKey != "" {
		return nil, fmt.Errorf("marker %q is not closed", activeKey)
	}
	return markers, nil
}

func parseMarkerEvent(line string) (markerEvent, error) {
	payload, ok := strings.CutPrefix(line, markerPrefix+" ")
	if !ok || strings.TrimSpace(payload) == "" {
		return markerEvent{}, fmt.Errorf("invalid %s marker line", markerPrefix)
	}
	decoder := json.NewDecoder(strings.NewReader(payload))
	decoder.DisallowUnknownFields()
	var event markerEvent
	if err := decoder.Decode(&event); err != nil {
		return markerEvent{}, fmt.Errorf("decoding %s marker: %w", markerPrefix, err)
	}
	if err := ensureJSONEnd(decoder); err != nil {
		return markerEvent{}, fmt.Errorf("decoding %s marker: %w", markerPrefix, err)
	}
	if strings.TrimSpace(event.Key) == "" {
		return markerEvent{}, fmt.Errorf("%s marker key must not be empty", markerPrefix)
	}
	if event.Event != "begin" && event.Event != "end" {
		return markerEvent{}, fmt.Errorf("%s marker event must be begin or end", markerPrefix)
	}
	return event, nil
}

func validateMatch(value string) error {
	if value != matchOne && value != matchAll {
		return fmt.Errorf("match must be one or all")
	}
	return nil
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
