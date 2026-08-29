package expression

import (
	"strings"
	"testing"
	"time"
)

func TestPureTimeHelpersDoNotAdoptMachineLocalTimezone(t *testing.T) {
	location, err := time.LoadLocation("Europe/Warsaw")
	if err != nil {
		t.Fatal(err)
	}
	previous := time.Local
	time.Local = location
	t.Cleanup(func() { time.Local = previous })

	got, err := AddTime("2026-03-28T12:00:00+01:00", map[string]any{"days": 1})
	if err != nil {
		t.Fatal(err)
	}
	if got != "2026-03-29T12:00:00+01:00" {
		t.Fatalf("time = %q", got)
	}
}

func TestTimeHelpersAcrossTemplateAndExpr(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		template   string
		expression string
		want       string
	}{
		{
			name:       "parse in location",
			template:   `{{ "2026-03-29 01:30" | parseTime "2006-01-02 15:04" "Europe/Warsaw" }}`,
			expression: `parseTime("2026-03-29 01:30", "2006-01-02 15:04", "Europe/Warsaw")`,
			want:       "2026-03-29T01:30:00+01:00",
		},
		{
			name:       "calendar day across DST",
			template:   `{{ "2026-03-28T12:00:00+01:00" | addTime (dict "days" 1 "timezone" "Europe/Warsaw") }}`,
			expression: `addTime("2026-03-28T12:00:00+01:00", {"days": 1, "timezone": "Europe/Warsaw"})`,
			want:       "2026-03-29T12:00:00+02:00",
		},
		{
			name:       "format in location",
			template:   `{{ "2026-08-29T10:00:00Z" | formatTime "2006-01-02 15:04" "Europe/Warsaw" }}`,
			expression: `formatTime("2026-08-29T10:00:00Z", "2006-01-02 15:04", "Europe/Warsaw")`,
			want:       "2026-08-29 12:00",
		},
		{
			name:       "computed zero adjustment returns the same instant",
			template:   `{{ "2026-03-28T12:00:00+01:00" | addTime (dict "days" 0 "timezone" "Europe/Warsaw") }}`,
			expression: `addTime("2026-03-28T12:00:00+01:00", {"days": 0, "timezone": "Europe/Warsaw"})`,
			want:       "2026-03-28T12:00:00+01:00",
		},
		{
			name:       "blank adjustment timezone keeps the instant's own zone",
			template:   `{{ "2026-03-28T12:00:00+01:00" | addTime (dict "days" 1 "timezone" "") }}`,
			expression: `addTime("2026-03-28T12:00:00+01:00", {"days": 1, "timezone": ""})`,
			want:       "2026-03-29T12:00:00+01:00",
		},
		{
			name:       "optional timezone",
			template:   `{{ "2026-08-29T10:00:00Z" | formatTime "2006-01-02" }}`,
			expression: `formatTime("2026-08-29T10:00:00Z", "2006-01-02")`,
			want:       "2026-08-29",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotTemplate, err := renderTemplate(tt.template)
			if err != nil {
				t.Fatalf("template: %v", err)
			}
			gotExpr, err := Eval(tt.expression, map[string]any{})
			if err != nil {
				t.Fatalf("Expr: %v", err)
			}
			if gotTemplate != tt.want || gotExpr != tt.want {
				t.Fatalf("template = %q, Expr = %#v, want %q", gotTemplate, gotExpr, tt.want)
			}
		})
	}
}

func TestTimeHelpersValidateInputs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		run  func() error
		want string
	}{
		{name: "blank value", run: func() error { _, err := ParseTime("", DefaultTimeLayout, ""); return err }, want: "must not be blank"},
		{name: "parse mismatch", run: func() error { _, err := ParseTime("not-a-date", DefaultTimeLayout, ""); return err }, want: "parsing time"},
		{name: "invalid timezone", run: func() error { _, err := FormatTime("2026-01-01T00:00:00Z", "2006", "Mars/Olympus"); return err }, want: "invalid timezone"},
		{name: "unknown adjustment", run: func() error { _, err := AddTime("2026-01-01T00:00:00Z", map[string]any{"weeks": 1}); return err }, want: "unknown time adjustment"},
		{name: "fractional calendar value", run: func() error { _, err := AddTime("2026-01-01T00:00:00Z", map[string]any{"days": 1.5}); return err }, want: "must be an integer"},
		{name: "no adjustment", run: func() error { _, err := AddTime("2026-01-01T00:00:00Z", map[string]any{"timezone": "UTC"}); return err }, want: "must include"},
		{name: "bad duration", run: func() error {
			_, err := AddTime("2026-01-01T00:00:00Z", map[string]any{"duration": "tomorrow"})
			return err
		}, want: "invalid duration"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.run()
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestEvaluatorsExposeNoClockFunction(t *testing.T) {
	t.Parallel()
	if _, exists := TemplateFuncs()["now"]; exists {
		t.Fatal("templates expose a clock function")
	}
	if _, err := Eval(`now()`, map[string]any{}); err == nil {
		t.Fatal("Expr exposes a clock function")
	}
}
