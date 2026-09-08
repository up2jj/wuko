package expression

import (
	"strings"
	"testing"
	"time"
)

func TestNaturalTimeEnglishPhrases(t *testing.T) {
	t.Parallel()
	reference := "2026-09-08T10:15:42.123456789+02:00"
	tests := []struct {
		name   string
		phrase string
		want   string
	}{
		{name: "next weekday", phrase: "next monday", want: "2026-09-14T10:15:42.123456789+02:00"},
		{name: "future words", phrase: "in two weeks", want: "2026-09-22T10:15:42.123456789+02:00"},
		{name: "past words", phrase: "two weeks ago", want: "2026-08-25T10:15:42.123456789+02:00"},
		{name: "tomorrow", phrase: "tomorrow", want: "2026-09-09T10:15:42.123456789+02:00"},
		{name: "yesterday", phrase: "yesterday", want: "2026-09-07T10:15:42.123456789+02:00"},
		{name: "named date", phrase: "September 14", want: "2026-09-14T10:15:42.123456789+02:00"},
		{name: "date and 24 hour clock", phrase: "tomorrow at 9:30", want: "2026-09-09T09:30:00+02:00"},
		{name: "date and 12 hour clock", phrase: "next monday at 4pm", want: "2026-09-14T16:00:00+02:00"},
		{name: "casual clock", phrase: "tomorrow morning", want: "2026-09-09T08:00:00+02:00"},
		{name: "dotted clock", phrase: "next monday at 4 p.m.", want: "2026-09-14T16:00:00+02:00"},
		{name: "case insensitive", phrase: "NEXT MONDAY", want: "2026-09-14T10:15:42.123456789+02:00"},
		{name: "outer whitespace", phrase: "  in two weeks\n", want: "2026-09-22T10:15:42.123456789+02:00"},
		{name: "collapsed inner whitespace", phrase: "next  monday", want: "2026-09-14T10:15:42.123456789+02:00"},
		// The English rule set accepts unspaced spellings, so the calendar correction must too:
		// without it the library's modulo-12 month arithmetic would answer 2026-01-08.
		{name: "unspaced future months", phrase: "in4months", want: "2027-01-08T10:15:42.123456789+02:00"},
		{name: "unspaced weekday", phrase: "nextmonday", want: "2026-09-14T10:15:42.123456789+02:00"},
		{name: "unspaced few", phrase: "in afew days", want: "2026-09-11T10:15:42.123456789+02:00"},
		// A clock phrase owns the whole time of day, so the reference minute must not survive it.
		// The "last night" rule pins only the hour, and ignores the unspaced spelling entirely.
		{name: "tonight", phrase: "tonight", want: "2026-09-08T23:00:00+02:00"},
		{name: "last night", phrase: "last night", want: "2026-09-07T23:00:00+02:00"},
		{name: "unspaced last night", phrase: "lastnight", want: "2026-09-07T23:00:00+02:00"},
		{name: "collapsed last night", phrase: "last  night", want: "2026-09-07T23:00:00+02:00"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := ParseNaturalTime(test.phrase, map[string]any{"reference": reference, "language": "en"})
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("time = %q, want %q", got, test.want)
			}
		})
	}
}

func TestNaturalTimeWeekdayIsStrictlyFuture(t *testing.T) {
	t.Parallel()
	got, err := ParseNaturalTime("next monday", map[string]any{"reference": "2026-09-14T10:15:00Z"})
	if err != nil {
		t.Fatal(err)
	}
	if got != "2026-09-21T10:15:00Z" {
		t.Fatalf("time = %q", got)
	}
}

