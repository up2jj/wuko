// Package decode converts serialized text into typed workflow values.
package decode

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	toml "github.com/pelletier/go-toml/v2"
	"github.com/up2jj/wuko/step"
	"gopkg.in/yaml.v3"
)

const defaultMaxBytes = "1MiB"

var yamlLinePattern = regexp.MustCompile(`(?:^|\s)line ([0-9]+)(?::|\s|$)`)

type Config struct {
	Format    string `yaml:"format"`
	From      string `yaml:"from,omitempty"`
	Path      string `yaml:"path,omitempty"`
	MaxBytes  string `yaml:"max_bytes,omitempty"`
	Trim      bool   `yaml:"trim,omitempty"`
	OmitEmpty bool   `yaml:"omit_empty,omitempty"`
}

type Runner struct {
	config   Config
	present  map[string]bool
	maxBytes int64
}

func Register(registry *step.Registry) error { return registry.Register("decode", New) }

func New(raw map[string]any) (step.Runner, error) {
	config := Config{MaxBytes: defaultMaxBytes}
	if err := step.DecodeConfig(raw, &config); err != nil {
		return nil, err
	}
	present := make(map[string]bool, len(raw))
	for field := range raw {
		present[field] = true
	}
	runner := &Runner{config: config, present: present}
	if err := runner.validate(false); err != nil {
		return nil, err
	}
	return runner, nil
}

func (r *Runner) Run(ctx context.Context, request step.Request) (step.Result, error) {
	if err := r.validate(true); err != nil {
		return step.Result{}, err
	}
	if err := ctx.Err(); err != nil {
		return step.Result{}, err
	}

	text, source, err := r.readInput(ctx, request)
	if err != nil {
		return step.Result{}, err
	}
	value, err := r.decode(text)
	if err != nil {
		return step.Result{}, fmt.Errorf("decoding %s input from %s: %w", r.config.Format, source, err)
	}
	if err := ctx.Err(); err != nil {
		return step.Result{}, err
	}
	return step.Result{Outputs: map[string]any{"value": value}}, nil
}

func (r *Runner) validate(resolved bool) error {
	if r.present["from"] == r.present["path"] {
		return fmt.Errorf("exactly one of from or path is required")
	}
	if r.present["from"] {
		if strings.TrimSpace(r.config.From) == "" {
			return fmt.Errorf("from must not be empty")
		}
		if !templated(r.config.From) {
			parts := strings.Split(r.config.From, ".")
			if len(parts) < 2 || parts[1] == "" || (parts[0] != "vars" && parts[0] != "steps") {
				return fmt.Errorf("from must be a dotted path rooted at vars or steps")
			}
		}
	}
	if r.present["path"] && strings.TrimSpace(r.config.Path) == "" {
		return fmt.Errorf("path must not be empty")
	}
	if strings.TrimSpace(r.config.Format) == "" {
		return fmt.Errorf("format is required")
	}

	for field, value := range map[string]string{
		"format": r.config.Format, "from": r.config.From, "path": r.config.Path, "max_bytes": r.config.MaxBytes,
	} {
		if resolved && templated(value) {
			return fmt.Errorf("%s contains an unresolved template", field)
		}
	}
	if !resolved && templated(r.config.Format) {
		return r.validateMaxBytes(false)
	}
	switch r.config.Format {
	case "json", "yaml", "toml":
		if r.present["trim"] || r.present["omit_empty"] {
			return fmt.Errorf("trim and omit_empty are only supported with lines")
		}
	case "lines":
	default:
		return fmt.Errorf("format must be json, yaml, toml, or lines")
	}
	return r.validateMaxBytes(resolved)
}

func (r *Runner) validateMaxBytes(resolved bool) error {
	if strings.TrimSpace(r.config.MaxBytes) == "" {
		return fmt.Errorf("max_bytes must be a positive size")
	}
	if !resolved && templated(r.config.MaxBytes) {
		return nil
	}
	maxBytes, err := parseSize(r.config.MaxBytes)
	if err != nil {
		return fmt.Errorf("max_bytes: %w", err)
	}
	if maxBytes <= 0 {
		return fmt.Errorf("max_bytes must be a positive size")
	}
	r.maxBytes = maxBytes
	return nil
}

