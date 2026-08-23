// Package form owns workflow browser-form declarations and typed submissions.
package form

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"net/url"
	"reflect"
	"regexp"
	"strconv"
	"strings"

	"github.com/up2jj/wuko/workflow"
	"gopkg.in/yaml.v3"
)

const (
	TypeString  = "string"
	TypeBoolean = "boolean"
	TypeNumber  = "number"
	TypeArray   = "array"
	TypeObject  = "object"
)

// Definition is the cohesive declaration of one browser form.
type Definition struct {
	Title       string       `yaml:"title"`
	Description string       `yaml:"description,omitempty"`
	Load        *Load        `yaml:"load,omitempty"`
	Fields      []Field      `yaml:"fields"`
	Result      ResultConfig `yaml:"result,omitempty"`
}

// Load declares a one-time preparatory workflow whose outputs become form data.
type Load struct {
	Steps   []workflow.Step   `yaml:"steps"`
	Finally []workflow.Step   `yaml:"finally,omitempty"`
	Outputs map[string]string `yaml:"outputs"`
}

// Field binds one browser control to a workflow variable.
type Field struct {
	Variable         string   `yaml:"variable"`
	Label            string   `yaml:"label"`
	Description      string   `yaml:"description,omitempty"`
	Type             string   `yaml:"type,omitempty"`
	Required         bool     `yaml:"required,omitempty"`
	Secret           bool     `yaml:"secret,omitempty"`
	Choices          []Choice `yaml:"choices,omitempty"`
	From             string   `yaml:"from,omitempty"`
	LabelField       string   `yaml:"label_field,omitempty"`
	ValueField       string   `yaml:"value_field,omitempty"`
	DescriptionField string   `yaml:"description_field,omitempty"`
	DisabledField    string   `yaml:"disabled_field,omitempty"`
	ReasonField      string   `yaml:"reason_field,omitempty"`
	MinLength        *int     `yaml:"min_length,omitempty"`
	MaxLength        *int     `yaml:"max_length,omitempty"`
	Pattern          string   `yaml:"pattern,omitempty"`
	Message          string   `yaml:"validation_message,omitempty"`
	pattern          *regexp.Regexp
}

// Choice is one static typed choice.
type Choice struct {
	Label       string `yaml:"label"`
	Description string `yaml:"description,omitempty"`
	Value       any    `yaml:"value"`
	Disabled    bool   `yaml:"disabled,omitempty"`
	Reason      string `yaml:"reason,omitempty"`
}

// ResultConfig optionally customizes terminal browser pages.
type ResultConfig struct {
	Success ResultView `yaml:"success,omitempty"`
	Failure ResultView `yaml:"failure,omitempty"`
}

// ResultView is a safe html/template fragment plus its heading.
type ResultView struct {
	Title    string `yaml:"title,omitempty"`
	Template string `yaml:"template,omitempty"`
}

// ResolvedField is a field with its immutable choices and initial value.
type ResolvedField struct {
	Field
	Value   any
	Options []Choice
}

