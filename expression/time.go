package expression

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"strings"
	"time"
)

const DefaultTimeLayout = time.RFC3339Nano

// ParseTime parses value with a Go time layout and returns a canonical RFC3339Nano string.
// A missing timezone uses UTC. When value carries its own offset, that instant is preserved
// and converted to timezone when one was explicitly requested.
func ParseTime(value, layout, timezone string) (string, error) {
	if strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("time value must not be blank")
	}
	if strings.TrimSpace(layout) == "" {
		return "", fmt.Errorf("time layout must not be blank")
	}
	location, err := loadTimeLocation(timezone, time.UTC)
	if err != nil {
		return "", err
	}
	parsed, err := time.ParseInLocation(layout, value, location)
	if err != nil {
		return "", fmt.Errorf("parsing time %q with layout %q: %w", value, layout, err)
	}
	if timezone != "" {
		parsed = parsed.In(location)
	}
	return parsed.Format(DefaultTimeLayout), nil
}

// AddTime applies calendar and exact-duration adjustments to an RFC3339 timestamp.
// Calendar arithmetic runs in the optional IANA timezone; otherwise the timestamp's
// encoded offset is retained. At least one adjustment field must be present, but a
// computed adjustment may be zero: that returns the same instant rather than an error.
func AddTime(value string, adjustments map[string]any) (string, error) {
	instant, err := parseCanonicalTime(value)
	if err != nil {
		return "", err
	}
	if adjustments == nil {
		return "", fmt.Errorf("time adjustments must be an object")
	}

	var years, months, days int
	var duration time.Duration
	var timezone string
	hasAdjustment := false
	for name, raw := range adjustments {
		switch name {
		case "years":
			years, err = timeAdjustmentInt(name, raw)
			hasAdjustment = true
		case "months":
			months, err = timeAdjustmentInt(name, raw)
			hasAdjustment = true
		case "days":
			days, err = timeAdjustmentInt(name, raw)
			hasAdjustment = true
		case "duration":
			text, ok := raw.(string)
			if !ok {
				return "", fmt.Errorf("time adjustment %q must be a string, got %T", name, raw)
			}
			duration, err = time.ParseDuration(text)
			hasAdjustment = true
		case "timezone":
			timezone, err = timeZoneOption(name, raw)
		default:
			return "", fmt.Errorf("unknown time adjustment %q", name)
		}
		if err != nil {
			return "", err
		}
	}
	if !hasAdjustment {
		return "", fmt.Errorf("time adjustments must include years, months, days, or duration")
	}

	location, err := loadTimeLocation(timezone, instant.Location())
	if err != nil {
		return "", err
	}
	adjusted := instant.In(location).AddDate(years, months, days).Add(duration)
	return adjusted.Format(DefaultTimeLayout), nil
}

// FormatTime formats an RFC3339 timestamp with a Go time layout in an optional IANA timezone.
func FormatTime(value, layout, timezone string) (string, error) {
	instant, err := parseCanonicalTime(value)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(layout) == "" {
		return "", fmt.Errorf("time layout must not be blank")
	}
	location, err := loadTimeLocation(timezone, instant.Location())
	if err != nil {
		return "", err
	}
	return instant.In(location).Format(layout), nil
}

func parseCanonicalTime(value string) (time.Time, error) {
	if strings.TrimSpace(value) == "" {
		return time.Time{}, fmt.Errorf("time value must not be blank")
	}
	// ParseInLocation with UTC prevents time.Parse's RFC3339 fast path from adopting
	// time.Local when the encoded offset happens to match the host zone.
	instant, err := time.ParseInLocation(DefaultTimeLayout, value, time.UTC)
	if err != nil {
		return time.Time{}, fmt.Errorf("parsing RFC3339 time %q: %w", value, err)
	}
	return instant, nil
}

func loadTimeLocation(name string, fallback *time.Location) (*time.Location, error) {
	if name == "" {
		return fallback, nil
	}
	if strings.TrimSpace(name) != name {
		return nil, fmt.Errorf("invalid timezone %q", name)
	}
	location, err := time.LoadLocation(name)
	if err != nil {
		return nil, fmt.Errorf("invalid timezone %q: %w", name, err)
	}
	return location, nil
}

func timeAdjustmentInt(name string, value any) (int, error) {
	var number int64
	switch typed := value.(type) {
	case int:
		return typed, nil
	case int8:
		number = int64(typed)
	case int16:
		number = int64(typed)
	case int32:
		number = int64(typed)
	case int64:
		number = typed
	case uint:
		if uint64(typed) > math.MaxInt64 {
			return 0, fmt.Errorf("time adjustment %q is out of range", name)
		}
		number = int64(typed)
	case uint8:
		number = int64(typed)
	case uint16:
		number = int64(typed)
	case uint32:
		number = int64(typed)
	case uint64:
		if typed > math.MaxInt64 {
			return 0, fmt.Errorf("time adjustment %q is out of range", name)
		}
		number = int64(typed)
	case float64:
		if math.Trunc(typed) != typed || typed < math.MinInt64 || typed > math.MaxInt64 {
			return 0, fmt.Errorf("time adjustment %q must be an integer", name)
		}
		number = int64(typed)
	case json.Number:
		parsed, err := typed.Int64()
		if err != nil {
			return 0, fmt.Errorf("time adjustment %q must be an integer", name)
		}
		number = parsed
	default:
		kind := reflect.TypeOf(value)
		if kind == nil {
			return 0, fmt.Errorf("time adjustment %q must be an integer, got <nil>", name)
		}
		return 0, fmt.Errorf("time adjustment %q must be an integer, got %s", name, kind)
	}
	if int64(int(number)) != number {
		return 0, fmt.Errorf("time adjustment %q is out of range", name)
	}
	return int(number), nil
}

// timeZoneOption reads the optional timezone adjustment. A blank value means "keep the
// instant's own zone", matching ParseTime and FormatTime, so callers can pass
// .workflow.timezone straight through when no workflow timezone is declared.
func timeZoneOption(name string, value any) (string, error) {
	text, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("time adjustment %q must be a string, got %T", name, value)
	}
	return text, nil
}
