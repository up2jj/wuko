// Package markdownedit implements source-preserving semantic Markdown edits.
package markdownedit

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/expr-lang/expr"
	"github.com/expr-lang/expr/vm"
	wukoexpr "github.com/up2jj/wuko/expression"
	"github.com/up2jj/wuko/step"
)

const (
	defaultMaxBytes  = "1MiB"
	maxSelectorDepth = 8
)

type Source struct {
	File string `yaml:"file,omitempty"`
	Var  string `yaml:"var,omitempty"`
	Expr string `yaml:"expr,omitempty"`
}

type Config struct {
	From     Source `yaml:"from"`
	MaxBytes string `yaml:"max_bytes,omitempty"`
	Edits    []Edit `yaml:"edits"`
}

type Edit struct {
	Operation string   `yaml:"operation"`
	Select    Selector `yaml:"select"`
	Field     string   `yaml:"field,omitempty"`
	Value     any      `yaml:"value,omitempty"`
	Expr      string   `yaml:"expr,omitempty"`
	Position  string   `yaml:"position,omitempty"`
	Result    string   `yaml:"result,omitempty"`
	Missing   string   `yaml:"missing,omitempty"`

	hasValue bool
	program  *vm.Program
}

type Selector struct {
	Kind        string            `yaml:"kind"`
	Text        string            `yaml:"text,omitempty"`
	TextRegex   string            `yaml:"text_regex,omitempty"`
	Level       int               `yaml:"level,omitempty"`
	ID          string            `yaml:"id,omitempty"`
	Attributes  map[string]string `yaml:"attributes,omitempty"`
	Language    string            `yaml:"language,omitempty"`
	Destination string            `yaml:"destination,omitempty"`
	Source      string            `yaml:"source,omitempty"`
	Title       string            `yaml:"title,omitempty"`
	Task        *bool             `yaml:"task,omitempty"`
	Checked     *bool             `yaml:"checked,omitempty"`
	Ordered     *bool             `yaml:"ordered,omitempty"`
	Headers     []string          `yaml:"headers,omitempty"`
	Column      any               `yaml:"column,omitempty"`
	Header      *bool             `yaml:"header,omitempty"`
	Occurrence  int               `yaml:"occurrence,omitempty"`
	Parent      *Selector         `yaml:"parent,omitempty"`
	Ancestor    *Selector         `yaml:"ancestor,omitempty"`
	Contains    *Selector         `yaml:"contains,omitempty"`
	Before      *Selector         `yaml:"before,omitempty"`
	After       *Selector         `yaml:"after,omitempty"`

	pattern *regexp.Regexp
}

type Runner struct {
	config     Config
	sourceExpr *vm.Program
	maxBytes   int64
}

func (*Runner) ExecutorAware()      {}
func (*Runner) ExecutorFileSystem() {}

func Register(registry *step.Registry) error { return registry.Register("markdown_edit", New) }

func New(raw map[string]any) (step.Runner, error) {
	config := Config{MaxBytes: defaultMaxBytes}
	if err := step.DecodeConfig(raw, &config); err != nil {
		return nil, err
	}
	if countNonEmpty(config.From.File, config.From.Var, config.From.Expr) != 1 {
		return nil, fmt.Errorf("from must contain exactly one of file, var, or expr")
	}
	if len(config.Edits) == 0 {
		return nil, fmt.Errorf("edits must contain at least one edit")
	}

	runner := &Runner{config: config}
	if !templated(config.MaxBytes) {
		maximum, err := parseSize(config.MaxBytes)
		if err != nil || maximum <= 0 {
			return nil, fmt.Errorf("max_bytes must be a positive byte size")
		}
		runner.maxBytes = maximum
	}
	if config.From.Expr != "" && !templated(config.From.Expr) {
		program, err := wukoexpr.Compile(config.From.Expr,
			expr.Env(step.ExpressionEnvironmentShape(nil)), expr.AllowUndefinedVariables())
		if err != nil {
			return nil, fmt.Errorf("compiling from.expr: %w", err)
		}
		runner.sourceExpr = program
	}

	rawEdits, ok := raw["edits"].([]any)
	if !ok || len(rawEdits) != len(runner.config.Edits) {
		return nil, fmt.Errorf("edits must be a list")
	}
	for index := range runner.config.Edits {
		rawEdit, ok := rawEdits[index].(map[string]any)
		if !ok {
			return nil, fmt.Errorf("edits[%d] must be an object", index)
		}
		if err := runner.prepareEdit(index, rawEdit); err != nil {
			return nil, err
		}
	}
	return runner, nil
}

