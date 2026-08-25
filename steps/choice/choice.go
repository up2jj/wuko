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
	Variable         string         `yaml:"variable"`
	Message          string         `yaml:"message"`
	Multiple         bool           `yaml:"multiple,omitempty"`
	SelectAll        bool           `yaml:"select_all,omitempty"`
	Required         *bool          `yaml:"required,omitempty"`
	MinSelected      *int           `yaml:"min_selected,omitempty"`
	MaxSelected      *int           `yaml:"max_selected,omitempty"`
	Choices          []ChoiceConfig `yaml:"choices,omitempty"`
	From             string         `yaml:"from,omitempty"`
	LabelField       string         `yaml:"label_field,omitempty"`
	ValueField       string         `yaml:"value_field,omitempty"`
	DescriptionField string         `yaml:"description_field,omitempty"`
	DisabledField    string         `yaml:"disabled_field,omitempty"`
	ReasonField      string         `yaml:"reason_field,omitempty"`
	DefaultField     string         `yaml:"default_field,omitempty"`
}

type ChoiceConfig struct {
	Label       string `yaml:"label"`
	Description string `yaml:"description,omitempty"`
	Value       any    `yaml:"value"`
	Disabled    bool   `yaml:"disabled,omitempty"`
	Reason      string `yaml:"reason,omitempty"`
	Default     bool   `yaml:"default,omitempty"`
}

type Runner struct{ config Config }

func Register(registry *step.Registry) error { return registry.Register("tui_choice", New) }

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
	if err := validateBounds(config); err != nil {
		return nil, err
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
	if err := ensureUnique(options); err != nil {
		return step.Result{}, err
	}
	if err := r.validateOptions(options); err != nil {
		return step.Result{}, err
	}

	if supplied, exists := request.Vars[r.config.Variable]; exists {
		return r.preSupplied(supplied, options)
	}
	if !request.Interactive {
		if r.minimum() == 0 {
			return r.selected(nil, options), nil
		}
		return step.Result{}, fmt.Errorf("variable %q is required when stdin is non-interactive; supply it with --var", r.config.Variable)
	}
	indexes, err := tui.Choose(ctx, request.Stdin, request.Stdout, tui.ChoicePickerConfig{
		Message: r.config.Message, Options: options, Multiple: r.config.Multiple, Required: r.required(),
		SelectAll:   r.config.SelectAll,
		MinSelected: r.config.MinSelected, MaxSelected: r.config.MaxSelected,
	})
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
			if choice.Disabled && strings.TrimSpace(choice.Reason) == "" {
				return nil, fmt.Errorf("choice %d is disabled without a reason", i+1)
			}
			options[i] = tui.Option{
				Label: choice.Label, Description: choice.Description, Value: choice.Value,
				Disabled: choice.Disabled, DisabledReason: choice.Reason, Default: choice.Default,
			}
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
		description := ""
		if r.config.DescriptionField != "" {
			resolved, err := step.LookupValue(item, r.config.DescriptionField)
			if err != nil {
				return nil, fmt.Errorf("choice source item %d description: %w", i+1, err)
			}
			description = fmt.Sprint(resolved)
		}
		disabled, err := boolField(item, r.config.DisabledField)
		if err != nil {
			return nil, fmt.Errorf("choice source item %d disabled: %w", i+1, err)
		}
		defaultChoice, err := boolField(item, r.config.DefaultField)
		if err != nil {
			return nil, fmt.Errorf("choice source item %d default: %w", i+1, err)
		}
		reason := ""
		if disabled {
			if r.config.ReasonField == "" {
				return nil, fmt.Errorf("choice source item %d is disabled without a reason field", i+1)
			}
			resolved, err := step.LookupValue(item, r.config.ReasonField)
			if err != nil {
				return nil, fmt.Errorf("choice source item %d reason: %w", i+1, err)
			}
			var ok bool
			reason, ok = resolved.(string)
			if !ok || strings.TrimSpace(reason) == "" {
				return nil, fmt.Errorf("choice source item %d reason must be a non-empty string", i+1)
			}
		}
		options = append(options, tui.Option{
			Label: fmt.Sprint(label), Description: description, Value: value,
			Disabled: disabled, DisabledReason: reason, Default: defaultChoice,
		})
	}
	return options, nil
}

