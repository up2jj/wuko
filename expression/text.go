package expression

import (
	"fmt"
	"html"
	"math"
	"regexp"
	"strings"
	"unicode"

	"github.com/rivo/uniseg"
	"golang.org/x/text/unicode/norm"
)

const (
	maxTextRepeatCount = 1_000_000
	// maxTextResultBytes caps the size of a text expansion. Expr's builtin repeat charged its
	// result size against the VM memory budget; Wuko's replacement keeps an equivalent ceiling so
	// that a large input multiplied by a large count cannot exhaust the process.
	maxTextResultBytes = 64 << 20
)

func reverseText(value string) string {
	result, _ := mapTextLines(value, func(line string) (string, error) {
		clusters := graphemeClusters(line)
		for left, right := 0, len(clusters)-1; left < right; left, right = left+1, right-1 {
			clusters[left], clusters[right] = clusters[right], clusters[left]
		}
		return strings.Join(clusters, ""), nil
	})
	return result
}

func reverseWords(value string) string {
	result, _ := mapTextLines(value, func(line string) (string, error) {
		words := strings.Fields(line)
		for left, right := 0, len(words)-1; left < right; left, right = left+1, right-1 {
			words[left], words[right] = words[right], words[left]
		}
		return strings.Join(words, " "), nil
	})
	return result
}

func repeat(value string, count int, separator string) (string, error) {
	if count < 0 {
		return "", fmt.Errorf("repeat count must not be negative")
	}
	if count > maxTextRepeatCount {
		return "", fmt.Errorf("repeat count exceeds memory budget")
	}
	if count == 0 || value == "" && separator == "" {
		return "", nil
	}
	maxInt := int(^uint(0) >> 1)
	separatorBytes := 0
	if count > 1 {
		if len(separator) > maxInt/(count-1) {
			return "", fmt.Errorf("repeat result exceeds memory budget")
		}
		separatorBytes = len(separator) * (count - 1)
	}
	if count > 0 && len(value) > (maxInt-separatorBytes)/count {
		return "", fmt.Errorf("repeat result exceeds memory budget")
	}
	total := len(value)*count + separatorBytes
	if total > maxTextResultBytes {
		return "", fmt.Errorf("repeat result of %d bytes exceeds memory budget of %d bytes", total, maxTextResultBytes)
	}
	var result strings.Builder
	result.Grow(total)
	for index := range count {
		if index > 0 {
			result.WriteString(separator)
		}
		result.WriteString(value)
	}
	return result.String(), nil
}

func truncate(value string, length int, suffix string) (string, error) {
	if length < 0 {
		return "", fmt.Errorf("truncate length must not be negative")
	}
	suffixLength := uniseg.GraphemeClusterCount(suffix)
	if suffixLength > length {
		return "", fmt.Errorf("truncate suffix has %d characters, exceeds length %d", suffixLength, length)
	}
	return mapTextLines(value, func(line string) (string, error) {
		clusters := graphemeClusters(line)
		if len(clusters) <= length {
			return line, nil
		}
		return strings.Join(clusters[:length-suffixLength], "") + suffix, nil
	})
}

func squeeze(value string) string {
	result, _ := mapTextLines(value, func(line string) (string, error) {
		return strings.Join(strings.Fields(line), " "), nil
	})
	return result
}

func removeWhitespace(value string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, value)
}

func removePunctuation(value string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsNumber(r) || unicode.IsSpace(r) {
			return r
		}
		return -1
	}, value)
}

func removeAccents(value string) string {
	decomposed := norm.NFD.String(value)
	plain := strings.Map(func(r rune) rune {
		if unicode.Is(unicode.Mn, r) {
			return -1
		}
		return r
	}, decomposed)
	return norm.NFC.String(plain)
}

func removeNonASCII(value string) string {
	return strings.Map(func(r rune) rune {
		if r > unicode.MaxASCII {
			return -1
		}
		return r
	}, value)
}

func stripHTML(value string) string {
	var result strings.Builder
	result.Grow(len(value))
	for index := 0; index < len(value); {
		if value[index] != '<' || !startsHTMLTag(value[index+1:]) {
			result.WriteByte(value[index])
			index++
			continue
		}
		end := endOfHTMLTag(value[index+1:])
		if end < 0 {
			// An unterminated tag consumes the remainder, matching how browsers drop it.
			break
		}
		index += end + 2
	}
	return html.UnescapeString(result.String())
}

