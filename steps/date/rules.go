package date

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/up2jj/wuko/tui"
)

const defaultFormat = "2006-01-02"

var relativeDate = regexp.MustCompile(`^[+-][0-9]+d$`)

// validateFormat checks Go's layout semantics rather than maintaining a second
// lexer for its date tokens. A full date must round-trip across centuries, and
// changing the clock and zone must not change the rendered calendar date.
func validateFormat(format string) error {
	if format == "" || strings.TrimSpace(format) != format || strings.ContainsAny(format, "\r\n\t") {
		return fmt.Errorf("format must be a nonblank, single-line Go date layout without surrounding whitespace")
	}
	for _, date := range []time.Time{
		time.Date(1, 1, 2, 0, 0, 0, 0, time.UTC),
		time.Date(1969, 2, 28, 0, 0, 0, 0, time.UTC),
		time.Date(2000, 2, 29, 0, 0, 0, 0, time.UTC),
		time.Date(2069, 11, 23, 0, 0, 0, 0, time.UTC),
		time.Date(9999, 12, 31, 0, 0, 0, 0, time.UTC),
	} {
		first := time.Date(date.Year(), date.Month(), date.Day(), 1, 2, 3, 123456789, time.FixedZone("First", 7200))
		second := time.Date(date.Year(), date.Month(), date.Day(), 13, 45, 56, 987654321, time.FixedZone("Second", -18000))
		parsed, err := time.ParseInLocation(format, date.Format(format), time.UTC)
		if err != nil || !parsed.Equal(date) || first.Format(format) != second.Format(format) || relativeDate.MatchString(date.Format(format)) {
			return fmt.Errorf("format must be a full Go date layout with a four-digit year and no time or timezone, e.g. 2006-01-02")
		}
	}
	return nil
}

func dateText(value any) (string, error) {
	text, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("must be a string; quote YAML dates and JSON values passed with --var")
	}
	return strings.TrimSpace(text), nil
}

// Normalize resolves a date or day shortcut against an already captured today.
// It preserves empty strings and does not apply required or bound constraints,
// so other form validators can inspect valid dates while a content rule fails.
func Normalize(value any, format string, today time.Time) (string, error) {
	text, err := dateText(value)
	if err != nil || text == "" {
		return "", err
	}
	parsed, err := parseDate(text, format, today)
	if err != nil {
		return "", err
	}
	return parsed.Format(format), nil
}

func relativeDays(value string) (int, error) {
	days, err := strconv.ParseInt(value[:len(value)-1], 10, 32)
	// This is the maximum span of the supported years 0001 through 9999.
	if err != nil || days < -3652058 || days > 3652058 {
		return 0, fmt.Errorf("relative date is outside years 0001 through 9999")
	}
	return int(days), nil
}

func parseDate(value, format string, today time.Time) (time.Time, error) {
	value = strings.TrimSpace(value)
	var parsed time.Time
	if relativeDate.MatchString(value) {
		days, err := relativeDays(value)
		if err != nil {
			return time.Time{}, err
		}
		parsed = today.AddDate(0, 0, days)
	} else {
		var err error
		parsed, err = time.ParseInLocation(format, value, time.UTC)
		// Go checks a weekday's spelling but ignores its value, so a layout with
		// one must round-trip to reject a weekday that contradicts the date.
		if err == nil && strings.Contains(format, "Mon") && !strings.EqualFold(parsed.Format(format), value) {
			err = fmt.Errorf("weekday does not match date")
		}
		if err != nil {
			return time.Time{}, fmt.Errorf("enter a valid date using %s or a shortcut such as +2d or -7d", format)
		}
	}
	if parsed.Year() < 1 || parsed.Year() > 9999 {
		return time.Time{}, fmt.Errorf("date must be within years 0001 through 9999")
	}
	return tui.CalendarDate(parsed), nil
}

// checkStaticBounds validates untemplated bounds at build time. Each bound's
// syntax is checked, and min > max is rejected when both bounds are absolute or
// both are relative, since only mixed pairs depend on the reference date.
func checkStaticBounds(config Config) error {
	var minimum, maximum time.Time
	var minDays, maxDays int
	for _, bound := range []struct {
		name, value string
		date        *time.Time
		days        *int
	}{{"min", config.Min, &minimum, &minDays}, {"max", config.Max, &maximum, &maxDays}} {
		value := strings.TrimSpace(bound.value)
		var err error
		switch {
		case value == "":
		case relativeDate.MatchString(value):
			*bound.days, err = relativeDays(value)
		default:
			*bound.date, err = parseDate(value, config.Format, time.Time{})
		}
		if err != nil {
			return fmt.Errorf("%s: %w", bound.name, err)
		}
	}
	minRelative, maxRelative := relativeDate.MatchString(strings.TrimSpace(config.Min)), relativeDate.MatchString(strings.TrimSpace(config.Max))
	if minRelative && maxRelative && minDays > maxDays || !minimum.IsZero() && !maximum.IsZero() && minimum.After(maximum) {
		return fmt.Errorf("min cannot exceed max")
	}
	return nil
}

func validateBounds(config Config, today time.Time) (time.Time, time.Time, error) {
	var minimum, maximum time.Time
	for _, bound := range []struct {
		name, value string
		target      *time.Time
	}{{"min", config.Min, &minimum}, {"max", config.Max, &maximum}} {
		if bound.value == "" {
			continue
		}
		value, err := parseDate(bound.value, config.Format, today)
		if err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("%s: %w", bound.name, err)
		}
		*bound.target = value
	}
	if !minimum.IsZero() && !maximum.IsZero() && minimum.After(maximum) {
		return time.Time{}, time.Time{}, fmt.Errorf("min cannot exceed max")
	}
	return minimum, maximum, nil
}
