package date

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/up2jj/wuko/step"
)

func dateRunner(t *testing.T, extra map[string]any, reference string) *Runner {
	t.Helper()
	raw := map[string]any{"variable": "due", "message": "Choose a deadline"}
	for key, value := range extra {
		raw[key] = value
	}
	instant, err := time.Parse(time.RFC3339, reference)
	if err != nil {
		t.Fatal(err)
	}
	runner, err := NewWithClock(raw, func() time.Time { return instant })
	if err != nil {
		t.Fatal(err)
	}
	return runner.(*Runner)
}

func TestDateFormatsAndConfiguration(t *testing.T) {
	t.Parallel()
	for _, format := range []string{"2006-01-02", "02.01.2006", "January 2, 2006", "Monday, 2 January 2006", "20060102"} {
		t.Run(format, func(t *testing.T) {
			if err := validateFormat(format); err != nil {
				t.Fatal(err)
			}
		})
	}
	for _, format := range []string{"", " ", "YYYY-MM-DD", "06-01-02", "2006-01", "01-02", "2006-01-02 15:04", "2006-01-02 -0700", "2006-01-02 MST", "2006-01-02.000", "+20060102d", " 2006-01-02 ", "2006\n01\n02"} {
		t.Run("rejects/"+format, func(t *testing.T) {
			if err := validateFormat(format); err == nil {
				t.Fatal("accepted incomplete or time-bearing layout")
			}
		})
	}
	for _, raw := range []map[string]any{
		{"message": "Date"}, {"variable": "due", "message": " "},
		{"variable": "due", "message": "Date", "format": ""},
		{"variable": "due", "message": "Date", "unknown": true},
		{"variable": "due", "message": "Date", "min": ""},
		{"variable": "due", "message": "Date", "max": "2026-02-30"},
		{"variable": "due", "message": "Date", "min": "2026-10-24", "max": "2026-10-23"},
		// Relative pairs and out-of-range spans fail before the workflow runs.
		{"variable": "due", "message": "Date", "min": "+2d", "max": "+1d"},
		{"variable": "due", "message": "Date", "max": "+999999999999999999999999d"},
		{"variable": "due", "message": "Date", "min": "-3652059d"},
	} {
		if _, err := New(raw); err == nil {
			t.Fatalf("New(%v) succeeded", raw)
		}
	}
	if _, err := NewWithClock(map[string]any{"variable": "due", "message": "Date"}, nil); err == nil {
		t.Fatal("accepted nil clock")
	}
}