// Decode strictly decodes and validates an opaque workflow form declaration.
func Decode(node *yaml.Node, vars map[string]any) (*Definition, error) {
	if node == nil || node.Kind == 0 || node.Tag == "!!null" {
		return nil, fmt.Errorf("workflow does not declare a form")
	}
	data, err := yaml.Marshal(node)
	if err != nil {
		return nil, fmt.Errorf("encoding form declaration: %w", err)
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	var definition Definition
	if err := decoder.Decode(&definition); err != nil {
		return nil, fmt.Errorf("decoding form: %w", err)
	}
	if err := definition.validate(vars); err != nil {
		return nil, err
	}
	return &definition, nil
}

func (definition *Definition) validate(vars map[string]any) error {
	if strings.TrimSpace(definition.Title) == "" {
		return fmt.Errorf("form title is required")
	}
	if len(definition.Fields) == 0 {
		return fmt.Errorf("form must contain at least one field")
	}
	for name, view := range map[string]ResultView{"success": definition.Result.Success, "failure": definition.Result.Failure} {
		if strings.TrimSpace(view.Template) == "" {
			continue
		}
		if _, err := template.New(name).Option("missingkey=error").Parse(view.Template); err != nil {
			return fmt.Errorf("form result %s template: %w", name, err)
		}
	}
	if definition.Load != nil {
		if len(definition.Load.Steps) == 0 {
			return fmt.Errorf("form load must contain at least one step")
		}
		if len(definition.Load.Outputs) == 0 {
			return fmt.Errorf("form load outputs are required")
		}
		for _, item := range definition.Load.Steps {
			if item.Return != nil {
				return fmt.Errorf("form load steps cannot contain return; use load.outputs")
			}
		}
	}
	seen := make(map[string]struct{}, len(definition.Fields))
	for index := range definition.Fields {
		field := &definition.Fields[index]
		if field.Type == "" {
			field.Type = TypeString
		}
		if strings.TrimSpace(field.Variable) == "" || strings.TrimSpace(field.Label) == "" {
			return fmt.Errorf("form field %d requires variable and label", index+1)
		}
		if _, exists := vars[field.Variable]; !exists {
			return fmt.Errorf("form field %q references undeclared variable", field.Variable)
		}
		if _, duplicate := seen[field.Variable]; duplicate {
			return fmt.Errorf("form variable %q is declared more than once", field.Variable)
		}
		seen[field.Variable] = struct{}{}
		switch field.Type {
		case TypeString, TypeBoolean, TypeNumber, TypeArray, TypeObject:
		default:
			return fmt.Errorf("form field %q has unsupported type %q", field.Variable, field.Type)
		}
		if field.Secret && field.Type != TypeString {
			return fmt.Errorf("form field %q secret is supported only for strings", field.Variable)
		}
		if len(field.Choices) > 0 && field.From != "" {
			return fmt.Errorf("form field %q cannot combine choices and from", field.Variable)
		}
		if field.From != "" && !strings.HasPrefix(field.From, "vars.") && !strings.HasPrefix(field.From, "data.") {
			return fmt.Errorf("form field %q source must start with vars. or data.", field.Variable)
		}
		if field.Type == TypeBoolean && (len(field.Choices) > 0 || field.From != "") {
			return fmt.Errorf("form field %q boolean cannot declare choices", field.Variable)
		}
		if field.Type == TypeObject && (len(field.Choices) > 0 || field.From != "") {
			return fmt.Errorf("form field %q object cannot declare choices", field.Variable)
		}
		if field.MinLength != nil && *field.MinLength < 0 || field.MaxLength != nil && *field.MaxLength < 0 {
			return fmt.Errorf("form field %q length constraints must be non-negative", field.Variable)
		}
		if field.MinLength != nil && field.MaxLength != nil && *field.MinLength > *field.MaxLength {
			return fmt.Errorf("form field %q min_length cannot exceed max_length", field.Variable)
		}
		if field.Pattern != "" {
			compiled, err := regexp.Compile(field.Pattern)
			if err != nil {
				return fmt.Errorf("form field %q pattern: %w", field.Variable, err)
			}
			field.pattern = compiled
		}
	}
	return nil
}

// Resolve freezes every field's initial value and dynamic choices.
func (definition *Definition) Resolve(vars, data map[string]any) ([]ResolvedField, error) {
	resolved := make([]ResolvedField, len(definition.Fields))
	for index, field := range definition.Fields {
		item := ResolvedField{Field: field, Value: workflow.Clone(vars[field.Variable])}
		options := field.Choices
		if field.From != "" {
			value, err := lookupSource(field.From, vars, data)
			if err != nil {
				return nil, fmt.Errorf("form field %q: %w", field.Variable, err)
			}
			options, err = dynamicChoices(field, value)
			if err != nil {
				return nil, fmt.Errorf("form field %q: %w", field.Variable, err)
			}
		}
		if err := validateChoices(field, options); err != nil {
			return nil, err
		}
		item.Options = options
		resolved[index] = item
	}
	return resolved, nil
}

func lookupSource(path string, vars, data map[string]any) (any, error) {
	parts := strings.Split(path, ".")
	var current any
	if parts[0] == "vars" {
		current = vars
	} else {
		current = data
	}
	for _, part := range parts[1:] {
		mapping, ok := current.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("source %q reaches a non-object at %q", path, part)
		}
		value, exists := mapping[part]
		if !exists {
			return nil, fmt.Errorf("source %q has no field %q", path, part)
		}
		current = value
	}
	return current, nil
}

func dynamicChoices(field Field, value any) ([]Choice, error) {
	items, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("source %q must resolve to a list", field.From)
	}
	choices := make([]Choice, 0, len(items))
	for index, item := range items {
		if scalar(item) {
			choices = append(choices, Choice{Label: fmt.Sprint(item), Value: item})
			continue
		}
		mapping, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("source item %d must be a scalar or object", index+1)
		}
		labelField := field.LabelField
		if labelField == "" {
			labelField = "label"
		}
		valueField := field.ValueField
		if valueField == "" {
			valueField = "value"
		}
		label, ok := mapping[labelField]
		if !ok {
			return nil, fmt.Errorf("source item %d has no label field %q", index+1, labelField)
		}
		choiceValue, ok := mapping[valueField]
		if !ok {
			return nil, fmt.Errorf("source item %d has no value field %q", index+1, valueField)
		}
		choice := Choice{Label: fmt.Sprint(label), Value: choiceValue}
		if field.DescriptionField != "" {
			choice.Description = fmt.Sprint(mapping[field.DescriptionField])
		}
		if field.DisabledField != "" {
			choice.Disabled, _ = mapping[field.DisabledField].(bool)
		}
		if choice.Disabled && field.ReasonField != "" {
			choice.Reason = fmt.Sprint(mapping[field.ReasonField])
		}
		choices = append(choices, choice)
	}
	return choices, nil
}

