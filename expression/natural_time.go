package expression

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/olebedev/when"
	"github.com/olebedev/when/rules"
	"github.com/olebedev/when/rules/common"
	"github.com/olebedev/when/rules/en"
)

// maxNaturalTimePhraseBytes bounds the work a single call can ask of the rule regexps. A natural
// date phrase is a handful of words; scanning megabytes of unrelated text only to reject it burns
// seconds of CPU per evaluation.
const maxNaturalTimePhraseBytes = 1 << 10

// englishLastNightHour is the hour the English rule set gives "last night".
const englishLastNightHour = 23

type naturalTimeLanguage struct {
	parser         *when.Parser
	calendarTarget func(string, time.Time) (time.Time, bool, error)
	clock          func(string, time.Time) (time.Time, bool)
}

var naturalTimeLanguages = map[string]naturalTimeLanguage{
	"en": newEnglishNaturalTimeLanguage(),
}

// The English rule set in github.com/olebedev/when separates its tokens with `\s*`, so it also
// recognizes unspaced spellings such as "in4months" and "nextmonday". These patterns mirror that
// looseness: a phrase the library accepts but these patterns miss would silently skip the calendar
// correction below and fall back to the library's exact-duration and modulo-12 month arithmetic.
var (
	englishFutureCalendarPattern = regexp.MustCompile(`(?i)\b(?:within|in)\s*(one|two|three|four|five|six|seven|eight|nine|ten|eleven|twelve|[0-9]+|an?(?:\s*few)?)\s*(days?|weeks?|months?|years?)\b`)
	englishPastCalendarPattern   = regexp.MustCompile(`(?i)\b(one|two|three|four|five|six|seven|eight|nine|ten|eleven|twelve|[0-9]+|an?(?:\s*few)?)\s*(days?|weeks?|months?|years?)\s+ago\b`)
	englishWeekdayPattern        = regexp.MustCompile(`(?i)\b(?:(this|last|past|next)\s*)?(sunday|sun|monday|mon|tuesday|tue|wednesday|wed|thursday|thur|thu|friday|fri|saturday|sat)(?:\s*(this|last|past|next)\s*week)?\b`)
	englishCasualDatePattern     = regexp.MustCompile(`(?i)\b(now|today|tonight|last\s*night|tomorrow|tmr|yesterday)\b`)
	englishClockPattern          = regexp.MustCompile(`(?i)\b(morning|afternoon|evening|noon|tonight|last\s*night|(?:[0-1]?[0-9]|2[0-3])[:\-][0-5][0-9](?:\s*(?:a\.?m?\.?|p\.?m?\.?))?|(?:[0-1]?[0-9])\s*(?:a\.?m?\.?|p\.?m?\.?))(?:\b|$)`)
)

var englishNumbers = map[string]int{
	"one": 1, "two": 2, "three": 3, "four": 4, "five": 5, "six": 6,
	"seven": 7, "eight": 8, "nine": 9, "ten": 10, "eleven": 11, "twelve": 12,
	"a": 1, "an": 1,
}

var englishWeekdays = map[string]time.Weekday{
	"sunday": time.Sunday, "sun": time.Sunday,
	"monday": time.Monday, "mon": time.Monday,
	"tuesday": time.Tuesday, "tue": time.Tuesday,
	"wednesday": time.Wednesday, "wed": time.Wednesday,
	"thursday": time.Thursday, "thur": time.Thursday, "thu": time.Thursday,
	"friday": time.Friday, "fri": time.Friday,
	"saturday": time.Saturday, "sat": time.Saturday,
}

// ParseNaturalTime parses an English natural-language date or time phrase. It returns a
// canonical RFC3339Nano string. The optional reference, language, and timezone fields make
// relative parsing reproducible and leave room for additional language rule sets.
func ParseNaturalTime(value string, options map[string]any) (string, error) {
	return parseNaturalTime(value, options, time.Now)
}