// startsHTMLTag reports whether the text following a "<" opens a tag rather than being a literal
// less-than sign. HTML only treats "<" as markup when a name, "/", "!", or "?" follows it, so
// "5 < 6" keeps its "<" instead of swallowing the rest of the text.
func startsHTMLTag(rest string) bool {
	if rest == "" {
		return false
	}
	switch first := rest[0]; {
	case first == '/', first == '!', first == '?':
		return true
	default:
		return 'a' <= first && first <= 'z' || 'A' <= first && first <= 'Z'
	}
}

// endOfHTMLTag returns the offset of the ">" closing a tag that started just before rest, skipping
// quoted attribute values so that <a title="a>b"> is removed whole. It returns -1 when the tag is
// never closed.
func endOfHTMLTag(rest string) int {
	quote := byte(0)
	for index := 0; index < len(rest); index++ {
		switch character := rest[index]; {
		case quote != 0:
			if character == quote {
				quote = 0
			}
		case character == '"' || character == '\'':
			quote = character
		case character == '>':
			return index
		}
	}
	return -1
}

func tabsToSpaces(value string, width int) (string, error) {
	if err := validateTextWidth(width); err != nil {
		return "", err
	}
	tabs := strings.Count(value, "\t")
	maxInt := int(^uint(0) >> 1)
	if tabs > 0 && width-1 > (maxInt-len(value))/tabs {
		return "", fmt.Errorf("tabsToSpaces result exceeds memory budget")
	}
	if total := len(value) + tabs*(width-1); total > maxTextResultBytes {
		return "", fmt.Errorf("tabsToSpaces result of %d bytes exceeds memory budget of %d bytes", total, maxTextResultBytes)
	}
	return strings.ReplaceAll(value, "\t", strings.Repeat(" ", width)), nil
}

func spacesToTabs(value string, width int) (string, error) {
	if err := validateTextWidth(width); err != nil {
		return "", err
	}
	return strings.ReplaceAll(value, strings.Repeat(" ", width), "\t"), nil
}

func validateTextWidth(width int) error {
	if width < 1 {
		return fmt.Errorf("width must be at least 1")
	}
	if width > maxTextRepeatCount {
		return fmt.Errorf("width exceeds memory budget")
	}
	return nil
}

func newlinesToSpaces(value string) string {
	normalized := strings.ReplaceAll(value, "\r\n", "\n")
	normalized = strings.TrimSuffix(normalized, "\n")
	return strings.Join(strings.Split(normalized, "\n"), " ")
}

func spacesToNewlines(value string) string {
	return strings.Join(strings.Fields(value), "\n")
}

func rotate(value string, count int) string {
	result, _ := mapTextLines(value, func(line string) (string, error) {
		clusters := graphemeClusters(line)
		if len(clusters) == 0 {
			return "", nil
		}
		shift := count % len(clusters)
		if shift < 0 {
			shift += len(clusters)
		}
		return strings.Join(clusters[shift:], "") + strings.Join(clusters[:shift], ""), nil
	})
	return result
}

func quote(value, delimiter string) (string, error) {
	if delimiter == "" {
		return "", fmt.Errorf("quote delimiter must not be empty")
	}
	return mapTextLines(value, func(line string) (string, error) {
		return delimiter + line + delimiter, nil
	})
}

func escapeRegex(value string) string { return regexp.QuoteMeta(value) }

func normalizeUnicode(value, form string) (string, error) {
	switch strings.ToLower(form) {
	case "nfc":
		return norm.NFC.String(value), nil
	case "nfd":
		return norm.NFD.String(value), nil
	case "nfkc":
		return norm.NFKC.String(value), nil
	case "nfkd":
		return norm.NFKD.String(value), nil
	default:
		return "", fmt.Errorf("unknown Unicode normalization form %q; use nfc, nfd, nfkc, or nfkd", form)
	}
}

// textInteger accepts any Go integer type, plus floats that carry no fraction. Wuko's parseJSON
// normalizes whole numbers to int64 and Expr environments routinely carry sized integers, so a
// plain values[i].(int) assertion would reject counts and lengths that reached the helper through
// data rather than through a literal.
func textInteger(value any) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case int8:
		return int(typed), true
	case int16:
		return int(typed), true
	case int32:
		return int(typed), true
	case int64:
		if typed > math.MaxInt || typed < math.MinInt {
			return 0, false
		}
		return int(typed), true
	case uint:
		if uint64(typed) > math.MaxInt {
			return 0, false
		}
		return int(typed), true
	case uint8:
		return int(typed), true
	case uint16:
		return int(typed), true
	case uint32:
		if uint64(typed) > math.MaxInt {
			return 0, false
		}
		return int(typed), true
	case uint64:
		if typed > math.MaxInt {
			return 0, false
		}
		return int(typed), true
	case float32:
		return integralFloat(float64(typed))
	case float64:
		return integralFloat(typed)
	default:
		return 0, false
	}
}

