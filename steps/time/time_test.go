package time

import (
	"context"
	"strings"
	"testing"
	stdtime "time"

	"github.com/up2jj/wuko/step"
)

func TestRunnerCapturesTransformsAndPublishesTime(t *testing.T) {
	t.Parallel()
	calls := 0
	runner, err := NewWithClock(map[string]any{
		"add":    map[string]any{"days": 1, "duration": "2h"},
		"format": "2006-01-02 15:04",
	}, func() stdtime.Time {
		calls++
		return stdtime.Date(2026, 3, 28, 12, 0, 0, 0, stdtime.UTC)
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(t.Context(), step.Request{StepID: "stamp", WorkflowTimezone: "Europe/Warsaw", Vars: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || result.Outputs["value"] != "2026-03-29 15:00" || result.Variables["stamp"] != result.Outputs["value"] {
		t.Fatalf("calls = %d, result = %#v", calls, result)
	}
}

func TestRunnerParsesExplicitValue(t *testing.T) {
	t.Parallel()
	runner, err := NewWithClock(map[string]any{
		"value": "2026-08-29 10:00", "parse_format": "2006-01-02 15:04",
		"timezone": "Europe/Warsaw", "format": "2006-01-02T15:04:05Z07:00", "variable": "published",
	}, func() stdtime.Time { t.Fatal("clock called"); return stdtime.Time{} })
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(t.Context(), step.Request{StepID: "stamp", Vars: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outputs["value"] != "2026-08-29T10:00:00+02:00" || result.Variables["published"] != result.Outputs["value"] {
		t.Fatalf("result = %#v", result)
	}
}

func TestRunnerUsesPreSuppliedFinalValueWithoutClock(t *testing.T) {
	t.Parallel()
	runner, err := NewWithClock(map[string]any{"format": "2006"}, func() stdtime.Time {
		t.Fatal("clock called")
		return stdtime.Time{}
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(t.Context(), step.Request{StepID: "stamp", PresetVars: map[string]any{"stamp": "fixture-date"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outputs["value"] != "fixture-date" || result.Variables["stamp"] != "fixture-date" {
		t.Fatalf("result = %#v", result)
	}
}

func TestNewDefersValidationOfTemplatedFields(t *testing.T) {
	t.Parallel()
	raw := map[string]any{
		"timezone": "{{ .vars.zone }}",
		"variable": "{{ .vars.who }}",
		"add":      map[string]any{"duration": "{{ .vars.delta }}"},
	}
	if _, err := NewWithClock(raw, stdtime.Now); err != nil {
		t.Fatalf("templated configuration rejected before rendering: %v", err)
	}
}

func TestRunnerAcceptsDateAndNumberPins(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		format   string
		supplied any
		want     string
	}{
		{name: "yaml date", format: "2006-01-02", supplied: stdtime.Date(2026, 8, 29, 0, 0, 0, 0, stdtime.UTC), want: "2026-08-29"},
		{name: "yaml date default format", supplied: stdtime.Date(2026, 8, 29, 0, 0, 0, 0, stdtime.UTC), want: "2026-08-29T00:00:00Z"},
		{name: "offset is preserved", format: "2006-01-02 15:04 -0700", supplied: stdtime.Date(2026, 8, 29, 10, 0, 0, 0, stdtime.FixedZone("", 2*60*60)), want: "2026-08-29 10:00 +0200"},
		{name: "whole number", format: "2006", supplied: 2026, want: "2026"},
		{name: "quoted string is verbatim", format: "2006", supplied: "2026-08-29", want: "2026-08-29"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := map[string]any{}
			if tt.format != "" {
				raw["format"] = tt.format
			}
			runner, err := NewWithClock(raw, func() stdtime.Time { t.Fatal("clock called"); return stdtime.Time{} })
			if err != nil {
				t.Fatal(err)
			}
			result, err := runner.Run(t.Context(), step.Request{StepID: "stamp", PresetVars: map[string]any{"stamp": tt.supplied}})
			if err != nil {
				t.Fatal(err)
			}
			if result.Outputs["value"] != tt.want || result.Variables["stamp"] != tt.want {
				t.Fatalf("result = %#v, want %q", result, tt.want)
			}
		})
	}
}

func TestRunnerRejectsUnrenderablePinWithQuotingHint(t *testing.T) {
	t.Parallel()
	runner, err := NewWithClock(map[string]any{}, stdtime.Now)
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.Run(t.Context(), step.Request{StepID: "stamp", PresetVars: map[string]any{"stamp": true}})
	if err == nil || !strings.Contains(err.Error(), "quote the value") {
		t.Fatalf("error = %v, want a quoting hint", err)
	}
}

func TestRunnerIgnoresVariablesWrittenDuringTheRun(t *testing.T) {
	t.Parallel()
	calls := 0
	runner, err := NewWithClock(map[string]any{"format": "2006-01-02 15:04:05"}, func() stdtime.Time {
		calls++
		return stdtime.Date(2026, 8, 29, 12, 0, calls, 0, stdtime.UTC)
	})
	if err != nil {
		t.Fatal(err)
	}
	// A loop reuses one state, so an earlier iteration's own write lands in Vars.
	// Only PresetVars pins the value; Vars must not stop the clock.
	request := step.Request{StepID: "stamp", WorkflowTimezone: "UTC", Vars: map[string]any{"stamp": "2026-01-01 00:00:00"}}
	first, err := runner.Run(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	request.Vars = map[string]any{"stamp": first.Outputs["value"]}
	second, err := runner.Run(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if first.Outputs["value"] == second.Outputs["value"] {
		t.Fatalf("clock was not re-read: first = %#v, second = %#v", first, second)
	}
	if first.Outputs["value"] != "2026-08-29 12:00:01" || second.Outputs["value"] != "2026-08-29 12:00:02" {
		t.Fatalf("first = %#v, second = %#v", first, second)
	}
}

func TestRunnerAppliesExplicitZeroAdjustments(t *testing.T) {
	t.Parallel()
	for _, add := range []map[string]any{{"days": 0}, {"duration": "0s"}} {
		runner, err := NewWithClock(map[string]any{
			"value": "2026-08-29T10:00:00Z", "timezone": "UTC", "add": add,
		}, stdtime.Now)
		if err != nil {
			t.Fatalf("add %v rejected: %v", add, err)
		}
		result, err := runner.Run(t.Context(), step.Request{StepID: "stamp"})
		if err != nil {
			t.Fatalf("add %v: %v", add, err)
		}
		if result.Outputs["value"] != "2026-08-29T10:00:00Z" {
			t.Fatalf("add %v produced %#v", add, result)
		}
	}
}

func TestRunnerValidationAndCancellation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		raw  map[string]any
		want string
	}{
		{name: "parse format without value", raw: map[string]any{"parse_format": "2006"}, want: "requires value"},
		{name: "blank variable", raw: map[string]any{"variable": " "}, want: "invalid variable"},
		{name: "invalid timezone", raw: map[string]any{"timezone": "Mars/Olympus"}, want: "invalid timezone"},
		{name: "unknown add field", raw: map[string]any{"add": map[string]any{"weeks": 1}}, want: "field weeks"},
		{name: "empty add", raw: map[string]any{"add": map[string]any{}}, want: "must include"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewWithClock(tt.raw, stdtime.Now)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
		})
	}

	runner, err := NewWithClock(map[string]any{}, stdtime.Now)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := runner.Run(ctx, step.Request{StepID: "stamp"}); err == nil {
		t.Fatal("expected cancellation")
	}
}