func parseNaturalTime(value string, options map[string]any, now func() time.Time) (string, error) {
	if len(value) > maxNaturalTimePhraseBytes {
		return "", fmt.Errorf("natural time value exceeds %d bytes", maxNaturalTimePhraseBytes)
	}
	// Collapsing interior whitespace keeps a phrase such as "last  night" on the same rules as its
	// single-spaced spelling, and keeps the recognized-phrase comparison below stable.
	phrase := strings.Join(strings.Fields(value), " ")
	if phrase == "" {
		return "", fmt.Errorf("natural time value must not be blank")
	}

	configuration, err := parseNaturalTimeOptions(options)
	if err != nil {
		return "", err
	}
	language, exists := naturalTimeLanguages[configuration.language]
	if !exists {
		return "", fmt.Errorf("unsupported natural time language %q", configuration.language)
	}

	reference, err := naturalTimeReference(configuration, now)
	if err != nil {
		return "", err
	}
	parsed, err := language.parser.Parse(phrase, reference)
	if err != nil {
		return "", fmt.Errorf("parsing natural time %q: %w", phrase, err)
	}
	if parsed == nil || parsed.Index != 0 || parsed.Text != phrase {
		return "", fmt.Errorf("natural time value %q is not a complete recognized phrase", phrase)
	}

	result := parsed.Time
	target, calendar, err := language.calendarTarget(phrase, reference)
	if err != nil {
		return "", err
	}
	clockValue, clock := language.clock(phrase, result)
	if clock {
		result = clockValue
	}
	if calendar {
		timeOfDay := reference
		if clock {
			timeOfDay = clockValue
		}
		result = time.Date(target.Year(), target.Month(), target.Day(), timeOfDay.Hour(), timeOfDay.Minute(), timeOfDay.Second(), timeOfDay.Nanosecond(), target.Location())
	}
	return result.Format(DefaultTimeLayout), nil
}

type naturalTimeOptions struct {
	language     string
	reference    string
	hasReference bool
	timezone     string
}

func parseNaturalTimeOptions(options map[string]any) (naturalTimeOptions, error) {
	configuration := naturalTimeOptions{language: "en"}
	for name, raw := range options {
		text, ok := raw.(string)
		if !ok {
			return naturalTimeOptions{}, fmt.Errorf("natural time option %q must be a string, got %T", name, raw)
		}
		switch name {
		case "language":
			if strings.TrimSpace(text) == "" || strings.TrimSpace(text) != text {
				return naturalTimeOptions{}, fmt.Errorf("invalid natural time language %q", text)
			}
			configuration.language = strings.ToLower(text)
		case "reference":
			if strings.TrimSpace(text) == "" {
				return naturalTimeOptions{}, fmt.Errorf("natural time reference must not be blank")
			}
			configuration.reference = text
			configuration.hasReference = true
		case "timezone":
			configuration.timezone = text
		default:
			return naturalTimeOptions{}, fmt.Errorf("unknown natural time option %q", name)
		}
	}
	return configuration, nil
}

func naturalTimeReference(configuration naturalTimeOptions, now func() time.Time) (time.Time, error) {
	reference := now().UTC()
	var err error
	if configuration.hasReference {
		reference, err = parseCanonicalTime(configuration.reference)
		if err != nil {
			return time.Time{}, fmt.Errorf("invalid natural time reference: %w", err)
		}
	}
	location, err := loadTimeLocation(configuration.timezone, reference.Location())
	if err != nil {
		return time.Time{}, err
	}
	return reference.In(location), nil
}

func newEnglishNaturalTimeLanguage() naturalTimeLanguage {
	parser := when.New(&rules.Options{Distance: 5, MatchByOrder: true})
	parser.Add(en.All...)
	parser.Add(common.All...)
	return naturalTimeLanguage{
		parser:         parser,
		calendarTarget: englishCalendarTarget,
		clock:          englishClock,
	}
}

