package form

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/up2jj/wuko/internal/steptest"
	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/tui"
)

func fixedFormClock(runner *Runner) {
	runner.now = func() time.Time { return time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC) }
}

func TestFormDatesResolveBindingsAndCustomValidation(t *testing.T) {
	t.Parallel()
	fields := []map[string]any{
		{"id": "start", "type": "date", "label": "Start", "value": map[string]any{"expr": "vars.start"}, "min": "+0d", "max": "+10d", "required": true},
		{"id": "end", "type": "date", "label": "End", "value": "+4d", "validation": map[string]any{"expr": "value > fields.start", "message": "End must follow start"}},
		{"id": "custom", "type": "date", "label": "Custom", "value": "+2d", "format": "02.01.2006", "min": "23.10.2026", "max": "+30d"},
		{"id": "optional", "type": "date", "label": "Optional", "value": ""},
		{"id": "lua", "type": "date", "label": "Lua", "value": "+2d", "validation": map[string]any{"lua": `function(value, context) return value == "2026-10-25" and context.fields.start == "2026-10-25" end`}},
	}
	runner := steptest.BuildAs[*Runner](t, New, configFor(fields...))
	fixedFormClock(runner)
	request := step.Request{Vars: map[string]any{"start": "+2d"}, WorkflowTimezone: "UTC"}
	result, err := runner.Run(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"start": "2026-10-25", "end": "2026-10-27", "custom": "25.10.2026", "optional": "", "lua": "2026-10-25"}
	if diff := cmp.Diff(want, result.Outputs); diff != "" {
		t.Fatalf("outputs mismatch (-want +got):\n%s", diff)
	}
	if len(result.Variables) != 0 {
		t.Fatal("form assigned variables")
	}
	if err := step.ValidateOutputKeys(outputSchema(configFor(fields...)), result.Outputs); err != nil {
		t.Fatal(err)
	}
	request.Vars["start"] = "+5d"
	result, err = runner.Run(t.Context(), request)
	if err == nil || !strings.Contains(err.Error(), "End must follow start") || len(result.Outputs) != 0 {
		t.Fatalf("result = %v, error = %v", result, err)
	}
}

func TestFormDateDefaultsAndClockStayFrozen(t *testing.T) {
	t.Parallel()
	runner := steptest.BuildAs[*Runner](t, New, configFor(
		map[string]any{"id": "first", "type": "date", "label": "First", "default": "+2d", "min": "-7d", "max": "+30d"},
		map[string]any{"id": "second", "type": "date", "label": "Second", "default": map[string]any{"expr": "vars.suggestion"}},
		map[string]any{"id": "empty", "type": "date", "label": "Empty"},
	))
	calls := 0
	runner.now = func() time.Time {
		calls++
		return time.Date(2026, 10, 23+calls-1, 23, 30, 0, 0, time.UTC)
	}
	runner.read = func(_ context.Context, _ step.Request, config tui.FormConfig) (map[string]any, error) {
		values := map[string]any{}
		for _, field := range config.Fields {
			if field.Date == nil || field.Date.Today.Format("2006-01-02") != "2026-10-24" {
				t.Fatal("fields did not share reference date")
			}
			values[field.ID] = field.Value
		}
		if values["first"] != "2026-10-26" || values["second"] != "2026-10-26" || values["empty"] != "" {
			t.Fatalf("initial values = %v", values)
		}
		parsed, err := config.Fields[0].Date.Parse("+2d")
		if err != nil || parsed.Format("2006-01-02") != "2026-10-26" {
			t.Fatalf("parsed = %v, error = %v", parsed, err)
		}
		return values, nil
	}
	result, err := runner.Run(t.Context(), step.Request{Interactive: true, WorkflowTimezone: "Europe/Warsaw", Vars: map[string]any{"suggestion": "+2d"}})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || result.Outputs["first"] != "2026-10-26" {
		t.Fatalf("clock calls = %d, result = %v", calls, result)
	}
}

func TestFormDatesInvalidDraftsAndAtomicSubmission(t *testing.T) {
	t.Parallel()
	runner := steptest.BuildAs[*Runner](t, New, configFor(
		map[string]any{"id": "due", "type": "date", "label": "Due", "default": "2026-02-30"},
		map[string]any{"id": "name", "type": "text", "label": "Name", "default": "release"},
	))
	fixedFormClock(runner)
	runner.read = func(_ context.Context, _ step.Request, config tui.FormConfig) (map[string]any, error) {
		values := map[string]any{"due": config.Fields[0].Value, "name": "release"}
		if values["due"] != "2026-02-30" {
			t.Fatal("invalid initial date must stay editable")
		}
		if draft := tui.FormDraft(config.Fields, values); draft["due"] != nil || draft["name"] != "release" {
			t.Fatalf("draft = %v", draft)
		}
		return values, nil
	}
	result, err := runner.Run(t.Context(), step.Request{Interactive: true})
	if err == nil || len(result.Outputs) != 0 {
		t.Fatalf("result = %v, error = %v", result, err)
	}
}

func TestFormDateConfigurationAndUnattendedRules(t *testing.T) {
	t.Parallel()
	for _, extra := range []map[string]any{
		{"format": "06-01-02"}, {"min": "invalid"}, {"max": ""},
		{"validation": map[string]any{"min_length": 2}}, {"multiple": true},
	} {
		field := fieldFor("date", "2026-10-23")
		for key, value := range extra {
			field[key] = value
		}
		if _, err := New(configFor(field)); err == nil {
			t.Fatalf("accepted %v", field)
		}
	}
	for _, test := range []struct {
		field map[string]any
		want  string
	}{
		{map[string]any{"id": "due", "type": "date", "label": "Due", "default": "+2d"}, "explicit value"},
		{fieldFor("date", 42), "invalid type"},
		{map[string]any{"id": "due", "type": "date", "label": "Due", "value": "", "required": true}, "date is required"},
		{map[string]any{"id": "due", "type": "date", "label": "Due", "value": "+2d", "max": "+1d"}, "on or before"},
	} {
		runner := steptest.BuildAs[*Runner](t, New, configFor(test.field))
		fixedFormClock(runner)
		result, err := runner.Run(t.Context(), step.Request{})
		if err == nil || !strings.Contains(err.Error(), test.want) || len(result.Outputs) != 0 {
			t.Fatalf("result = %v, error = %v, want %s", result, err, test.want)
		}
	}
}

func TestFormDateDraftPreservesParseableOutOfBoundsDates(t *testing.T) {
	t.Parallel()
	runner := steptest.BuildAs[*Runner](t, New, configFor(map[string]any{"id": "due", "type": "date", "label": "Due", "value": "+31d", "max": "+30d"}))
	fixedFormClock(runner)
	runner.read = func(_ context.Context, _ step.Request, config tui.FormConfig) (map[string]any, error) {
		values := map[string]any{"due": config.Fields[0].Value}
		if draft := tui.FormDraft(config.Fields, values); draft["due"] != "2026-11-23" {
			t.Fatalf("draft = %v", draft)
		}
		return values, nil
	}
	result, err := runner.Run(t.Context(), step.Request{Interactive: true, WorkflowTimezone: "UTC"})
	if err == nil || !strings.Contains(err.Error(), "on or before") || len(result.Outputs) != 0 {
		t.Fatalf("result = %v, error = %v", result, err)
	}
}
