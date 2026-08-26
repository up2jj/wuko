// Package table implements a display-only tabular TUI step.
package table

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
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
}

type ColumnConfig struct {
	Header string `yaml:"header"`
	Field  string `yaml:"field"`
	Width  *int   `yaml:"width,omitempty"`
}

type Runner struct{ config Config }

func Register(registry *step.Registry) error { return registry.Register("tui_table", New) }

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
	if len(config.Columns) == 0 {
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
	return &Runner{config: config}, nil
}

func (r *Runner) Run(ctx context.Context, request step.Request) (step.Result, error) {
	source, err := step.Lookup(request, r.config.From)
	if err != nil {
		return step.Result{}, fmt.Errorf("resolving table source: %w", err)
	}
	items, ok := asSlice(source)
	if !ok {
		return step.Result{}, fmt.Errorf("table source %q is not a list", r.config.From)
	}
	config, err := r.tableConfig(ctx, items)
	if err != nil {
		return step.Result{}, err
	}
	if !request.Interactive {
		if err := tui.WriteTable(request.Stdout, config, 80); err != nil {
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
	columns := make([]tui.TableColumn, len(r.config.Columns))
	for index, column := range r.config.Columns {
		columns[index] = tui.TableColumn{Header: column.Header}
		if column.Width != nil {
			columns[index].Width = *column.Width
		}
	}
	rows := make([][]string, len(items))
	for rowIndex, item := range items {
		if err := ctx.Err(); err != nil {
			return tui.TableConfig{}, err
		}
		if _, ok := item.(map[string]any); !ok {
			return tui.TableConfig{}, fmt.Errorf("table source row %d is not an object", rowIndex+1)
		}
		row := make([]string, len(r.config.Columns))
		for columnIndex, column := range r.config.Columns {
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
	}
	return tui.TableConfig{Message: r.config.Message, Columns: columns, Rows: rows}, nil
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
