package reporter

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	// ValueStringLimit bounds each string inside a reported value.
	ValueStringLimit = 4 << 10
	// ValueLimit bounds the encoded size of one step's outputs or one variable.
	ValueLimit = 64 << 10
)

// ValueInclude selects the run values a JSON report adds. The zero value adds none.
type ValueInclude struct {
	StepOutputs bool
	Vars        bool
}

// Any reports whether any values are requested, which is when the engine must capture them.
func (include ValueInclude) Any() bool { return include.StepOutputs || include.Vars }

// ParseValueInclude reads the --report-json-include names.
func ParseValueInclude(names []string) (ValueInclude, error) {
	var include ValueInclude
	for _, name := range names {
		switch strings.TrimSpace(name) {
		case "outputs":
			include.StepOutputs = true
		case "vars":
			include.Vars = true
		default:
			return ValueInclude{}, fmt.Errorf("unknown report value %q; expected outputs or vars", name)
		}
	}
	return include, nil
}

func reportWithValues(outcome Outcome, include ValueInclude) ExecutionReport {
	report := NewExecutionReport(outcome)
	if outcome.Values == nil {
		return report
	}
	if include.StepOutputs {
		report.StepOutputs = limitValues(outcome.Values.Steps, &report.ValuesTruncated)
	}
	if include.Vars {
		report.Vars = limitValues(outcome.Values.Vars, &report.ValuesTruncated)
	}
	return report
}

// limitValues bounds each entry separately, so one large value does not hide the others.
// The engine already normalized and redacted the values; this only shortens them.
func limitValues(values map[string]any, truncated *bool) map[string]any {
	result := make(map[string]any, len(values))
	for key, value := range values {
		value = limitStrings(value, truncated)
		if encoded, err := json.Marshal(value); err == nil && len(encoded) > ValueLimit {
			value = fmt.Sprintf("<truncated: %d bytes>", len(encoded))
			*truncated = true
		}
		result[key] = value
	}
	return result
}

func limitStrings(value any, truncated *bool) any {
	switch typed := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(typed))
		for key, item := range typed {
			result[key] = limitStrings(item, truncated)
		}
		return result
	case []any:
		result := make([]any, len(typed))
		for index, item := range typed {
			result[index] = limitStrings(item, truncated)
		}
		return result
	case string:
		if len(typed) <= ValueStringLimit {
			return typed
		}
		*truncated = true
		end := ValueStringLimit
		for end > 0 && !utf8.RuneStart(typed[end]) {
			end--
		}
		return typed[:end] + "…<truncated>"
	}
	return value
}
