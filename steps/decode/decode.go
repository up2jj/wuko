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

	data, source, err := r.readInput(ctx, request)
	if err != nil {
		return step.Result{}, err
	}
	value, err := r.decode(data)
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

func (r *Runner) readInput(ctx context.Context, request step.Request) ([]byte, string, error) {
	if r.present["from"] {
		value, err := step.Lookup(request, r.config.From)
		if err != nil {
			return nil, "", fmt.Errorf("resolving input: %w", err)
		}
		text, ok := value.(string)
		if !ok {
			return nil, "", fmt.Errorf("from %q resolved to %T, want string", r.config.From, value)
		}
		if int64(len(text)) > r.maxBytes {
			return nil, "", fmt.Errorf("input from %q exceeds max_bytes %s", r.config.From, r.config.MaxBytes)
		}
		return []byte(text), fmt.Sprintf("%q", r.config.From), nil
	}

	path, err := resolvePath(request.RunDir, r.config.Path)
	if err != nil {
		return nil, "", err
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, "", fmt.Errorf("opening decode file %s: %w", path, err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, "", fmt.Errorf("inspecting decode file %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return nil, "", fmt.Errorf("decode file %s must be a regular file", path)
	}
	if info.Size() > r.maxBytes {
		return nil, "", fmt.Errorf("decode file %s exceeds max_bytes %s", path, r.config.MaxBytes)
	}
	data, err := readBounded(ctx, file, r.maxBytes)
	if err != nil {
		if errors.Is(err, errInputTooLarge) {
			return nil, "", fmt.Errorf("decode file %s exceeds max_bytes %s", path, r.config.MaxBytes)
		}
		return nil, "", fmt.Errorf("reading decode file %s: %w", path, err)
	}
	return data, path, nil
}

func (r *Runner) decode(data []byte) (any, error) {
	switch r.config.Format {
	case "json":
		return decodeJSON(data)
	case "yaml":
		return decodeYAML(data)
	case "toml":
		return decodeTOML(data)
	case "lines":
		return decodeLines(string(data), r.config.Trim, r.config.OmitEmpty), nil
	default:
		panic("validated decode format")
	}
}

func decodeJSON(data []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, jsonLocationError(data, err)
	}
	extraOffset := nextNonSpace(data, decoder.InputOffset())
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			line, column := lineColumn(data, extraOffset)
			return nil, fmt.Errorf("line %d, column %d: multiple JSON values are not supported", line, column)
		}
		return nil, jsonLocationError(data, err)
	}
	return value, nil
}

func decodeYAML(data []byte) (any, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	var document yaml.Node
	if err := decoder.Decode(&document); err != nil {
		return nil, yamlLocationError(data, err)
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
		return nil, yamlLocationError(data, err)
	}
	var value any
	if err := document.Decode(&value); err != nil {
		return nil, yamlLocationError(data, err)
	}
	return normalize(value)
}

func decodeTOML(data []byte) (any, error) {
	value := make(map[string]any)
	if err := toml.Unmarshal(data, &value); err != nil {
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

func normalize(value any) (any, error) {
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

var errInputTooLarge = errors.New("input exceeds limit")

func readBounded(ctx context.Context, reader io.Reader, maximum int64) ([]byte, error) {
	capacity := int64(32 * 1024)
	if maximum < capacity {
		capacity = maximum
	}
	data := make([]byte, 0, int(capacity))
	buffer := make([]byte, 32*1024)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		count, readErr := reader.Read(buffer)
		if count > 0 {
			if int64(count) > maximum-int64(len(data)) {
				return nil, errInputTooLarge
			}
			data = append(data, buffer[:count]...)
		}
		if errors.Is(readErr, io.EOF) {
			return data, nil
		}
		if readErr != nil {
			return nil, readErr
		}
	}
}

func jsonLocationError(data []byte, err error) error {
	offset := int64(len(data) + 1)
	var syntaxErr *json.SyntaxError
	if errors.As(err, &syntaxErr) {
		offset = syntaxErr.Offset
	}
	var typeErr *json.UnmarshalTypeError
	if errors.As(err, &typeErr) {
		offset = typeErr.Offset
	}
	line, column := lineColumn(data, offset)
	return fmt.Errorf("line %d, column %d: %w", line, column, err)
}

func yamlLocationError(data []byte, err error) error {
	line := 1
	if match := yamlLinePattern.FindStringSubmatch(err.Error()); len(match) == 2 {
		line, _ = strconv.Atoi(match[1])
	}
	column := lineEndColumn(data, line)
	return fmt.Errorf("line %d, column %d: %w", line, column, err)
}

func lineColumn(data []byte, offset int64) (int, int) {
	if offset < 1 {
		offset = 1
	}
	if offset > int64(len(data)+1) {
		offset = int64(len(data) + 1)
	}
	prefix := data[:offset-1]
	line := bytes.Count(prefix, []byte{'\n'}) + 1
	start := bytes.LastIndexByte(prefix, '\n') + 1
	column := utf8.RuneCount(prefix[start:]) + 1
	return line, column
}

func lineEndColumn(data []byte, line int) int {
	if line < 1 {
		return 1
	}
	lines := bytes.Split(data, []byte{'\n'})
	if line > len(lines) {
		line = len(lines)
	}
	if line == 0 {
		return 1
	}
	return utf8.RuneCount(bytes.TrimSuffix(lines[line-1], []byte{'\r'})) + 1
}

func nextNonSpace(data []byte, offset int64) int64 {
	for offset < int64(len(data)) {
		switch data[offset] {
		case ' ', '\t', '\r', '\n':
			offset++
		default:
			return offset + 1
		}
	}
	return int64(len(data) + 1)
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
