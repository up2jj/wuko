package expression

import (
	"strings"
	"unicode/utf8"

	"github.com/rivo/uniseg"
)

// CountBytes returns the UTF-8 byte length of value.
func CountBytes(value string) int { return len(value) }

// CountRunes returns the number of Unicode code points in value.
func CountRunes(value string) int { return utf8.RuneCountInString(value) }

// CountGraphemes returns the number of user-perceived characters in value.
func CountGraphemes(value string) int { return uniseg.GraphemeClusterCount(value) }

// CountWords returns the number of Unicode-whitespace-separated words in value.
func CountWords(value string) int { return len(strings.Fields(value)) }

// CountLines returns the number of logical lines, excluding a trailing empty line.
func CountLines(value string) int {
	if value == "" {
		return 0
	}
	lines := strings.Count(value, "\n") + 1
	if strings.HasSuffix(value, "\n") {
		lines--
	}
	return lines
}
