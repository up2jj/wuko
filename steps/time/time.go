// Package time implements explicit, recordable workflow time capture and transformation.
package time

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	stdtime "time"

	"github.com/up2jj/wuko/expression"
	"github.com/up2jj/wuko/step"
)

var variablePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

type Config struct {
	Value       string     `yaml:"value,omitempty"`
	ParseFormat string     `yaml:"parse_format,omitempty"`
	Timezone    string     `yaml:"timezone,omitempty"`
	Add         Adjustment `yaml:"add,omitempty"`
	Format      string     `yaml:"format,omitempty"`
	Variable    string     `yaml:"variable,omitempty"`
}

type Adjustment struct {
	Years    int    `yaml:"years,omitempty"`
	Months   int    `yaml:"months,omitempty"`
	Days     int    `yaml:"days,omitempty"`
	Duration string `yaml:"duration,omitempty"`
}

type Runner struct {
	config   Config
	hasValue bool
	hasAdd   bool
	// addKeys records which adjustment fields the author wrote, so an explicit zero
	// still counts as an adjustment while a bare "add: {}" stays an error.
	addKeys map[string]struct{}
	now     func() stdtime.Time
}

func Register(registry *step.Registry) error { return registry.Register("time", New) }

func New(raw map[string]any) (step.Runner, error) {
	return NewWithClock(raw, stdtime.Now)
}

// NewWithClock builds a time runner with an injected clock for deterministic tests and embedding.
func NewWithClock(raw map[string]any, now func() stdtime.Time) (step.Runner, error) {
	var config Config
	if err := step.DecodeConfig(raw, &config); err != nil {
		return nil, err
	}
	if now == nil {
		return nil, fmt.Errorf("clock is required")
	}
	_, hasValue := raw["value"]
	_, hasAdd := raw["add"]
	if _, exists := raw["variable"]; exists && !templated(config.Variable) && !variablePattern.MatchString(config.Variable) {
		return nil, fmt.Errorf("invalid variable name %q", config.Variable)
	}
	if _, exists := raw["parse_format"]; exists {
		if !hasValue {
			return nil, fmt.Errorf("parse_format requires value")
		}
		if strings.TrimSpace(config.ParseFormat) == "" {
			return nil, fmt.Errorf("parse_format must not be blank")
		}
	}
	if _, exists := raw["format"]; exists && strings.TrimSpace(config.Format) == "" {
		return nil, fmt.Errorf("format must not be blank")
	}
	if _, exists := raw["timezone"]; exists && !templated(config.Timezone) {
		if strings.TrimSpace(config.Timezone) == "" {
			return nil, fmt.Errorf("timezone must not be blank")
		}
		if _, err := stdtime.LoadLocation(config.Timezone); err != nil {
			return nil, fmt.Errorf("invalid timezone %q: %w", config.Timezone, err)
		}
	}
	addKeys := adjustmentKeys(raw)
	if hasAdd && !templated(config.Add.Duration) {
		adjustments := adjustmentMap(config.Add, addKeys, "UTC")
		if _, err := expression.AddTime("2000-01-01T00:00:00Z", adjustments); err != nil {
			return nil, err
		}
	}
	return &Runner{config: config, hasValue: hasValue, hasAdd: hasAdd, addKeys: addKeys, now: now}, nil
}

func (runner *Runner) Run(ctx context.Context, request step.Request) (step.Result, error) {
	if err := ctx.Err(); err != nil {
		return step.Result{}, err
	}
	variable := runner.config.Variable
	if variable == "" {
		variable = request.StepID
	}
	if !variablePattern.MatchString(variable) {
		return step.Result{}, fmt.Errorf("invalid variable name %q", variable)
	}
	if override, exists := request.PresetVars[variable]; exists {
		value, err := presetValue(variable, override, runner.config.Format)
		if err != nil {
			return step.Result{}, err
		}
		return result(variable, value), nil
	}

	timezone := runner.config.Timezone
	if timezone == "" {
		timezone = request.WorkflowTimezone
	}
	if timezone == "" {
		timezone = stdtime.Local.String()
	}
	location, err := stdtime.LoadLocation(timezone)
	if err != nil {
		return step.Result{}, fmt.Errorf("invalid timezone %q: %w", timezone, err)
	}

	var canonical string
	if runner.hasValue {
		layout := runner.config.ParseFormat
		if layout == "" {
			layout = expression.DefaultTimeLayout
		}
		canonical, err = expression.ParseTime(runner.config.Value, layout, timezone)
	} else {
		canonical = runner.now().In(location).Format(expression.DefaultTimeLayout)
	}
	if err != nil {
		return step.Result{}, err
	}
	if runner.hasAdd {
		canonical, err = expression.AddTime(canonical, adjustmentMap(runner.config.Add, runner.addKeys, timezone))
		if err != nil {
			return step.Result{}, err
		}
	}
	format := runner.config.Format
	if format == "" {
		format = expression.DefaultTimeLayout
	}
	value, err := expression.FormatTime(canonical, format, timezone)
	if err != nil {
		return step.Result{}, err
	}
	return result(variable, value), nil
}

// presetValue renders a variable the run was started with. Text is published exactly as
// supplied. An unquoted YAML date such as "stamp: 2026-08-29" decodes to a time value
// rather than text, so it is rendered with the step's format in the zone it carries, and
// a whole number is rendered in decimal -- both are natural ways to pin a stamp.
func presetValue(variable string, supplied any, format string) (string, error) {
	switch typed := supplied.(type) {
	case string:
		if strings.TrimSpace(typed) == "" {
			return "", fmt.Errorf("pre-supplied variable %q must not be blank", variable)
		}
		return typed, nil
	case stdtime.Time:
		if format == "" {
			format = expression.DefaultTimeLayout
		}
		return typed.Format(format), nil
	case int:
		return strconv.Itoa(typed), nil
	case int64:
		return strconv.FormatInt(typed, 10), nil
	case uint64:
		return strconv.FormatUint(typed, 10), nil
	default:
		return "", fmt.Errorf("pre-supplied variable %q must be a string, date, or whole number, got %T; quote the value to publish it verbatim", variable, supplied)
	}
}

// adjustmentKeys reports the adjustment fields present in the raw configuration. A
// non-object "add" yields an empty set, which keeps it an error rather than a no-op.
func adjustmentKeys(raw map[string]any) map[string]struct{} {
	nested, ok := raw["add"].(map[string]any)
	if !ok {
		return nil
	}
	keys := make(map[string]struct{}, len(nested))
	for name := range nested {
		keys[name] = struct{}{}
	}
	return keys
}

func adjustmentMap(adjustment Adjustment, keys map[string]struct{}, timezone string) map[string]any {
	result := map[string]any{"timezone": timezone}
	if _, written := keys["years"]; written || adjustment.Years != 0 {
		result["years"] = adjustment.Years
	}
	if _, written := keys["months"]; written || adjustment.Months != 0 {
		result["months"] = adjustment.Months
	}
	if _, written := keys["days"]; written || adjustment.Days != 0 {
		result["days"] = adjustment.Days
	}
	if adjustment.Duration != "" {
		result["duration"] = adjustment.Duration
	}
	return result
}

func templated(value string) bool { return strings.Contains(value, "{{") }

func result(variable, value string) step.Result {
	return step.Result{Outputs: map[string]any{"value": value}, Variables: map[string]any{variable: value}}
}