func TestDateSuppliedValues(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name          string
		value         any
		config        map[string]any
		want, wantErr string
	}{
		{name: "absolute", value: "2026-10-23", want: "2026-10-23"},
		{name: "future", value: "+2d", want: "2026-10-25"},
		{name: "past", value: "-7d", want: "2026-10-16"},
		{name: "today", value: "+0d", want: "2026-10-23"},
		{name: "trimmed", value: " +2d ", want: "2026-10-25"},
		{name: "custom", value: "+2d", config: map[string]any{"format": "02.01.2006"}, want: "25.10.2026"},
		{name: "custom absolute", value: "25.10.2026", config: map[string]any{"format": "02.01.2006"}, want: "25.10.2026"},
		{name: "inclusive minimum", value: "+2d", config: map[string]any{"min": "+2d", "max": "+4d"}, want: "2026-10-25"},
		{name: "inclusive maximum", value: "+4d", config: map[string]any{"min": "+2d", "max": "+4d"}, want: "2026-10-27"},
		{name: "chronological bounds", value: "02.11.2026", config: map[string]any{"format": "02.01.2006", "min": "30.10.2026", "max": "03.11.2026"}, want: "02.11.2026"},
		{name: "below bounds", value: "-7d", config: map[string]any{"min": "+0d"}, wantErr: "on or after"},
		{name: "above bounds", value: "+2d", config: map[string]any{"max": "+1d"}, wantErr: "on or before"},
		{name: "optional", value: " ", config: map[string]any{"required": false}, want: ""},
		{name: "required", value: "", wantErr: "date is required"},
		{name: "first supported date", value: "0001-01-01", config: map[string]any{"max": "0001-01-01"}, want: "0001-01-01"},
		{name: "last supported date", value: "9999-12-31", want: "9999-12-31"},
		{name: "invalid leap day", value: "2026-02-29", wantErr: "valid date"},
		{name: "invalid date", value: "2026-02-30", wantErr: "valid date"},
		{name: "wrong format", value: "2026-10-23", config: map[string]any{"format": "02.01.2006"}, wantErr: "valid date"},
		{name: "weekday", value: "friday, 23 october 2026", config: map[string]any{"format": "Monday, 2 January 2006"}, want: "Friday, 23 October 2026"},
		{name: "contradicting weekday", value: "Monday, 23 October 2026", config: map[string]any{"format": "Monday, 2 January 2006"}, wantErr: "valid date"},
		{name: "no sign", value: "2d", wantErr: "valid date"},
		{name: "fraction", value: "+1.5d", wantErr: "valid date"},
		{name: "wrong unit", value: "+2h", wantErr: "valid date"},
		{name: "compound", value: "+2d-1d", wantErr: "valid date"},
		{name: "overflow", value: "+99999999999999999999999d", wantErr: "outside years"},
		{name: "out of range", value: "+3652058d", wantErr: "within years"},
		{name: "year zero", value: "0000-01-01", wantErr: "within years"},
		{name: "integer", value: 12345, wantErr: "must be a string"},
		{name: "nil", value: nil, wantErr: "must be a string"},
		{name: "yaml timestamp", value: time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC), wantErr: "must be a string"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			runner := dateRunner(t, test.config, "2026-10-23T12:00:00Z")
			result, err := runner.Run(t.Context(), step.Request{Vars: map[string]any{"due": test.value}, WorkflowTimezone: "UTC"})
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("error = %v, want %s", err, test.wantErr)
				}
				if len(result.Outputs) != 0 || len(result.Variables) != 0 {
					t.Fatalf("published invalid result: %v", result)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			want := step.Result{Outputs: map[string]any{"value": test.want}, Variables: map[string]any{"due": test.want}}
			if diff := cmp.Diff(want, result); diff != "" {
				t.Fatalf("result mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestDateCalendarArithmeticAndTimezone(t *testing.T) {
	t.Parallel()
	for _, test := range []struct{ reference, zone, value, want string }{
		{"2026-10-23T23:30:00Z", "Europe/Warsaw", "+0d", "2026-10-24"},
		{"2026-10-23T00:30:00Z", "America/New_York", "+0d", "2026-10-22"},
		{"2026-03-28T12:00:00Z", "Europe/Warsaw", "+2d", "2026-03-30"},
		{"2026-10-24T12:00:00Z", "Europe/Warsaw", "+2d", "2026-10-26"},
		{"2028-02-28T12:00:00Z", "UTC", "+1d", "2028-02-29"},
		{"2026-12-31T12:00:00Z", "UTC", "+2d", "2027-01-02"},
		{"2026-01-01T12:00:00Z", "UTC", "-7d", "2025-12-25"},
	} {
		t.Run(test.reference+"/"+test.zone, func(t *testing.T) {
			runner := dateRunner(t, nil, test.reference)
			result, err := runner.Run(t.Context(), step.Request{Vars: map[string]any{"due": test.value}, WorkflowTimezone: test.zone})
			if err != nil {
				t.Fatal(err)
			}
			if result.Outputs["value"] != test.want {
				t.Fatalf("value = %v, want %s", result.Outputs["value"], test.want)
			}
		})
	}
}

func TestDateFreezesClockAndRejectsInvalidRuntimeBounds(t *testing.T) {
	t.Parallel()
	runner := dateRunner(t, map[string]any{"min": "-7d", "max": "+2d"}, "2026-10-23T12:00:00Z")
	config, validate, err := runner.PreparePicker(runner.now(), time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	runner.now = func() time.Time { return time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC) }
	for range 2 {
		value, err := validate("+2d")
		if err != nil || value != "2026-10-25" {
			t.Fatalf("value = %v, error = %v", value, err)
		}
	}
	if config.Min.Format(defaultFormat) != "2026-10-16" || config.Max.Format(defaultFormat) != "2026-10-25" {
		t.Fatalf("bounds = %v..%v", config.Min, config.Max)
	}
	for _, bounds := range []map[string]any{{"min": "+2d", "max": "2026-10-24"}, {"max": "+3000000d"}} {
		runner := dateRunner(t, bounds, "2026-10-23T12:00:00Z")
		if _, _, err := runner.PreparePicker(runner.now(), time.UTC); err == nil {
			t.Fatalf("accepted bounds %v", bounds)
		}
	}
	if _, err := Location("Invalid/Timezone"); err == nil {
		t.Fatal("accepted invalid timezone")
	}
}

func TestDateInteractivePrefillAndCancellation(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name        string
		config      map[string]any
		input, want string
		cancelled   bool
	}{
		{name: "today", input: "\r", want: "2026-10-23"},
		{name: "relative prefill", config: map[string]any{"value": "+2d"}, input: "\r", want: "2026-10-25"},
		{name: "clamped today", config: map[string]any{"min": "+2d"}, input: "\r", want: "2026-10-25"},
		{name: "typed shortcut", input: "\t\x01\x0b-7d\r", want: "2026-10-16"},
		{name: "clear optional", config: map[string]any{"required": false}, input: "\x18\r", want: ""},
		{name: "cancel", input: "\x03", cancelled: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner := dateRunner(t, test.config, "2026-10-23T12:00:00Z")
			result, err := runner.Run(t.Context(), step.Request{Interactive: true, Stdin: strings.NewReader(test.input), Stdout: io.Discard, WorkflowTimezone: "UTC"})
			if test.cancelled {
				if !errors.Is(err, context.Canceled) || len(result.Outputs) != 0 {
					t.Fatalf("result = %v, error = %v", result, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if result.Outputs["value"] != test.want {
				t.Fatalf("value = %v, want %s", result.Outputs["value"], test.want)
			}
		})
	}
}

func TestDateUnattendedNeedsSuppliedValue(t *testing.T) {
	t.Parallel()
	runner := dateRunner(t, map[string]any{"value": "+2d", "required": false}, "2026-10-23T12:00:00Z")
	if _, err := runner.Run(t.Context(), step.Request{}); err == nil || !strings.Contains(err.Error(), "supply it with --var") {
		t.Fatalf("error = %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := runner.Run(ctx, step.Request{Vars: map[string]any{"due": "+0d"}}); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
}

func TestDateUnattendedAnswersWithValue(t *testing.T) {
	t.Parallel()
	runner := dateRunner(t, map[string]any{"value": "+2d", "max": "+30d", "noninteractive": "default"}, "2026-03-10T12:00:00Z")
	result, err := runner.Run(t.Context(), step.Request{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outputs["value"] != "2026-03-12" || result.Variables["due"] != "2026-03-12" {
		t.Fatalf("result = %#v", result)
	}
	outOfBounds := dateRunner(t, map[string]any{"value": "+40d", "max": "+30d"}, "2026-03-10T12:00:00Z")
	if _, err := outOfBounds.Run(t.Context(), step.Request{Prompts: step.PromptAnswers{Defaults: true}}); err == nil {
		t.Fatal("out-of-bounds default succeeded")
	}
	if _, err := NewWithClock(map[string]any{"variable": "due", "message": "Due", "noninteractive": "default"}, time.Now); err == nil {
		t.Fatal("noninteractive: default without value succeeded")
	}
}