func TestNaturalTimeCalendarArithmeticAcrossDST(t *testing.T) {
	t.Parallel()
	options := map[string]any{
		"reference": "2026-03-23T12:00:00+01:00",
		"timezone":  "Europe/Warsaw",
	}
	tests := []struct {
		phrase string
		want   string
	}{
		{phrase: "next monday", want: "2026-03-30T12:00:00+02:00"},
		{phrase: "nextmonday", want: "2026-03-30T12:00:00+02:00"},
		{phrase: "in1 week", want: "2026-03-30T12:00:00+02:00"},
		{phrase: "in two weeks", want: "2026-04-06T12:00:00+02:00"},
		{phrase: "in one month", want: "2026-04-23T12:00:00+02:00"},
	}
	for _, test := range tests {
		t.Run(test.phrase, func(t *testing.T) {
			got, err := ParseNaturalTime(test.phrase, options)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("time = %q, want %q", got, test.want)
			}
		})
	}
}

func TestNaturalTimeSubdayDurationsAreExact(t *testing.T) {
	t.Parallel()
	got, err := ParseNaturalTime("in two hours", map[string]any{
		"reference": "2026-03-29T01:30:00+01:00",
		"timezone":  "Europe/Warsaw",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != "2026-03-29T04:30:00+02:00" {
		t.Fatalf("time = %q", got)
	}
}

func TestNaturalTimeUsesClockWhenReferenceIsOmitted(t *testing.T) {
	t.Parallel()
	fixed := func() time.Time {
		return time.Date(2026, time.September, 8, 8, 15, 0, 0, time.UTC)
	}
	got, err := parseNaturalTime("tomorrow", map[string]any{"timezone": "Europe/Warsaw"}, fixed)
	if err != nil {
		t.Fatal(err)
	}
	if got != "2026-09-09T10:15:00+02:00" {
		t.Fatalf("time = %q", got)
	}
}

func TestNaturalTimeAcrossTemplateAndExpr(t *testing.T) {
	t.Parallel()
	templateSource := `{{ "in two weeks" | parseNaturalTime (dict "reference" "2026-09-08T10:15:00+02:00" "timezone" "Europe/Warsaw") }}`
	gotTemplate, err := renderTemplate(templateSource)
	if err != nil {
		t.Fatal(err)
	}
	gotExpr, err := Eval(`parseNaturalTime("in two weeks", {"reference": "2026-09-08T10:15:00+02:00", "timezone": "Europe/Warsaw"})`, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	const want = "2026-09-22T10:15:00+02:00"
	if gotTemplate != want || gotExpr != want {
		t.Fatalf("template = %q, Expr = %#v, want %q", gotTemplate, gotExpr, want)
	}
}

func TestNaturalTimeRejectsInvalidInput(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		value   string
		options map[string]any
		want    string
	}{
		{name: "blank", value: " ", want: "must not be blank"},
		{name: "unrecognized", value: "someday", want: "complete recognized phrase"},
		{name: "surrounding prose", value: "deploy next monday", want: "complete recognized phrase"},
		{name: "trailing punctuation", value: "next monday.", want: "complete recognized phrase"},
		{name: "unsupported language", value: "tomorrow", options: map[string]any{"language": "pl"}, want: `unsupported natural time language "pl"`},
		{name: "blank language", value: "tomorrow", options: map[string]any{"language": ""}, want: "invalid natural time language"},
		{name: "bad language type", value: "tomorrow", options: map[string]any{"language": 1}, want: `option "language" must be a string`},
		{name: "blank reference", value: "tomorrow", options: map[string]any{"reference": ""}, want: "reference must not be blank"},
		{name: "bad reference", value: "tomorrow", options: map[string]any{"reference": "yesterday"}, want: "invalid natural time reference"},
		{name: "bad timezone", value: "tomorrow", options: map[string]any{"timezone": "Mars/Olympus"}, want: "invalid timezone"},
		{name: "unknown option", value: "tomorrow", options: map[string]any{"locale": "en"}, want: `unknown natural time option "locale"`},
		{name: "oversized", value: strings.Repeat("x", maxNaturalTimePhraseBytes+1), want: "exceeds 1024 bytes"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := ParseNaturalTime(test.value, test.options)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}