func (r *Runner) preSupplied(value any, options []tui.Option) (step.Result, error) {
	if !r.config.Multiple {
		index := optionIndex(options, value)
		if index < 0 {
			if !r.required() && value == nil {
				return r.selected(nil, options), nil
			}
			return step.Result{}, fmt.Errorf("pre-supplied variable %q is not an available choice", r.config.Variable)
		}
		if options[index].Disabled {
			return step.Result{}, fmt.Errorf("pre-supplied variable %q is a disabled choice: %s", r.config.Variable, options[index].DisabledReason)
		}
		return r.selected([]int{index}, options), nil
	}
	values, ok := asSlice(value)
	if !ok {
		return step.Result{}, fmt.Errorf("pre-supplied variable %q must be a list", r.config.Variable)
	}
	indexes := make([]int, 0, len(values))
	seen := make(map[int]struct{})
	for _, selected := range values {
		index := optionIndex(options, selected)
		if index < 0 {
			return step.Result{}, fmt.Errorf("pre-supplied variable %q contains an unavailable choice %v", r.config.Variable, selected)
		}
		if options[index].Disabled {
			return step.Result{}, fmt.Errorf("pre-supplied variable %q contains disabled choice %v: %s", r.config.Variable, selected, options[index].DisabledReason)
		}
		if _, duplicate := seen[index]; duplicate {
			continue
		}
		seen[index] = struct{}{}
		indexes = append(indexes, index)
	}
	if len(indexes) < r.minimum() {
		return step.Result{}, fmt.Errorf("pre-supplied variable %q must contain at least %d values", r.config.Variable, r.minimum())
	}
	if r.config.MaxSelected != nil && len(indexes) > *r.config.MaxSelected {
		return step.Result{}, fmt.Errorf("pre-supplied variable %q must contain at most %d values", r.config.Variable, *r.config.MaxSelected)
	}
	return r.selected(indexes, options), nil
}

func (r *Runner) selected(indexes []int, options []tui.Option) step.Result {
	if !r.config.Multiple {
		if len(indexes) == 0 {
			return step.Result{
				Outputs:   map[string]any{"value": nil, "label": "", "selected": false},
				Variables: map[string]any{r.config.Variable: nil},
			}
		}
		option := options[indexes[0]]
		return step.Result{
			Outputs:   map[string]any{"value": option.Value, "label": option.Label, "selected": true},
			Variables: map[string]any{r.config.Variable: option.Value},
		}
	}
	values := make([]any, 0, len(indexes))
	labels := make([]any, 0, len(indexes))
	for _, index := range indexes {
		values = append(values, options[index].Value)
		labels = append(labels, options[index].Label)
	}
	return step.Result{
		Outputs:   map[string]any{"values": values, "labels": labels, "count": len(values)},
		Variables: map[string]any{r.config.Variable: values},
	}
}

func (r *Runner) required() bool { return r.config.Required == nil || *r.config.Required }

func (r *Runner) minimum() int {
	if !r.config.Multiple {
		if r.required() {
			return 1
		}
		return 0
	}
	if r.config.MinSelected != nil || r.config.MaxSelected != nil {
		if r.config.MinSelected != nil {
			return *r.config.MinSelected
		}
		return 0
	}
	if r.required() {
		return 1
	}
	return 0
}

func (r *Runner) validateOptions(options []tui.Option) error {
	enabled := 0
	defaults := 0
	for index, option := range options {
		if option.Disabled {
			if option.Default {
				return fmt.Errorf("choice %d cannot be both disabled and default", index+1)
			}
		} else {
			enabled++
		}
		if option.Default {
			defaults++
		}
	}
	if !r.config.Multiple && defaults > 1 {
		return fmt.Errorf("single choice mode allows at most one default")
	}
	if r.minimum() > enabled {
		return fmt.Errorf("minimum selected %d exceeds %d enabled choices", r.minimum(), enabled)
	}
	if r.config.SelectAll && r.config.MaxSelected != nil && enabled > *r.config.MaxSelected {
		return fmt.Errorf("select all would select %d choices, exceeding maximum selected %d", enabled, *r.config.MaxSelected)
	}
	if r.config.MaxSelected != nil && defaults > *r.config.MaxSelected {
		return fmt.Errorf("%d default choices exceed maximum selected %d", defaults, *r.config.MaxSelected)
	}
	return nil
}

func validateBounds(config Config) error {
	if config.SelectAll && !config.Multiple {
		return fmt.Errorf("select_all requires multiple: true")
	}
	if (config.MinSelected != nil || config.MaxSelected != nil) && !config.Multiple {
		return fmt.Errorf("min_selected and max_selected require multiple: true")
	}
	if config.MinSelected != nil && *config.MinSelected < 0 {
		return fmt.Errorf("min_selected cannot be negative")
	}
	if config.MaxSelected != nil && *config.MaxSelected < 0 {
		return fmt.Errorf("max_selected cannot be negative")
	}
	if config.MinSelected != nil && config.MaxSelected != nil && *config.MinSelected > *config.MaxSelected {
		return fmt.Errorf("min_selected cannot exceed max_selected")
	}
	return nil
}

func boolField(item any, path string) (bool, error) {
	if path == "" {
		return false, nil
	}
	value, err := step.LookupValue(item, path)
	if err != nil {
		return false, err
	}
	result, ok := value.(bool)
	if !ok {
		return false, fmt.Errorf("field %q must be a boolean", path)
	}
	return result, nil
}

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
