package schedule

import (
	"context"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

func TestParse(t *testing.T) {
	tests := []struct {
		name       string
		expression string
		timezone   string
		wantError  string
	}{
		{name: "five fields", expression: "0 9 * * *"},
		{name: "seconds", expression: "30 0 9 * * *"},
		{name: "named timezone", expression: "0 9 * * *", timezone: "Europe/Warsaw"},
		{name: "empty", expression: " ", wantError: "must not be empty"},
		{name: "too few fields", expression: "0 9 * *", wantError: "five fields or six fields"},
		{name: "too many fields", expression: "0 0 9 * * * *", wantError: "five fields or six fields"},
		{name: "descriptor", expression: "@daily", wantError: "descriptors are not supported"},
		{name: "embedded timezone", expression: "CRON_TZ=UTC 0 9 * * *", wantError: "workflow timezone field"},
		{name: "bad expression", expression: "0 25 * * *", wantError: "invalid cron"},
		{name: "impossible expression", expression: "0 0 30 2 *", wantError: "no future occurrence"},
		{name: "bad timezone", expression: "0 9 * * *", timezone: "Mars/Olympus", wantError: "invalid timezone"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			schedule, err := Parse(tt.expression, tt.timezone)
			if tt.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantError) {
					t.Fatalf("Parse() error = %v, want %q", err, tt.wantError)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if schedule.Expression() != tt.expression {
				t.Fatalf("Expression() = %q, want %q", schedule.Expression(), tt.expression)
			}
		})
	}
}

func TestNextTreatsCurrentMatchingBucketAsDue(t *testing.T) {
	tests := []struct {
		name       string
		expression string
		now        time.Time
		want       time.Time
	}{
		{
			name: "current minute", expression: "15 10 * * *",
			now:  time.Date(2026, time.August, 21, 10, 15, 42, 0, time.UTC),
			want: time.Date(2026, time.August, 21, 10, 15, 42, 0, time.UTC),
		},
		{
			name: "current second", expression: "42 15 10 * * *",
			now:  time.Date(2026, time.August, 21, 10, 15, 42, 500, time.UTC),
			want: time.Date(2026, time.August, 21, 10, 15, 42, 500, time.UTC),
		},
		{
			name: "next second", expression: "43 15 10 * * *",
			now:  time.Date(2026, time.August, 21, 10, 15, 42, 0, time.UTC),
			want: time.Date(2026, time.August, 21, 10, 15, 43, 0, time.UTC),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			schedule, err := Parse(tt.expression, "UTC")
			if err != nil {
				t.Fatal(err)
			}
			if got := schedule.Next(tt.now); !got.Equal(tt.want) {
				t.Fatalf("Next() = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestNextUsesConfiguredTimezoneAndHandlesDST(t *testing.T) {
	schedule, err := Parse("30 2 * * *", "Europe/Warsaw")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.March, 28, 3, 0, 0, 0, time.UTC)
	want := time.Date(2026, time.March, 30, 0, 30, 0, 0, time.UTC)
	if got := schedule.Next(now); !got.Equal(want) {
		t.Fatalf("Next() = %s, want %s", got, want)
	}
}

func TestWaitCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if err := Wait(ctx, time.Now().Add(time.Hour)); err != context.Canceled {
			t.Fatalf("Wait() error = %v, want context.Canceled", err)
		}
	})
}