func (r *Runner) readInput(ctx context.Context, request step.Request) (string, string, error) {
	if r.present["from"] {
		value, err := step.Lookup(request, r.config.From)
		if err != nil {
			return "", "", fmt.Errorf("resolving input: %w", err)
		}
		text, ok := value.(string)
		if !ok {
			return "", "", fmt.Errorf("from %q resolved to %T, want string", r.config.From, value)
		}
		if int64(len(text)) > r.maxBytes {
			return "", "", fmt.Errorf("input from %q exceeds max_bytes %s", r.config.From, r.config.MaxBytes)
		}
		return text, fmt.Sprintf("%q", r.config.From), nil
	}

	path, err := resolvePath(request.RunDir, r.config.Path)
	if err != nil {
		return "", "", err
	}
	file, err := os.Open(path)
	if err != nil {
		return "", "", fmt.Errorf("opening decode file %s: %w", path, err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", "", fmt.Errorf("inspecting decode file %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return "", "", fmt.Errorf("decode file %s must be a regular file", path)
	}
	if info.Size() > r.maxBytes {
		return "", "", fmt.Errorf("decode file %s exceeds max_bytes %s", path, r.config.MaxBytes)
	}
	text, err := readBounded(ctx, file, r.maxBytes, info.Size())
	if err != nil {
		if errors.Is(err, errInputTooLarge) {
			return "", "", fmt.Errorf("decode file %s exceeds max_bytes %s", path, r.config.MaxBytes)
		}
		return "", "", fmt.Errorf("reading decode file %s: %w", path, err)
	}
	return text, path, nil
}

func (r *Runner) decode(text string) (any, error) {
	switch r.config.Format {
	case "json":
		return decodeJSON(text)
	case "yaml":
		return decodeYAML(text)
	case "toml":
		return decodeTOML(text)
	case "lines":
		return decodeLines(text, r.config.Trim, r.config.OmitEmpty), nil
	default:
		panic("validated decode format")
	}
}

func decodeJSON(text string) (any, error) {
	decoder := json.NewDecoder(strings.NewReader(text))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, jsonLocationError(text, err)
	}
	extraOffset := nextNonSpace(text, decoder.InputOffset())
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			line, column := lineColumn(text, extraOffset)
			return nil, fmt.Errorf("line %d, column %d: multiple JSON values are not supported", line, column)
		}
		return nil, jsonLocationError(text, err)
	}
	return value, nil
}

func decodeYAML(text string) (any, error) {
	decoder := yaml.NewDecoder(strings.NewReader(text))
	var document yaml.Node
	if err := decoder.Decode(&document); err != nil {
		return nil, yamlLocationError(text, err)
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			line, column := 1, 1
			if len(extra.Content) > 0 {
				line, column = extra.Content[0].Line, extra.Content[0].Column
			}
			return nil, fmt.Errorf("line %d, column %d: multiple YAML documents are not supported", line, column)
		}
		return nil, yamlLocationError(text, err)
	}
	var value any
	if err := document.Decode(&value); err != nil {
		return nil, yamlLocationError(text, err)
	}
	return normalize(value)
}

func decodeTOML(text string) (any, error) {
	value := make(map[string]any)
	if err := toml.Unmarshal([]byte(text), &value); err != nil {
		var decodeErr *toml.DecodeError
		if errors.As(err, &decodeErr) {
			line, column := decodeErr.Position()
			return nil, fmt.Errorf("line %d, column %d: %w", line, column, err)
		}
		return nil, err
	}
	return normalize(value)
}

