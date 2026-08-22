// Package input implements an interactive, prepopulatable text input step.
package input

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/tui"
)

type Config struct {
	Variable   string              `yaml:"variable"`
	Message    string              `yaml:"message"`
	Value      string              `yaml:"value,omitempty"`
	Required   *bool               `yaml:"required,omitempty"`
	Validation step.TextValidation `yaml:"validation,omitempty"`
	Modifiers  Modifiers           `yaml:"modifiers,omitempty"`
}

type Modifiers struct {
	Split string `yaml:"split,omitempty"`
	JSON  bool   `yaml:"json,omitempty"`
	Trim  bool   `yaml:"trim,omitempty"`
}

type Runner struct {
	config   Config
	validate step.TextValidator
	modify   func(string) (any, error)
}

func Register(registry *step.Registry) error { return registry.Register("tui_input", New) }

func New(raw map[string]any) (step.Runner, error) {
	var config Config
	if err := step.DecodeConfig(raw, &config); err != nil {
		return nil, err
	}
	if config.Variable == "" {
		return nil, fmt.Errorf("variable is required")
	}
	if strings.TrimSpace(config.Message) == "" {
		return nil, fmt.Errorf("message is required")
	}
	validate, err := step.CompileTextValidator(required(config.Required), config.Validation)
	if err != nil {
		return nil, err
	}
	isRequired := required(config.Required)
	modify, err := compileModifier(config.Modifiers, isRequired)
	if err != nil {
		return nil, err
	}
	validateAndModify := func(value string) error {
		if err := validate(normalize(value, config.Modifiers.Trim)); err != nil {
			return err
		}
		_, err := modify(value)
		return err
	}
	return &Runner{config: config, validate: validateAndModify, modify: modify}, nil
}

func (r *Runner) Run(ctx context.Context, request step.Request) (step.Result, error) {
	if value, exists := request.Vars[r.config.Variable]; exists {
		text, ok := value.(string)
		if !ok {
			return step.Result{}, fmt.Errorf("pre-supplied variable %q must be a string", r.config.Variable)
		}
		if err := r.validate(text); err != nil {
			return step.Result{}, fmt.Errorf("pre-supplied variable %q: %w", r.config.Variable, err)
		}
		return r.result(text)
	}
	if !request.Interactive {
		return step.Result{}, fmt.Errorf("variable %q is required when stdin is non-interactive; supply it with --var", r.config.Variable)
	}
	value, err := tui.TextWithValidation(ctx, request.Stdin, request.Stdout, r.config.Message, r.config.Value, required(r.config.Required), r.validate)
	if err != nil {
		return step.Result{}, fmt.Errorf("reading input: %w", err)
	}
	return r.result(value)
}

func required(value *bool) bool { return value == nil || *value }

func (r *Runner) result(text string) (step.Result, error) {
	value, err := r.modify(text)
	if err != nil {
		return step.Result{}, fmt.Errorf("modifying input: %w", err)
	}
	return step.Result{
		Outputs:   map[string]any{"value": value},
		Variables: map[string]any{r.config.Variable: value},
	}, nil
}

func compileModifier(config Modifiers, required bool) (func(string) (any, error), error) {
	if config.Split != "" && config.JSON {
		return nil, fmt.Errorf("modifiers split and json are mutually exclusive")
	}
	if config.Split != "" {
		pattern, err := regexp.Compile(config.Split)
		if err != nil {
			return nil, fmt.Errorf("modifier split: %w", err)
		}
		return func(value string) (any, error) {
			value = normalize(value, config.Trim)
			if !required && value == "" {
				return []any{}, nil
			}
			parts := pattern.Split(value, -1)
			result := make([]any, len(parts))
			for i, part := range parts {
				if config.Trim {
					part = strings.TrimSpace(part)
				}
				result[i] = part
			}
			return result, nil
		}, nil
	}
	if config.JSON {
		return func(value string) (any, error) {
			value = normalize(value, config.Trim)
			if !required && value == "" {
				return nil, nil
			}
			return decodeJSON(value)
		}, nil
	}
	return func(value string) (any, error) { return normalize(value, config.Trim), nil }, nil
}

func normalize(value string, trim bool) string {
	if trim {
		return strings.TrimSpace(value)
	}
	return value
}

func decodeJSON(value string) (any, error) {
	decoder := json.NewDecoder(strings.NewReader(value))
	decoder.UseNumber()
	var result any
	if err := decoder.Decode(&result); err != nil {
		return nil, fmt.Errorf("must be valid JSON: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("must contain exactly one JSON value")
		}
		return nil, fmt.Errorf("must be valid JSON: %w", err)
	}
	return result, nil
}