func (r *Runner) prepareEdit(index int, raw map[string]any) error {
	edit := &r.config.Edits[index]
	if edit.Result == "" {
		edit.Result = "one"
	}
	if edit.Missing == "" {
		edit.Missing = "error"
	}
	if !oneOf(edit.Operation, "set", "replace", "insert", "delete", "prepend", "append") {
		return fmt.Errorf("edits[%d].operation must be set, replace, insert, delete, prepend, or append", index)
	}
	if err := edit.Select.prepare(0); err != nil {
		return fmt.Errorf("edits[%d].select: %w", index, err)
	}
	if !templated(edit.Result) && !oneOf(edit.Result, "one", "all") {
		return fmt.Errorf("edits[%d].result must be one or all", index)
	}
	if !templated(edit.Missing) && !oneOf(edit.Missing, "error", "ignore") {
		return fmt.Errorf("edits[%d].missing must be error or ignore", index)
	}

	_, hasValue := raw["value"]
	_, hasExpr := raw["expr"]
	edit.hasValue = hasValue
	needsReplacement := edit.Operation != "delete"
	if needsReplacement && hasValue == hasExpr {
		return fmt.Errorf("edits[%d] operation %s requires exactly one of value or expr", index, edit.Operation)
	}
	if !needsReplacement && (hasValue || hasExpr) {
		return fmt.Errorf("edits[%d] value and expr are not allowed with operation delete", index)
	}
	if edit.Operation == "insert" {
		if !templated(edit.Position) && !oneOf(edit.Position, "before", "after") {
			return fmt.Errorf("edits[%d].position must be before or after with operation insert", index)
		}
	} else if edit.Position != "" {
		return fmt.Errorf("edits[%d].position is only allowed with operation insert", index)
	}
	if edit.Operation == "set" {
		if strings.TrimSpace(edit.Field) == "" {
			return fmt.Errorf("edits[%d].field is required with operation set", index)
		}
		if !templated(edit.Select.Kind) && !templated(edit.Field) {
			if err := validateSetField(edit.Select.Kind, edit.Field); err != nil {
				return fmt.Errorf("edits[%d]: %w", index, err)
			}
		}
	} else if edit.Field != "" {
		return fmt.Errorf("edits[%d].field is only allowed with operation set", index)
	}
	if !templated(edit.Select.Kind) {
		if edit.Select.Kind == "table_cell" && edit.Operation != "set" {
			return fmt.Errorf("edits[%d]: table_cell supports only operation set", index)
		}
		if edit.Select.Kind == "table_row" && !oneOf(edit.Operation, "replace", "insert", "delete") {
			return fmt.Errorf("edits[%d]: table_row supports replace, insert, or delete", index)
		}
	}
	if hasValue {
		if err := validateJSON(edit.Value); err != nil {
			return fmt.Errorf("edits[%d].value is not JSON-compatible: %w", index, err)
		}
		if err := validateLiteralValue(edit); err != nil {
			return fmt.Errorf("edits[%d]: %w", index, err)
		}
	}
	if hasExpr {
		if strings.TrimSpace(edit.Expr) == "" {
			return fmt.Errorf("edits[%d].expr must not be empty", index)
		}
		if !templated(edit.Expr) {
			program, err := wukoexpr.Compile(edit.Expr, expr.Env(step.ExpressionEnvironmentShape(map[string]any{
				"current": map[string]any{}, "path": "", "index": 0,
			})), expr.AllowUndefinedVariables())
			if err != nil {
				return fmt.Errorf("compiling edits[%d].expr: %w", index, err)
			}
			edit.program = program
		}
	}
	return nil
}

