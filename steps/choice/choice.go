package choice

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/tui"
)

type Config struct {
	Variable   string         `yaml:"variable"`
	Message    string         `yaml:"message"`
	Multiple   bool           `yaml:"multiple,omitempty"`
	Required   *bool          `yaml:"required,omitempty"`
	Choices    []ChoiceConfig `yaml:"choices,omitempty"`
	From       string         `yaml:"from,omitempty"`
	LabelField string         `yaml:"label_field,omitempty"`
	ValueField string         `yaml:"value_field,omitempty"`
}

type ChoiceConfig struct {
	Label string `yaml:"label"`
	Value any    `yaml:"value"`
}

type Runner struct{ config Config }

func Register(registry *step.Registry) error { return registry.Register("choice", New) }

func New(raw map[string]any) (step.Runner, error) {
	var config Config
	if err := step.DecodeConfig(raw, &config); err != nil {
		return nil, err
	}
	if config.Variable == "" || strings.TrimSpace(config.Message) == "" {
		return nil, fmt.Errorf("variable and message are required")
	}
	if (len(config.Choices) == 0) == (config.From == "") {
		return nil, fmt.Errorf("exactly one of choices or from is required")
	}
	if config.LabelField == "" {
		config.LabelField = "label"
	}
	if config.ValueField == "" {
		config.ValueField = "value"
	}
	return &Runner{config: config}, nil
}

func (r *Runner) Run(ctx context.Context, request step.Request) (step.Result, error) {
	options, err := r.options(request)
	if err != nil {
		return step.Result{}, err
	}
	if len(options) == 0 {
		return step.Result{}, fmt.Errorf("choice set is empty")
	}
	if err := ensureUnique(options); err != nil {
		return step.Result{}, err
	}

	if supplied, exists := request.Vars[r.config.Variable]; exists {
		return r.preSupplied(supplied, options)
	}
	if !request.Interactive {
		return step.Result{}, fmt.Errorf("variable %q is required when stdin is non-interactive; supply it with --var", r.config.Variable)
	}
	indexes, err := tui.Choose(ctx, request.Stdin, request.Stdout, r.config.Message, options, r.config.Multiple, r.required())
	if err != nil {
		return step.Result{}, fmt.Errorf("choosing: %w", err)
	}
	return r.selected(indexes, options), nil
}

func (r *Runner) options(request step.Request) ([]tui.Option, error) {
	if len(r.config.Choices) > 0 {
		options := make([]tui.Option, len(r.config.Choices))
		for i, choice := range r.config.Choices {
			if choice.Label == "" {
				return nil, fmt.Errorf("choice %d has an empty label", i+1)
			}
			if !scalar(choice.Value) {
				return nil, fmt.Errorf("choice %d value must be a scalar", i+1)
			}
			options[i] = tui.Option{Label: choice.Label, Value: choice.Value}
		}
		return options, nil
	}

	value, err := step.Lookup(request, r.config.From)
	if err != nil {
		return nil, fmt.Errorf("resolving choices: %w", err)
	}
	items, ok := asSlice(value)
	if !ok {
		return nil, fmt.Errorf("choice source %q is not a list", r.config.From)
	}
	options := make([]tui.Option, 0, len(items))
	for i, item := range items {
		if scalar(item) {
			options = append(options, tui.Option{Label: fmt.Sprint(item), Value: item})
			continue
		}
		label, err := step.LookupValue(item, r.config.LabelField)
		if err != nil {
			return nil, fmt.Errorf("choice source item %d label: %w", i+1, err)
		}
		value, err := step.LookupValue(item, r.config.ValueField)
		if err != nil {
			return nil, fmt.Errorf("choice source item %d value: %w", i+1, err)
		}
		if !scalar(value) {
			return nil, fmt.Errorf("choice source item %d value must be a scalar", i+1)
		}
		options = append(options, tui.Option{Label: fmt.Sprint(label), Value: value})
	}
	return options, nil
}

func (r *Runner) preSupplied(value any, options []tui.Option) (step.Result, error) {
	if !r.config.Multiple {
		index := optionIndex(options, value)
		if index < 0 {
			return step.Result{}, fmt.Errorf("pre-supplied variable %q is not an available choice", r.config.Variable)
		}
		return r.selected([]int{index}, options), nil
	}
	values, ok := asSlice(value)
	if !ok {
		return step.Result{}, fmt.Errorf("pre-supplied variable %q must be a list", r.config.Variable)
	}
	if r.required() && len(values) == 0 {
		return step.Result{}, fmt.Errorf("pre-supplied variable %q must contain at least one value", r.config.Variable)
	}
	indexes := make([]int, 0, len(values))
	seen := make(map[int]struct{})
	for _, selected := range values {
		index := optionIndex(options, selected)
		if index < 0 {
			return step.Result{}, fmt.Errorf("pre-supplied variable %q contains an unavailable choice %v", r.config.Variable, selected)
		}
		if _, duplicate := seen[index]; duplicate {
			continue
		}
		seen[index] = struct{}{}
		indexes = append(indexes, index)
	}
	return r.selected(indexes, options), nil
}

func (r *Runner) selected(indexes []int, options []tui.Option) step.Result {
	if !r.config.Multiple {
		option := options[indexes[0]]
		return step.Result{Outputs: map[string]any{"value": option.Value, "label": option.Label}, Variables: map[string]any{r.config.Variable: option.Value}}
	}
	values := make([]any, 0, len(indexes))
	labels := make([]any, 0, len(indexes))
	for _, index := range indexes {
		values = append(values, options[index].Value)
		labels = append(labels, options[index].Label)
	}
	return step.Result{Outputs: map[string]any{"values": values, "labels": labels}, Variables: map[string]any{r.config.Variable: values}}
}

func (r *Runner) required() bool { return r.config.Required == nil || *r.config.Required }

func ensureUnique(options []tui.Option) error {
	seen := make(map[string]struct{}, len(options))
	for _, option := range options {
		encoded, err := json.Marshal(option.Value)
		if err != nil {
			return fmt.Errorf("encoding choice value: %w", err)
		}
		key := string(encoded)
		if _, ok := seen[key]; ok {
			return fmt.Errorf("duplicate choice value %s", key)
		}
		seen[key] = struct{}{}
	}
	return nil
}

func optionIndex(options []tui.Option, value any) int {
	encoded, err := json.Marshal(value)
	if err != nil {
		return -1
	}
	for i, option := range options {
		candidate, err := json.Marshal(option.Value)
		if err == nil && string(candidate) == string(encoded) {
			return i
		}
	}
	return -1
}

func scalar(value any) bool {
	switch value.(type) {
	case nil, string, bool, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64, json.Number:
		return true
	default:
		return false
	}
}

func asSlice(value any) ([]any, bool) {
	if values, ok := value.([]any); ok {
		return values, true
	}
	rv := reflect.ValueOf(value)
	if !rv.IsValid() || (rv.Kind() != reflect.Slice && rv.Kind() != reflect.Array) {
		return nil, false
	}
	values := make([]any, rv.Len())
	for i := range rv.Len() {
		values[i] = rv.Index(i).Interface()
	}
	return values, true
}