func integralFloat(value float64) (int, bool) {
	if math.IsNaN(value) || math.IsInf(value, 0) || value != math.Trunc(value) {
		return 0, false
	}
	if value > math.MaxInt || value < math.MinInt {
		return 0, false
	}
	return int(value), true
}

func graphemeClusters(value string) []string {
	iterator := uniseg.NewGraphemes(value)
	clusters := make([]string, 0, uniseg.GraphemeClusterCount(value))
	for iterator.Next() {
		clusters = append(clusters, iterator.Str())
	}
	return clusters
}

func mapTextLines(value string, transform func(string) (string, error)) (string, error) {
	if value == "" {
		return transform("")
	}
	var result strings.Builder
	result.Grow(len(value))
	remaining := value
	for remaining != "" {
		newline := strings.IndexByte(remaining, '\n')
		if newline < 0 {
			line, err := transform(remaining)
			if err != nil {
				return "", err
			}
			result.WriteString(line)
			break
		}
		content := remaining[:newline]
		ending := "\n"
		if strings.HasSuffix(content, "\r") {
			content = strings.TrimSuffix(content, "\r")
			ending = "\r\n"
		}
		line, err := transform(content)
		if err != nil {
			return "", err
		}
		result.WriteString(line)
		result.WriteString(ending)
		remaining = remaining[newline+1:]
	}
	return result.String(), nil
}

func templateRepeat(values ...any) (string, error) {
	value, count, option, err := templateTextArguments("repeat", values, 2, "")
	if err != nil {
		return "", err
	}
	return repeat(value, count, option)
}

func templateTruncate(values ...any) (string, error) {
	value, length, suffix, err := templateTextArguments("truncate", values, 80, "")
	if err != nil {
		return "", err
	}
	return truncate(value, length, suffix)
}

func templateTabsToSpaces(values ...any) (string, error) {
	value, width, err := templateTextIntegerArgument("tabsToSpaces", values, 4)
	if err != nil {
		return "", err
	}
	return tabsToSpaces(value, width)
}

func templateSpacesToTabs(values ...any) (string, error) {
	value, width, err := templateTextIntegerArgument("spacesToTabs", values, 4)
	if err != nil {
		return "", err
	}
	return spacesToTabs(value, width)
}

func templateRotate(values ...any) (string, error) {
	value, count, err := templateTextIntegerArgument("rotate", values, 1)
	if err != nil {
		return "", err
	}
	return rotate(value, count), nil
}

func templateQuote(values ...any) (string, error) {
	value, delimiter, err := templateTextStringArgument("quote", values, "\"")
	if err != nil {
		return "", err
	}
	return quote(value, delimiter)
}

func templateNormalizeUnicode(values ...any) (string, error) {
	value, form, err := templateTextStringArgument("normalizeUnicode", values, "nfc")
	if err != nil {
		return "", err
	}
	return normalizeUnicode(value, form)
}

func templateTextArguments(name string, values []any, defaultNumber int, defaultText string) (string, int, string, error) {
	if len(values) < 1 || len(values) > 3 {
		return "", 0, "", fmt.Errorf("%s expects optional number and text arguments followed by a piped string", name)
	}
	value, ok := values[len(values)-1].(string)
	if !ok {
		return "", 0, "", fmt.Errorf("%s value must be a string, got %T", name, values[len(values)-1])
	}
	number := defaultNumber
	if len(values) >= 2 {
		var ok bool
		number, ok = textInteger(values[0])
		if !ok {
			return "", 0, "", fmt.Errorf("%s numeric argument must be an integer, got %T", name, values[0])
		}
	}
	text := defaultText
	if len(values) == 3 {
		var ok bool
		text, ok = values[1].(string)
		if !ok {
			return "", 0, "", fmt.Errorf("%s text argument must be a string, got %T", name, values[1])
		}
	}
	return value, number, text, nil
}

func templateTextIntegerArgument(name string, values []any, fallback int) (string, int, error) {
	if len(values) < 1 || len(values) > 2 {
		return "", 0, fmt.Errorf("%s expects an optional integer followed by a piped string", name)
	}
	value, ok := values[len(values)-1].(string)
	if !ok {
		return "", 0, fmt.Errorf("%s value must be a string, got %T", name, values[len(values)-1])
	}
	if len(values) == 1 {
		return value, fallback, nil
	}
	integer, ok := textInteger(values[0])
	if !ok {
		return "", 0, fmt.Errorf("%s argument must be an integer, got %T", name, values[0])
	}
	return value, integer, nil
}