func validateChoices(field Field, choices []Choice) error {
	seen := make([]any, 0, len(choices))
	for index, choice := range choices {
		if strings.TrimSpace(choice.Label) == "" || !scalar(choice.Value) {
			return fmt.Errorf("form field %q choice %d requires a label and scalar value", field.Variable, index+1)
		}
		if choice.Disabled && strings.TrimSpace(choice.Reason) == "" {
			return fmt.Errorf("form field %q choice %d is disabled without a reason", field.Variable, index+1)
		}
		for _, previous := range seen {
			if reflect.DeepEqual(previous, choice.Value) {
				return fmt.Errorf("form field %q contains duplicate choice value %v", field.Variable, choice.Value)
			}
		}
		seen = append(seen, choice.Value)
	}
	return nil
}

// Submit decodes and validates one HTML form submission against frozen fields.
func Submit(fields []ResolvedField, values url.Values) (map[string]any, map[string]string) {
	result := make(map[string]any)
	errors := make(map[string]string)
	for index, field := range fields {
		value, present, err := submitField(index, field, values)
		if err == nil && present {
			err = validateValue(field, value)
		}
		if err != nil {
			errors[field.Variable] = err.Error()
			continue
		}
		if present {
			result[field.Variable] = value
		}
	}
	return result, errors
}

func submitField(index int, field ResolvedField, values url.Values) (any, bool, error) {
	name := "field_" + strconv.Itoa(index)
	if len(field.Options) > 0 {
		selected := values[name]
		if field.Type != TypeArray {
			if len(selected) == 0 || selected[0] == "" {
				return nil, false, requiredError(field)
			}
			choice, err := selectedChoice(field, selected[0])
			return choice, err == nil, err
		}
		items := make([]any, 0, len(selected))
		for _, raw := range selected {
			choice, err := selectedChoice(field, raw)
			if err != nil {
				return nil, false, err
			}
			items = append(items, choice)
		}
		if len(items) == 0 && field.Required {
			return nil, false, fmt.Errorf("select at least one value")
		}
		return items, true, nil
	}
	raw := values.Get(name)
	switch field.Type {
	case TypeBoolean:
		return raw == "true", true, nil
	case TypeString:
		if field.Secret && raw == "" {
			if existing, ok := field.Value.(string); ok && existing != "" {
				return existing, true, nil
			}
		}
		if raw == "" && field.Required {
			return nil, false, fmt.Errorf("value is required")
		}
		return raw, true, nil
	case TypeNumber:
		if raw == "" {
			return nil, false, requiredError(field)
		}
		decoder := json.NewDecoder(strings.NewReader(raw))
		decoder.UseNumber()
		var value any
		if err := decoder.Decode(&value); err != nil || hasMoreJSON(decoder) {
			return nil, false, fmt.Errorf("must be a number")
		}
		if _, ok := value.(json.Number); !ok {
			return nil, false, fmt.Errorf("must be a number")
		}
		return value, true, nil
	case TypeArray, TypeObject:
		if raw == "" {
			return nil, false, requiredError(field)
		}
		decoder := json.NewDecoder(strings.NewReader(raw))
		decoder.UseNumber()
		var value any
		if err := decoder.Decode(&value); err != nil || hasMoreJSON(decoder) {
			return nil, false, fmt.Errorf("must be valid JSON")
		}
		if field.Type == TypeArray {
			if _, ok := value.([]any); !ok {
				return nil, false, fmt.Errorf("must be a JSON array")
			}
		} else if _, ok := value.(map[string]any); !ok {
			return nil, false, fmt.Errorf("must be a JSON object")
		}
		return value, true, nil
	default:
		return nil, false, fmt.Errorf("unsupported field type")
	}
}

func hasMoreJSON(decoder *json.Decoder) bool {
	var extra any
	return decoder.Decode(&extra) != io.EOF
}

func selectedChoice(field ResolvedField, raw string) (any, error) {
	index, err := strconv.Atoi(raw)
	if err != nil || index < 0 || index >= len(field.Options) {
		return nil, fmt.Errorf("selected value is unavailable")
	}
	choice := field.Options[index]
	if choice.Disabled {
		return nil, fmt.Errorf("selected value is disabled: %s", choice.Reason)
	}
	return workflow.Clone(choice.Value), nil
}

func requiredError(field ResolvedField) error {
	if field.Required {
		return fmt.Errorf("value is required")
	}
	return nil
}

func validateValue(field ResolvedField, value any) error {
	text, ok := value.(string)
	if !ok {
		return nil
	}
	if field.MinLength != nil && len([]rune(text)) < *field.MinLength {
		return validationError(field, fmt.Sprintf("must contain at least %d characters", *field.MinLength))
	}
	if field.MaxLength != nil && len([]rune(text)) > *field.MaxLength {
		return validationError(field, fmt.Sprintf("must contain at most %d characters", *field.MaxLength))
	}
	if field.pattern != nil && !field.pattern.MatchString(text) {
		return validationError(field, "has an invalid format")
	}
	return nil
}

func validationError(field ResolvedField, fallback string) error {
	if strings.TrimSpace(field.Message) != "" {
		return fmt.Errorf("%s", field.Message)
	}
	return fmt.Errorf("%s", fallback)
}

func scalar(value any) bool {
	switch value.(type) {
	case nil, string, bool, int, int64, float64, json.Number:
		return true
	default:
		return false
	}
}
