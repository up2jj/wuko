package engine

import (
	"bytes"
	"cmp"
	"encoding/json"
	"slices"
	"strings"
	"sync"

	"github.com/up2jj/wuko/diagnostic"
	"github.com/up2jj/wuko/secret"
)

// RunValues is a redacted snapshot of a finished run's step outputs and final variables.
// Values are normalized to JSON shapes: maps, slices, strings, booleans, json.Number, and nil.
type RunValues struct {
	Steps map[string]any
	Vars  map[string]any
}

// sensitiveValues collects step-declared sensitive values. Concurrent branches add to it.
type sensitiveValues struct {
	mu     sync.Mutex
	values []string
}

func (values *sensitiveValues) add(items []string) {
	if len(items) == 0 {
		return
	}
	values.mu.Lock()
	defer values.mu.Unlock()
	for _, item := range items {
		if item != "" && !slices.Contains(values.values, item) {
			values.values = append(values.values, item)
		}
	}
}

// snapshot returns the values longest first, so a value that prefixes a longer one cannot
// leave the longer one's remainder visible.
func (values *sensitiveValues) snapshot() []string {
	values.mu.Lock()
	result := slices.Clone(values.values)
	values.mu.Unlock()
	slices.SortFunc(result, func(a, b string) int { return cmp.Or(len(b)-len(a), strings.Compare(a, b)) })
	return result
}

func captureRunValues(state *State, sensitive []string, session *secret.Session) *RunValues {
	if state == nil {
		return nil
	}
	redact := func(value string) string {
		for _, item := range sensitive {
			value = strings.ReplaceAll(value, item, diagnostic.Redacted)
		}
		return session.Redact(value)
	}
	// Step IDs are not values, so key rules apply within each step's outputs; variable
	// names are, so a variable named like a credential is redacted as a whole.
	steps := make(map[string]any, len(state.Steps))
	for id, outputs := range state.Steps {
		steps[id] = redactValue(diagnostic.RedactValue(normalizeValue(outputs)), redact)
	}
	vars := make(map[string]any, len(state.Vars))
	for name, value := range state.Vars {
		vars[name] = normalizeValue(value)
	}
	redactedVars, _ := redactValue(diagnostic.RedactValue(vars), redact).(map[string]any)
	return &RunValues{Steps: steps, Vars: redactedVars}
}

// normalizeValue encodes each entry independently, so one value that cannot be encoded
// does not hide the others.

func normalizeValue(value any) any {
	raw, err := json.Marshal(value)
	if err != nil {
		return "<unavailable: " + err.Error() + ">"
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var normalized any
	if err := decoder.Decode(&normalized); err != nil {
		return "<unavailable: " + err.Error() + ">"
	}
	return normalized
}

func redactValue(value any, redact func(string) string) any {
	switch typed := value.(type) {
	case map[string]any:
		for key, item := range typed {
			typed[key] = redactValue(item, redact)
		}
	case []any:
		for index, item := range typed {
			typed[index] = redactValue(item, redact)
		}
	case string:
		return redact(typed)
	}
	return value
}
