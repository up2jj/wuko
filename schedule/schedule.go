// Package schedule parses workflow cron expressions and calculates activation times.
package schedule

import (
	"context"
	"fmt"
	"strings"
	"time"

	cron "github.com/robfig/cron/v3"
)

var parser = cron.NewParser(
	cron.SecondOptional | cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow,
)

// Schedule is one validated cron schedule with a fixed timezone and field granularity.
type Schedule struct {
	expression  string
	location    *time.Location
	granularity time.Duration
	parsed      cron.Schedule
}

// Parse validates a five-field cron expression or a six-field expression with seconds.
// timezone must be an IANA timezone name when supplied and otherwise defaults to time.Local.
func Parse(expression, timezone string) (*Schedule, error) {
	expression = strings.TrimSpace(expression)
	if expression == "" {
		return nil, fmt.Errorf("cron must not be empty")
	}
	if strings.HasPrefix(expression, "@") {
		return nil, fmt.Errorf("cron descriptors are not supported")
	}
	if strings.HasPrefix(expression, "TZ=") || strings.HasPrefix(expression, "CRON_TZ=") {
		return nil, fmt.Errorf("cron must use the workflow timezone field instead of an embedded timezone")
	}
	fields := strings.Fields(expression)
	if len(fields) != 5 && len(fields) != 6 {
		return nil, fmt.Errorf("cron must contain five fields or six fields including seconds")
	}

	location := time.Local
	parseExpression := expression
	if timezone != "" {
		var err error
		location, err = time.LoadLocation(timezone)
		if err != nil {
			return nil, fmt.Errorf("invalid timezone %q: %w", timezone, err)
		}
		parseExpression = "CRON_TZ=" + timezone + " " + expression
	}
	parsed, err := parser.Parse(parseExpression)
	if err != nil {
		return nil, fmt.Errorf("invalid cron %q: %w", expression, err)
	}
	if parsed.Next(time.Now()).IsZero() {
		return nil, fmt.Errorf("invalid cron %q: schedule has no future occurrence", expression)
	}

	granularity := time.Minute
	if len(fields) == 6 {
		granularity = time.Second
	}
	return &Schedule{
		expression: expression, location: location, granularity: granularity, parsed: parsed,
	}, nil
}

// Expression returns the normalized cron expression.
func (schedule *Schedule) Expression() string { return schedule.expression }

// Location returns the timezone used to interpret the expression.
func (schedule *Schedule) Location() *time.Location { return schedule.location }

// Matches reports whether now falls in an activation minute or second.
func (schedule *Schedule) Matches(now time.Time) bool {
	bucket := now.In(schedule.location).Truncate(schedule.granularity)
	next := schedule.parsed.Next(bucket.Add(-time.Nanosecond))
	return next.Equal(bucket)
}

// Next returns now when its current minute or second matches, otherwise the next future activation.
func (schedule *Schedule) Next(now time.Time) time.Time {
	if schedule.Matches(now) {
		return now
	}
	return schedule.NextAfter(now)
}

// NextAfter returns the next activation strictly later than now.
func (schedule *Schedule) NextAfter(now time.Time) time.Time { return schedule.parsed.Next(now) }

// Wait blocks until instant or context cancellation.
func Wait(ctx context.Context, instant time.Time) error {
	delay := time.Until(instant)
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