func templateTextStringArgument(name string, values []any, fallback string) (string, string, error) {
	if len(values) < 1 || len(values) > 2 {
		return "", "", fmt.Errorf("%s expects an optional string followed by a piped string", name)
	}
	value, ok := values[len(values)-1].(string)
	if !ok {
		return "", "", fmt.Errorf("%s value must be a string, got %T", name, values[len(values)-1])
	}
	if len(values) == 1 {
		return value, fallback, nil
	}
	option, ok := values[0].(string)
	if !ok {
		return "", "", fmt.Errorf("%s argument must be a string, got %T", name, values[0])
	}
	return value, option, nil
}

func exprRepeat(values ...any) (any, error) {
	value, number, text, err := exprTextArguments("repeat", values, 2, "")
	if err != nil {
		return nil, err
	}
	return repeat(value, number, text)
}

func exprTruncate(values ...any) (any, error) {
	value, number, text, err := exprTextArguments("truncate", values, 80, "")
	if err != nil {
		return nil, err
	}
	return truncate(value, number, text)
}

func exprTabsToSpaces(values ...any) (any, error) {
	value, width, err := exprTextIntegerArgument("tabsToSpaces", values, 4)
	if err != nil {
		return nil, err
	}
	return tabsToSpaces(value, width)
}

func exprSpacesToTabs(values ...any) (any, error) {
	value, width, err := exprTextIntegerArgument("spacesToTabs", values, 4)
	if err != nil {
		return nil, err
	}
	return spacesToTabs(value, width)
}

func exprRotate(values ...any) (any, error) {
	value, count, err := exprTextIntegerArgument("rotate", values, 1)
	if err != nil {
		return nil, err
	}
	return rotate(value, count), nil
}

func exprQuote(values ...any) (any, error) {
	value, delimiter, err := exprTextStringArgument("quote", values, "\"")
	if err != nil {
		return nil, err
	}
	return quote(value, delimiter)
}

func exprNormalizeUnicode(values ...any) (any, error) {
	value, form, err := exprTextStringArgument("normalizeUnicode", values, "nfc")
	if err != nil {
		return nil, err
	}
	return normalizeUnicode(value, form)
}

func exprTextArguments(name string, values []any, defaultNumber int, defaultText string) (string, int, string, error) {
	if len(values) < 1 || len(values) > 3 {
		return "", 0, "", fmt.Errorf("%s expects a value and up to two optional arguments", name)
	}
	value, ok := values[0].(string)
	if !ok {
		return "", 0, "", fmt.Errorf("%s value must be a string, got %T", name, values[0])
	}
	number := defaultNumber
	if len(values) >= 2 {
		var ok bool
		number, ok = textInteger(values[1])
		if !ok {
			return "", 0, "", fmt.Errorf("%s numeric argument must be an integer, got %T", name, values[1])
		}
	}
	text := defaultText
	if len(values) == 3 {
		var ok bool
		text, ok = values[2].(string)
		if !ok {
			return "", 0, "", fmt.Errorf("%s text argument must be a string, got %T", name, values[2])
		}
	}
	return value, number, text, nil
}

func exprTextIntegerArgument(name string, values []any, fallback int) (string, int, error) {
	if len(values) < 1 || len(values) > 2 {
		return "", 0, fmt.Errorf("%s expects a value and optional integer", name)
	}
	value, ok := values[0].(string)
	if !ok {
		return "", 0, fmt.Errorf("%s value must be a string, got %T", name, values[0])
	}
	if len(values) == 1 {
		return value, fallback, nil
	}
	integer, ok := textInteger(values[1])
	if !ok {
		return "", 0, fmt.Errorf("%s argument must be an integer, got %T", name, values[1])
	}
	return value, integer, nil
}

func exprTextStringArgument(name string, values []any, fallback string) (string, string, error) {
	if len(values) < 1 || len(values) > 2 {
		return "", "", fmt.Errorf("%s expects a value and optional string", name)
	}
	value, ok := values[0].(string)
	if !ok {
		return "", "", fmt.Errorf("%s value must be a string, got %T", name, values[0])
	}
	if len(values) == 1 {
		return value, fallback, nil
	}
	option, ok := values[1].(string)
	if !ok {
		return "", "", fmt.Errorf("%s argument must be a string, got %T", name, values[1])
	}
	return value, option, nil
}
