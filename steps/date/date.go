// Package date implements a calendar date prompt with relative day shortcuts.
package date

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/tui"
)

type Config struct {
	Variable string `yaml:"variable"`
	Message  string `yaml:"message"`
	Value    string `yaml:"value,omitempty"`
	Format   string `yaml:"format,omitempty"`
	Min      string `yaml:"min,omitempty"`
	Max      string `yaml:"max,omitempty"`
	Required *bool  `yaml:"required,omitempty"`
	// NonInteractive answers with Value when the run cannot prompt.
	NonInteractive step.NonInteractive `yaml:"noninteractive,omitempty"`
}

type Runner struct {
	config Config
	// hasDefault records an explicit value, which may be empty, to answer with.
	hasDefault bool
	now        func() time.Time
}

func Register(registry *step.Registry) error {
	return registry.RegisterDefinition("tui_date", step.Registration{Builder: New, Outputs: step.ClosedOutputs("value")})
}

func New(raw map[string]any) (step.Runner, error) {
	return NewWithClock(raw, time.Now)
}

// NewWithClock builds a runner with an injected clock for deterministic embedding.
func NewWithClock(raw map[string]any, now func() time.Time) (step.Runner, error) {
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
	if now == nil {
		return nil, fmt.Errorf("clock is required")
	}
	_, hasDefault := raw["value"]
	if err := config.NonInteractive.Validate(hasDefault); err != nil {
		return nil, err
	}
	if _, exists := raw["format"]; !exists {
		config.Format = defaultFormat
	}
	if !templated(config.Format) {
		if err := validateFormat(config.Format); err != nil {
			return nil, err
		}
	}
	for _, bound := range [][2]string{{"min", config.Min}, {"max", config.Max}} {
		if _, exists := raw[bound[0]]; exists && strings.TrimSpace(bound[1]) == "" {
			return nil, fmt.Errorf("%s must not be blank", bound[0])
		}
	}
	// Mixed absolute and relative bounds depend on the step's captured reference
	// date, so PreparePicker checks their order again.
	if !templated(config.Format) && !templated(config.Min) && !templated(config.Max) {
		if err := checkStaticBounds(config); err != nil {
			return nil, err
		}
	}
	return &Runner{config: config, hasDefault: hasDefault, now: now}, nil
}

// Location resolves a workflow timezone, defaulting to the local zone. Callers
// preparing several pickers resolve it once because each lookup reads tzdata.
func Location(timezone string) (*time.Location, error) {
	if timezone == "" {
		return time.Local, nil
	}
	location, err := time.LoadLocation(timezone)
	if err != nil {
		return nil, fmt.Errorf("invalid workflow timezone: %w", err)
	}
	return location, nil
}

// PreparePicker freezes the reference date and bounds for a standalone prompt or
// one form field. Its validator is also used for unattended values and drafts.
func (r *Runner) PreparePicker(reference time.Time, location *time.Location) (tui.DatePickerConfig, func(any) (any, error), error) {
	today := tui.CalendarDate(reference.In(location))
	minimum, maximum, err := validateBounds(r.config, today)
	if err != nil {
		return tui.DatePickerConfig{}, nil, err
	}
	required := r.config.Required == nil || *r.config.Required
	parse := func(value string) (time.Time, error) {
		if strings.TrimSpace(value) == "" {
			if required {
				return time.Time{}, fmt.Errorf("a date is required")
			}
			return time.Time{}, nil
		}
		date, err := parseDate(value, r.config.Format, today)
		if err != nil {
			return time.Time{}, err
		}
		if !minimum.IsZero() && date.Before(minimum) {
			return time.Time{}, fmt.Errorf("date must be on or after %s", minimum.Format(r.config.Format))
		}
		if !maximum.IsZero() && date.After(maximum) {
			return time.Time{}, fmt.Errorf("date must be on or before %s", maximum.Format(r.config.Format))
		}
		return date, nil
	}
	validate := func(value any) (any, error) {
		text, err := dateText(value)
		if err != nil {
			return nil, err
		}
		parsed, err := parse(text)
		if err != nil {
			return nil, err
		}
		if parsed.IsZero() {
			return "", nil
		}
		return parsed.Format(r.config.Format), nil
	}
	return tui.DatePickerConfig{Message: r.config.Message, Value: r.config.Value, Format: r.config.Format,
		Required: required, Today: today, Min: minimum, Max: maximum, Parse: parse}, validate, nil
}

func (r *Runner) Run(ctx context.Context, request step.Request) (step.Result, error) {
	if err := ctx.Err(); err != nil {
		return step.Result{}, err
	}
	location, err := Location(request.WorkflowTimezone)
	if err != nil {
		return step.Result{}, err
	}
	config, validate, err := r.PreparePicker(r.now(), location)
	if err != nil {
		return step.Result{}, err
	}
	value, supplied := request.Vars[r.config.Variable]
	switch {
	case supplied:
	case r.hasDefault && request.UseDefault(r.config.NonInteractive):
		value = r.config.Value
	case !request.Interactive:
		return step.Result{}, step.MissingAnswerError(r.config.Variable, r.hasDefault)
	default:
		value, err = tui.PickDate(ctx, request.Stdin, request.Stdout, config)
		if err != nil {
			return step.Result{}, fmt.Errorf("selecting date: %w", err)
		}
	}
	normalized, err := validate(value)
	if err != nil {
		return step.Result{}, fmt.Errorf("variable %q: %w", r.config.Variable, err)
	}
	if err := ctx.Err(); err != nil {
		return step.Result{}, err
	}
	return step.Result{Outputs: map[string]any{"value": normalized}, Variables: map[string]any{r.config.Variable: normalized}}, nil
}

func templated(value string) bool {
	return strings.Contains(value, "{{") && strings.Contains(value, "}}")
}
