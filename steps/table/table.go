// Package table implements a display-only tabular TUI step.
package table

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"unicode"

	"github.com/charmbracelet/x/ansi"
	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/tui"
)

type Config struct {
	Message string         `yaml:"message"`
	From    string         `yaml:"from"`
	Columns []ColumnConfig `yaml:"columns"`
	// Browse opens the interactive browser in a terminal; false always prints.
	Browse *bool `yaml:"browse,omitempty"`
	// GroupField prints a heading per distinct value, in order of first
	// appearance, and omits the columns showing that field.
	GroupField string `yaml:"group_field,omitempty"`
	// Empty replaces the table when the source list has no rows.
	Empty string `yaml:"empty,omitempty"`
}

type ColumnConfig struct {
	Header string `yaml:"header"`
	Field  string `yaml:"field"`
	Width  *int   `yaml:"width,omitempty"`
}

type Runner struct{ config Config }

func Register(registry *step.Registry) error {
	return registry.RegisterDefinition("tui_table", step.Registration{Builder: New, Config: Config{}, Outputs: step.ClosedOutputs(), References: step.LookupFrom})
}

func New(raw map[string]any) (step.Runner, error) {
	var config Config
	if err := step.DecodeConfig(raw, &config); err != nil {
		return nil, err
	}
	if strings.TrimSpace(config.Message) == "" {
		return nil, fmt.Errorf("message is required")
	}
	if strings.TrimSpace(config.From) == "" {
		return nil, fmt.Errorf("from is required")
	}
	if step.ConfigKnown(raw, "columns") && len(config.Columns) == 0 {
		return nil, fmt.Errorf("at least one column is required")
	}
	for index, column := range config.Columns {
		if strings.TrimSpace(column.Header) == "" {
			return nil, fmt.Errorf("column %d header is required", index+1)
		}
		if strings.TrimSpace(column.Field) == "" {
			return nil, fmt.Errorf("column %d field is required", index+1)
		}
		if column.Width != nil && *column.Width <= 0 {
			return nil, fmt.Errorf("column %d width must be positive", index+1)
		}
	}
	if step.ConfigKnown(raw, "group_field") && config.GroupField != "" {
		if config.browse() {
			return nil, fmt.Errorf("group_field requires browse: false")
		}
		if step.ConfigKnown(raw, "columns", "group_field") && !slices.ContainsFunc(config.Columns, func(column ColumnConfig) bool { return column.Field != config.GroupField }) {
			return nil, fmt.Errorf("group_field %q leaves no columns to show", config.GroupField)
		}
	}
	return &Runner{config: config}, nil
}

func (c Config) browse() bool { return c.Browse == nil || *c.Browse }

func (r *Runner) Run(ctx context.Context, request step.Request) (step.Result, error) {
	source, err := step.Lookup(request, r.config.From)
	if err != nil {
		return step.Result{}, fmt.Errorf("resolving table source: %w", err)
	}
	items, ok := asSlice(source)
	if !ok {
		return step.Result{}, fmt.Errorf("table source %q is not a list", r.config.From)
	}
	if len(items) == 0 && r.config.Empty != "" {
		if _, err := fmt.Fprintf(request.Stdout, "%s\n%s\n", ansi.Strip(r.config.Message), sanitizeCell(r.config.Empty)); err != nil {
			return step.Result{}, fmt.Errorf("writing table: %w", err)
		}
		return step.Result{}, nil
	}
	config, err := r.tableConfig(ctx, items)
	if err != nil {
		return step.Result{}, err
	}
	if !request.Interactive || !r.config.browse() {
		if err := tui.WriteTable(request.Stdout, config, tui.TerminalWidth(request.Stdout)); err != nil {
			return step.Result{}, fmt.Errorf("writing table: %w", err)
		}
		return step.Result{}, nil
	}
	if err := tui.Table(ctx, request.Stdin, request.Stdout, config); err != nil {
		return step.Result{}, fmt.Errorf("showing table: %w", err)
	}
	return step.Result{}, nil
}