func (s *Selector) prepare(depth int) error {
	if depth >= maxSelectorDepth {
		return fmt.Errorf("structural selector nesting exceeds %d levels", maxSelectorDepth)
	}
	if !templated(s.Kind) && !oneOf(s.Kind,
		"document", "heading", "paragraph", "blockquote", "code_block", "link", "image",
		"list", "list_item", "table", "table_row", "table_cell") {
		return fmt.Errorf("unsupported kind %q", s.Kind)
	}
	if strings.TrimSpace(s.Kind) == "" {
		return fmt.Errorf("kind is required")
	}
	if s.Occurrence < 0 {
		return fmt.Errorf("occurrence must be positive")
	}
	if s.TextRegex != "" && !templated(s.TextRegex) {
		pattern, err := regexp.Compile(s.TextRegex)
		if err != nil {
			return fmt.Errorf("text_regex: %w", err)
		}
		s.pattern = pattern
	}
	if s.Level < 0 || s.Level > 6 {
		return fmt.Errorf("level must be between 1 and 6")
	}
	if s.Column != nil {
		switch value := s.Column.(type) {
		case int:
			if value < 1 {
				return fmt.Errorf("column number must be positive")
			}
		case string:
			if strings.TrimSpace(value) == "" {
				return fmt.Errorf("column name must not be empty")
			}
		default:
			return fmt.Errorf("column must be a positive number or header name")
		}
	}
	if !templated(s.Kind) {
		if err := s.validateFields(); err != nil {
			return err
		}
	}
	for name, nested := range map[string]*Selector{
		"parent": s.Parent, "ancestor": s.Ancestor, "contains": s.Contains,
		"before": s.Before, "after": s.After,
	} {
		if nested == nil {
			continue
		}
		if err := nested.prepare(depth + 1); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	return nil
}

func (s *Selector) validateFields() error {
	if (s.Level != 0 || s.ID != "" || len(s.Attributes) > 0) && s.Kind != "heading" {
		return fmt.Errorf("level, id, and attributes are only supported for heading selectors")
	}
	if s.Language != "" && s.Kind != "code_block" {
		return fmt.Errorf("language is only supported for code_block selectors")
	}
	if s.Destination != "" && s.Kind != "link" {
		return fmt.Errorf("destination is only supported for link selectors")
	}
	if s.Source != "" && s.Kind != "image" {
		return fmt.Errorf("source is only supported for image selectors")
	}
	if s.Title != "" && s.Kind != "link" && s.Kind != "image" {
		return fmt.Errorf("title is only supported for link and image selectors")
	}
	if (s.Task != nil || s.Checked != nil) && s.Kind != "list_item" {
		return fmt.Errorf("task and checked are only supported for list_item selectors")
	}
	if s.Ordered != nil && s.Kind != "list" {
		return fmt.Errorf("ordered is only supported for list selectors")
	}
	if len(s.Headers) > 0 && !oneOf(s.Kind, "table", "table_row", "table_cell") {
		return fmt.Errorf("headers is only supported for table selectors")
	}
	if (s.Column != nil || s.Header != nil) && s.Kind != "table_cell" {
		return fmt.Errorf("column and header are only supported for table_cell selectors")
	}
	return nil
}

func validateSetField(kind, field string) error {
	valid := false
	switch kind {
	case "heading":
		valid = field == "text" || field == "level" || strings.HasPrefix(field, "attribute.")
	case "code_block":
		valid = oneOf(field, "content", "info", "language")
	case "link":
		valid = oneOf(field, "text", "destination", "title")
	case "image":
		valid = oneOf(field, "alt", "source", "title")
	case "list_item":
		valid = field == "checked"
	case "table_cell":
		valid = field == "content"
	}
	if !valid {
		return fmt.Errorf("field %q is not supported for %s", field, kind)
	}
	return nil
}

func validateLiteralValue(edit *Edit) error {
	if edit.Operation == "delete" {
		return nil
	}
	if edit.Select.Kind == "table" && oneOf(edit.Operation, "prepend", "append") ||
		edit.Select.Kind == "table_row" && oneOf(edit.Operation, "replace", "insert") {
		_, err := tableRowValue(edit.Value)
		return err
	}
	if edit.Operation != "set" {
		_, err := stringValue(edit.Value, edit.Operation+" value")
		return err
	}
	switch {
	case edit.Select.Kind == "heading" && edit.Field == "level":
		level, ok := integerValue(edit.Value)
		if !ok || level < 1 || level > 6 {
			return fmt.Errorf("heading level must be an integer between 1 and 6")
		}
	case edit.Select.Kind == "list_item" && edit.Field == "checked":
		if _, ok := edit.Value.(bool); !ok {
			return fmt.Errorf("checked value must be boolean, got %T", edit.Value)
		}
	case edit.Select.Kind == "heading" && strings.HasPrefix(edit.Field, "attribute.") && edit.Value == nil:
		return nil
	default:
		_, err := stringValue(edit.Value, edit.Field)
		return err
	}
	return nil
}

func countNonEmpty(values ...string) int {
	count := 0
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			count++
		}
	}
	return count
}

func oneOf(value string, choices ...string) bool {
	for _, choice := range choices {
		if value == choice {
			return true
		}
	}
	return false
}

func templated(value string) bool { return strings.Contains(value, "{{") }

func parseSize(value string) (int64, error) {
	trimmed := strings.TrimSpace(value)
	for _, unit := range []struct {
		suffix string
		factor int64
	}{{"GiB", 1 << 30}, {"MiB", 1 << 20}, {"KiB", 1 << 10}, {"B", 1}} {
		if !strings.HasSuffix(trimmed, unit.suffix) {
			continue
		}
		number := strings.TrimSpace(strings.TrimSuffix(trimmed, unit.suffix))
		parsed, err := strconv.ParseInt(number, 10, 64)
		if err != nil || parsed <= 0 || parsed > math.MaxInt64/unit.factor {
			return 0, fmt.Errorf("invalid size")
		}
		return parsed * unit.factor, nil
	}
	return 0, fmt.Errorf("size must use B, KiB, MiB, or GiB")
}

func validateJSON(value any) error {
	if number, ok := value.(float64); ok && (math.IsNaN(number) || math.IsInf(number, 0)) {
		return fmt.Errorf("non-finite number")
	}
	_, err := json.Marshal(value)
	return err
}