func englishCalendarTarget(phrase string, reference time.Time) (time.Time, bool, error) {
	if match := englishFutureCalendarPattern.FindStringSubmatch(phrase); match != nil {
		return englishRelativeCalendarTarget(reference, match[1], match[2], 1)
	}
	if match := englishPastCalendarPattern.FindStringSubmatch(phrase); match != nil {
		return englishRelativeCalendarTarget(reference, match[1], match[2], -1)
	}
	if match := englishCasualDatePattern.FindStringSubmatch(phrase); match != nil {
		switch englishUnspaced(match[1]) {
		case "tomorrow", "tmr":
			return reference.AddDate(0, 0, 1), true, nil
		case "yesterday", "lastnight":
			return reference.AddDate(0, 0, -1), true, nil
		default:
			return reference, true, nil
		}
	}
	if match := englishWeekdayPattern.FindStringSubmatch(phrase); match != nil {
		return englishWeekdayTarget(reference, match[1], match[2], match[3]), true, nil
	}
	return time.Time{}, false, nil
}

// englishClock reports the time of day a phrase pins down, with seconds and fractional seconds
// zeroed. Every English clock rule sets both the hour and the minute except "last night": its rule
// pins the hour alone, and only for the spaced spelling, so the parsed value would otherwise carry
// the reference's minute — or, for "lastnight", none of the phrase's meaning at all.
func englishClock(phrase string, parsed time.Time) (time.Time, bool) {
	match := englishClockPattern.FindStringSubmatch(phrase)
	if match == nil {
		return time.Time{}, false
	}
	hour, minute := parsed.Hour(), parsed.Minute()
	if englishUnspaced(match[1]) == "lastnight" {
		hour, minute = englishLastNightHour, 0
	}
	return time.Date(parsed.Year(), parsed.Month(), parsed.Day(), hour, minute, 0, 0, parsed.Location()), true
}

// englishUnspaced folds a matched phrase to its spaceless lowercase spelling, since the English
// rules separate their tokens with `\s*` and so read "lastnight" as readily as "last night".
func englishUnspaced(value string) string {
	return strings.ToLower(strings.Join(strings.Fields(value), ""))
}

func englishRelativeCalendarTarget(reference time.Time, quantityText, unit string, direction int) (time.Time, bool, error) {
	quantity, err := englishQuantity(quantityText)
	if err != nil {
		return time.Time{}, false, err
	}
	quantity *= direction
	switch strings.ToLower(unit) {
	case "day", "days":
		return reference.AddDate(0, 0, quantity), true, nil
	case "week", "weeks":
		return reference.AddDate(0, 0, quantity*7), true, nil
	case "month", "months":
		return reference.AddDate(0, quantity, 0), true, nil
	case "year", "years":
		return reference.AddDate(quantity, 0, 0), true, nil
	default:
		return time.Time{}, false, fmt.Errorf("unsupported English calendar unit %q", unit)
	}
}

func englishQuantity(value string) (int, error) {
	value = strings.ToLower(strings.Join(strings.Fields(value), " "))
	// The English rule set reads any "few" spelling, spaced or not, as three.
	if strings.Contains(value, "few") {
		return 3, nil
	}
	if quantity, exists := englishNumbers[value]; exists {
		return quantity, nil
	}
	quantity, err := strconv.ParseInt(value, 10, 32)
	if err != nil {
		return 0, fmt.Errorf("invalid English natural time quantity %q", value)
	}
	return int(quantity), nil
}

func englishWeekdayTarget(reference time.Time, prefix, weekday, suffix string) time.Time {
	target := englishWeekdays[strings.ToLower(weekday)]
	modifier := strings.ToLower(prefix)
	if suffix != "" {
		modifier = strings.ToLower(suffix)
	}
	if modifier == "" {
		modifier = "next"
	}
	difference := int(target - reference.Weekday())
	switch modifier {
	case "last", "past":
		if difference >= 0 {
			difference -= 7
		}
	case "this":
		// The English parser treats "this" as the named day in the current
		// Sunday-to-Saturday week, which may be earlier than the reference.
	default:
		if difference <= 0 {
			difference += 7
		}
	}
	return reference.AddDate(0, 0, difference)
}