func (r *Runner) tableConfig(ctx context.Context, items []any) (tui.TableConfig, error) {
	var shown []ColumnConfig
	for _, column := range r.config.Columns {
		if r.config.GroupField == "" || column.Field != r.config.GroupField {
			shown = append(shown, column)
		}
	}
	columns := make([]tui.TableColumn, len(shown))
	for index, column := range shown {
		columns[index] = tui.TableColumn{Header: column.Header}
		if column.Width != nil {
			columns[index].Width = *column.Width
		}
	}
	rows := make([][]string, len(items))
	var groups []string
	if r.config.GroupField != "" {
		groups = make([]string, len(items))
	}
	for rowIndex, item := range items {
		if err := ctx.Err(); err != nil {
			return tui.TableConfig{}, err
		}
		if _, ok := item.(map[string]any); !ok {
			return tui.TableConfig{}, fmt.Errorf("table source row %d is not an object", rowIndex+1)
		}
		row := make([]string, len(shown))
		for columnIndex, column := range shown {
			value, err := step.LookupValue(item, column.Field)
			if err != nil {
				return tui.TableConfig{}, fmt.Errorf("table source row %d column %q: %w", rowIndex+1, column.Header, err)
			}
			row[columnIndex], err = formatCell(value)
			if err != nil {
				return tui.TableConfig{}, fmt.Errorf("table source row %d column %q: %w", rowIndex+1, column.Header, err)
			}
		}
		rows[rowIndex] = row
		if groups != nil {
			value, err := step.LookupValue(item, r.config.GroupField)
			if err != nil {
				return tui.TableConfig{}, fmt.Errorf("table source row %d group_field: %w", rowIndex+1, err)
			}
			if groups[rowIndex], err = formatCell(value); err != nil {
				return tui.TableConfig{}, fmt.Errorf("table source row %d group_field: %w", rowIndex+1, err)
			}
			if groups[rowIndex] == "" {
				groups[rowIndex] = "(none)"
			}
		}
	}
	if groups != nil {
		rows, groups = groupRows(rows, groups)
	}
	return tui.TableConfig{Message: r.config.Message, Columns: columns, Rows: rows, Groups: groups}, nil
}

// groupRows orders rows by the first appearance of their group, keeping the
// source order within each group.
func groupRows(rows [][]string, groups []string) ([][]string, []string) {
	var order []string
	members := map[string][]int{}
	for index, group := range groups {
		if _, ok := members[group]; !ok {
			order = append(order, group)
		}
		members[group] = append(members[group], index)
	}
	orderedRows := make([][]string, 0, len(rows))
	orderedGroups := make([]string, 0, len(groups))
	for _, group := range order {
		for _, index := range members[group] {
			orderedRows = append(orderedRows, rows[index])
			orderedGroups = append(orderedGroups, group)
		}
	}
	return orderedRows, orderedGroups
}

func formatCell(value any) (string, error) {
	if value == nil {
		return "", nil
	}
	var rendered string
	switch value.(type) {
	case string, bool, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64, json.Number:
		rendered = fmt.Sprint(value)
	default:
		encoded, err := json.Marshal(value)
		if err != nil {
			return "", fmt.Errorf("encoding cell as JSON: %w", err)
		}
		rendered = string(encoded)
	}
	return sanitizeCell(rendered), nil
}

func sanitizeCell(value string) string {
	value = ansi.Strip(value)
	value = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, value)
	return strings.TrimSpace(value)
}

func asSlice(value any) ([]any, bool) {
	if values, ok := value.([]any); ok {
		return values, true
	}
	reflected := reflect.ValueOf(value)
	if !reflected.IsValid() || (reflected.Kind() != reflect.Slice && reflected.Kind() != reflect.Array) {
		return nil, false
	}
	values := make([]any, reflected.Len())
	for index := range reflected.Len() {
		values[index] = reflected.Index(index).Interface()
	}
	return values, true
}
