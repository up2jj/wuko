package expression

import (
	"fmt"
	"math/big"
	"strings"
)

// BaseConvert converts a signed integer between bases 2 and 36.
func BaseConvert(value string, from, to int, uppercase bool) (string, error) {
	if from < 2 || from > 36 || to < 2 || to > 36 {
		return "", fmt.Errorf("number bases must be between 2 and 36")
	}
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "", fmt.Errorf("number must not be blank")
	}
	if len(trimmed) > maxTextResultBytes {
		return "", fmt.Errorf("number exceeds memory budget")
	}
	number := new(big.Int)
	if _, ok := number.SetString(trimmed, from); !ok {
		return "", fmt.Errorf("%q is not a base %d integer", value, from)
	}
	result := number.Text(to)
	if uppercase {
		result = strings.ToUpper(result)
	}
	if len(result) > maxTextResultBytes {
		return "", fmt.Errorf("converted number exceeds memory budget")
	}
	return result, nil
}

var romanValues = []struct {
	value int
	text  string
}{
	{1000, "M"}, {900, "CM"}, {500, "D"}, {400, "CD"}, {100, "C"}, {90, "XC"},
	{50, "L"}, {40, "XL"}, {10, "X"}, {9, "IX"}, {5, "V"}, {4, "IV"}, {1, "I"},
}

// RomanEncode renders a canonical Roman numeral from 1 through 3999.
func RomanEncode(value int) (string, error) {
	if value < 1 || value > 3999 {
		return "", fmt.Errorf("Roman numeral value must be between 1 and 3999")
	}
	var result strings.Builder
	left := value
	for _, entry := range romanValues {
		for left >= entry.value {
			result.WriteString(entry.text)
			left -= entry.value
		}
	}
	return result.String(), nil
}

// RomanDecode parses a canonical Roman numeral from I through MMMCMXCIX.
func RomanDecode(value string) (int, error) {
	normalized := strings.ToUpper(strings.TrimSpace(value))
	if normalized == "" {
		return 0, fmt.Errorf("Roman numeral must not be blank")
	}
	total := 0
	for len(normalized) > 0 {
		matched := false
		for _, entry := range romanValues {
			if strings.HasPrefix(normalized, entry.text) {
				total += entry.value
				normalized = normalized[len(entry.text):]
				matched = true
				break
			}
		}
		if !matched {
			return 0, fmt.Errorf("%q is not a Roman numeral", value)
		}
	}
	canonical, err := RomanEncode(total)
	if err != nil || canonical != strings.ToUpper(strings.TrimSpace(value)) {
		return 0, fmt.Errorf("%q is not a canonical Roman numeral", value)
	}
	return total, nil
}

// Ordinal appends the English ordinal suffix to an integer.
func Ordinal(value int64) string {
	abs := uint64(value)
	if value < 0 {
		abs = uint64(-(value + 1)) + 1
	}
	mod100 := abs % 100
	suffix := "th"
	if mod100 < 11 || mod100 > 13 {
		switch abs % 10 {
		case 1:
			suffix = "st"
		case 2:
			suffix = "nd"
		case 3:
			suffix = "rd"
		}
	}
	return fmt.Sprintf("%d%s", value, suffix)
}