func decodeLines(input string, trim, omitEmpty bool) []any {
	if input == "" {
		return []any{}
	}
	lines := strings.Split(input, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	result := make([]any, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSuffix(line, "\r")
		if trim {
			line = strings.TrimSpace(line)
		}
		if omitEmpty && line == "" {
			continue
		}
		result = append(result, line)
	}
	return result
}

// normalize converts a decoded YAML or TOML value into the JSON-compatible shape the
// rest of a workflow sees: objects, arrays, strings, bools, nil, and json.Number.
//
// It walks the tree directly for the types YAML and TOML actually produce, and hands
// anything else to roundTrip, which is what this function used to do for the whole
// document. Routing the leftovers keeps the exact behaviour -- including error text --
// for values the walk deliberately does not model: timestamps and other json.Marshaler
// types, maps with non-string keys, NaN and infinity, and strings holding invalid UTF-8,
// which JSON encoding rewrites with the replacement rune.
func normalize(value any) (any, error) {
	switch typed := value.(type) {
	case nil, bool, json.Number:
		return value, nil
	case string:
		if utf8.ValidString(typed) {
			return value, nil
		}
	case int:
		return json.Number(strconv.Itoa(typed)), nil
	case int64:
		return json.Number(strconv.FormatInt(typed, 10)), nil
	case uint64:
		return json.Number(strconv.FormatUint(typed, 10)), nil
	case float64:
		if text, ok := jsonFloatText(typed); ok {
			return json.Number(text), nil
		}
	case map[string]any:
		return normalizeMap(typed)
	case []any:
		return normalizeSlice(typed)
	}
	return roundTrip(value)
}

func normalizeMap(source map[string]any) (any, error) {
	if source == nil {
		return nil, nil // A nil map encodes as JSON null, not as an empty object.
	}
	result := make(map[string]any, len(source))
	for key, item := range source {
		if !utf8.ValidString(key) {
			return roundTrip(source)
		}
		converted, err := normalize(item)
		if err != nil {
			return nil, err
		}
		result[key] = converted
	}
	return result, nil
}

func normalizeSlice(source []any) (any, error) {
	if source == nil {
		return nil, nil // A nil slice encodes as JSON null, not as an empty array.
	}
	result := make([]any, len(source))
	for i, item := range source {
		converted, err := normalize(item)
		if err != nil {
			return nil, err
		}
		result[i] = converted
	}
	return result, nil
}

func roundTrip(value any) (any, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("value is not JSON-compatible: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var normalized any
	if err := decoder.Decode(&normalized); err != nil {
		return nil, fmt.Errorf("normalizing value: %w", err)
	}
	return normalized, nil
}

// jsonFloatText renders f the way encoding/json does, so the walk in normalize yields
// the same json.Number the marshal round-trip did. It reports false for NaN and
// infinity, which JSON cannot represent, leaving those to roundTrip and its error.
// TestJSONFloatTextMatchesEncodingJSON pins this to the standard library.
func jsonFloatText(number float64) (string, bool) {
	if math.IsNaN(number) || math.IsInf(number, 0) {
		return "", false
	}
	// JSON switches to exponent form outside the range where %f stays compact.
	format := byte('f')
	if absolute := math.Abs(number); absolute != 0 && (absolute < 1e-6 || absolute >= 1e21) {
		format = 'e'
	}
	text := strconv.AppendFloat(make([]byte, 0, 32), number, format, -1, 64)
	if format == 'e' {
		// Collapse a padded exponent, turning 1e-09 into 1e-9.
		if size := len(text); size >= 4 && text[size-4] == 'e' && text[size-3] == '-' && text[size-2] == '0' {
			text[size-2] = text[size-1]
			text = text[:size-1]
		}
	}
	return string(text), true
}

var errInputTooLarge = errors.New("input exceeds limit")

// readBounded reads at most maximum bytes. size is the reader's expected length,
// used only to size the buffer up front; it stays a hint because a file can grow
// between the caller's stat and this read, so maximum remains the real limit.
func readBounded(ctx context.Context, reader io.Reader, maximum, size int64) (string, error) {
	var builder strings.Builder
	builder.Grow(int(readCapacity(maximum, size)))
	buffer := make([]byte, 32*1024)
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		count, readErr := reader.Read(buffer)
		if count > 0 {
			if int64(count) > maximum-int64(builder.Len()) {
				return "", errInputTooLarge
			}
			builder.Write(buffer[:count])
		}
		if errors.Is(readErr, io.EOF) {
			return builder.String(), nil
		}
		if readErr != nil {
			return "", readErr
		}
	}
}

func readCapacity(maximum, size int64) int64 {
	capacity := size
	if capacity <= 0 {
		capacity = 32 * 1024
	}
	if capacity > maximum {
		capacity = maximum
	}
	if capacity > math.MaxInt {
		capacity = math.MaxInt
	}
	return capacity
}

func jsonLocationError(text string, err error) error {
	offset := int64(len(text) + 1)
	var syntaxErr *json.SyntaxError
	if errors.As(err, &syntaxErr) {
		offset = syntaxErr.Offset
	}
	var typeErr *json.UnmarshalTypeError
	if errors.As(err, &typeErr) {
		offset = typeErr.Offset
	}
	line, column := lineColumn(text, offset)
	return fmt.Errorf("line %d, column %d: %w", line, column, err)
}

func yamlLocationError(text string, err error) error {
	line := 1
	if match := yamlLinePattern.FindStringSubmatch(err.Error()); len(match) == 2 {
		line, _ = strconv.Atoi(match[1])
	}
	column := lineEndColumn(text, line)
	return fmt.Errorf("line %d, column %d: %w", line, column, err)
}

func lineColumn(text string, offset int64) (int, int) {
	if offset < 1 {
		offset = 1
	}
	if offset > int64(len(text)+1) {
		offset = int64(len(text) + 1)
	}
	prefix := text[:offset-1]
	line := strings.Count(prefix, "\n") + 1
	start := strings.LastIndexByte(prefix, '\n') + 1
	column := utf8.RuneCountInString(prefix[start:]) + 1
	return line, column
}

func lineEndColumn(text string, line int) int {
	if line < 1 {
		return 1
	}
	lines := strings.Split(text, "\n")
	if line > len(lines) {
		line = len(lines)
	}
	if line == 0 {
		return 1
	}
	return utf8.RuneCountInString(strings.TrimSuffix(lines[line-1], "\r")) + 1
}

func nextNonSpace(text string, offset int64) int64 {
	for offset < int64(len(text)) {
		switch text[offset] {
		case ' ', '\t', '\r', '\n':
			offset++
		default:
			return offset + 1
		}
	}
	return int64(len(text) + 1)
}

func resolvePath(runDir, path string) (string, error) {
	if filepath.IsAbs(path) {
		return filepath.Clean(path), nil
	}
	if runDir == "" {
		var err error
		runDir, err = os.Getwd()
		if err != nil {
			return "", fmt.Errorf("finding run directory: %w", err)
		}
	}
	resolved, err := filepath.Abs(filepath.Join(runDir, path))
	if err != nil {
		return "", fmt.Errorf("resolving decode path %s: %w", path, err)
	}
	return filepath.Clean(resolved), nil
}

func parseSize(value string) (int64, error) {
	trimmed := strings.TrimSpace(value)
	for _, unit := range []struct {
		suffix     string
		multiplier int64
	}{
		{"TiB", 1 << 40}, {"GiB", 1 << 30}, {"MiB", 1 << 20}, {"KiB", 1 << 10}, {"B", 1},
	} {
		if !strings.HasSuffix(trimmed, unit.suffix) {
			continue
		}
		number := strings.TrimSpace(strings.TrimSuffix(trimmed, unit.suffix))
		parsed, err := strconv.ParseInt(number, 10, 64)
		if err != nil || parsed < 0 || parsed > math.MaxInt64/unit.multiplier {
			break
		}
		return parsed * unit.multiplier, nil
	}
	return 0, fmt.Errorf("must be a non-negative size such as 64KiB")
}

func templated(value string) bool { return strings.Contains(value, "{{") }

var _ step.Runner = (*Runner)(nil)
