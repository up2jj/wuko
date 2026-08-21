package diagnostic

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"
)

const (
	Redacted      = "<redacted>"
	ConfigLimit   = 4 << 10
	truncatedMark = "…<truncated>"
)

var sensitiveKeyPattern = regexp.MustCompile(`(?i)(password|passwd|secret|token|credential|api[-_]?key|authorization|private[-_]?key)`)
var urlPattern = regexp.MustCompile(`https?://[^\s"'<>]+`)

// RedactedJSON returns deterministic compact JSON after recursively removing sensitive values.
// It is intended for diagnostics, not as a general-purpose secret scanner: values placed under
// innocuous keys can still be visible.
func RedactedJSON(value any) string {
	raw, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprintf("%q", fmt.Sprintf("<unavailable: %v>", err))
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var normalized any
	if err := decoder.Decode(&normalized); err != nil {
		return fmt.Sprintf("%q", fmt.Sprintf("<unavailable: %v>", err))
	}
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	encoder.SetEscapeHTML(false)
	err = encoder.Encode(redact(normalized, false))
	if err != nil {
		return fmt.Sprintf("%q", fmt.Sprintf("<unavailable: %v>", err))
	}
	return truncateUTF8(strings.TrimSuffix(output.String(), "\n"), ConfigLimit)
}

func redact(value any, redactValues bool) any {
	switch typed := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(typed))
		for key, item := range typed {
			sensitive := redactValues || sensitiveKey(key) || strings.EqualFold(key, "env") || strings.EqualFold(key, "environment")
			if sensitive && !isContainer(item) {
				result[key] = Redacted
				continue
			}
			result[key] = redact(item, sensitive)
		}
		return result
	case []any:
		result := make([]any, len(typed))
		for i, item := range typed {
			if redactValues && !isContainer(item) {
				result[i] = Redacted
			} else {
				result[i] = redact(item, redactValues)
			}
		}
		return result
	case string:
		if redactValues {
			return Redacted
		}
		return urlPattern.ReplaceAllStringFunc(typed, SafeURLString)
	default:
		if redactValues {
			return Redacted
		}
		return value
	}
}

func sensitiveKey(key string) bool {
	trimmed := strings.TrimSpace(key)
	return strings.EqualFold(trimmed, "auth") || strings.EqualFold(trimmed, "cookies") || sensitiveKeyPattern.MatchString(key)
}

func isContainer(value any) bool {
	switch value.(type) {
	case map[string]any, []any:
		return true
	default:
		return false
	}
}

// SafeURLString strips query strings and user information from HTTP(S) URLs. Other strings are
// returned unchanged.
func SafeURLString(value string) string {
	parsed, err := url.Parse(value)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return value
	}
	parsed.User = nil
	parsed.RawQuery = ""
	return parsed.String()
}

func truncateUTF8(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	end := limit - len(truncatedMark)
	if end < 0 {
		end = 0
	}
	for end > 0 && !utf8.ValidString(value[:end]) {
		end--
	}
	return value[:end] + truncatedMark
}
