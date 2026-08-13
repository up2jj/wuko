package step

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

// TextValidation declares reusable validation rules for text-entry steps.
type TextValidation struct {
	MinLength int    `yaml:"min_length,omitempty"`
	MaxLength int    `yaml:"max_length,omitempty"`
	Pattern   string `yaml:"pattern,omitempty"`
	Message   string `yaml:"message,omitempty"`
}

// TextValidator validates one text value without exposing that value in errors.
type TextValidator func(string) error

// CompileTextValidator validates the rule configuration and returns a reusable validator.
func CompileTextValidator(required bool, rules TextValidation) (TextValidator, error) {
	if rules.MinLength < 0 {
		return nil, fmt.Errorf("validation min_length cannot be negative")
	}
	if rules.MaxLength < 0 {
		return nil, fmt.Errorf("validation max_length cannot be negative")
	}
	if rules.MaxLength > 0 && rules.MinLength > rules.MaxLength {
		return nil, fmt.Errorf("validation min_length cannot exceed max_length")
	}

	var pattern *regexp.Regexp
	if rules.Pattern != "" {
		compiled, err := regexp.Compile(rules.Pattern)
		if err != nil {
			return nil, fmt.Errorf("validation pattern: %w", err)
		}
		pattern = compiled
	}

	validationError := func(fallback string) error {
		if rules.Message != "" {
			return errors.New(rules.Message)
		}
		return errors.New(fallback)
	}

	return func(value string) error {
		if required && strings.TrimSpace(value) == "" {
			return validationError("a value is required")
		}
		if value == "" {
			return nil
		}
		length := utf8.RuneCountInString(value)
		if length < rules.MinLength {
			return validationError(fmt.Sprintf("must be at least %d characters", rules.MinLength))
		}
		if rules.MaxLength > 0 && length > rules.MaxLength {
			return validationError(fmt.Sprintf("must be at most %d characters", rules.MaxLength))
		}
		if pattern != nil && !pattern.MatchString(value) {
			return validationError("has an invalid format")
		}
		return nil
	}, nil
}
