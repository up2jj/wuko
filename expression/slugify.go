package expression

import (
	"fmt"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

type slugifyOptions struct {
	mode          string
	separator     rune
	preserveSlash bool
}

// Slugify converts value into a deterministic ASCII slug. The optional options map accepts
// mode ("slug" or "git"), separator ("-", "_", or "."), and preserve_slash (a bool).
func Slugify(value string, options map[string]any) (string, error) {
	parsed, err := parseSlugifyOptions(options)
	if err != nil {
		return "", err
	}

	result := slugifyValue(value, parsed)
	if result == "" {
		return "", fmt.Errorf("slugify result is empty")
	}
	return result, nil
}

func parseSlugifyOptions(options map[string]any) (slugifyOptions, error) {
	parsed := slugifyOptions{mode: "slug", separator: '-'}
	if options == nil {
		return parsed, nil
	}

	for key, value := range options {
		switch key {
		case "mode":
			mode, ok := value.(string)
			if !ok {
				return slugifyOptions{}, fmt.Errorf("slugify option %q must be a string", key)
			}
			switch mode {
			case "slug", "git":
				parsed.mode = mode
			default:
				return slugifyOptions{}, fmt.Errorf("slugify option %q must be %q or %q", key, "slug", "git")
			}
		case "separator":
			separator, ok := value.(string)
			if !ok {
				return slugifyOptions{}, fmt.Errorf("slugify option %q must be one of %q, %q, or %q", key, "-", "_", ".")
			}
			if len(separator) != 1 || (separator[0] != '-' && separator[0] != '_' && separator[0] != '.') {
				return slugifyOptions{}, fmt.Errorf("slugify option %q must be one of %q, %q, or %q", key, "-", "_", ".")
			}
			parsed.separator = rune(separator[0])
		case "preserve_slash":
			preserveSlash, ok := value.(bool)
			if !ok {
				return slugifyOptions{}, fmt.Errorf("slugify option %q must be a boolean", key)
			}
			parsed.preserveSlash = preserveSlash
		default:
			return slugifyOptions{}, fmt.Errorf("unknown slugify option %q", key)
		}
	}

	if parsed.mode == "git" && !hasOption(options, "preserve_slash") {
		parsed.preserveSlash = true
	}
	if parsed.mode == "git" && parsed.separator == '.' {
		return slugifyOptions{}, fmt.Errorf("slugify option %q value %q is not supported in git mode", "separator", ".")
	}
	return parsed, nil
}

func hasOption(options map[string]any, key string) bool {
	_, exists := options[key]
	return exists
}

func slugifyValue(value string, options slugifyOptions) string {
	value = norm.NFD.String(strings.ToLower(value))
	segments := make([]string, 0, 1)
	var segment strings.Builder
	pendingSeparator := false

	flush := func() {
		if segment.Len() == 0 {
			return
		}
		segments = append(segments, segment.String())
		segment.Reset()
		pendingSeparator = false
	}

	for _, r := range value {
		if unicode.Is(unicode.Mn, r) {
			continue
		}
		if isSlugLetterOrDigit(r) {
			if pendingSeparator && segment.Len() > 0 {
				segment.WriteRune(options.separator)
			}
			pendingSeparator = false
			segment.WriteRune(r)
			continue
		}
		if r == '/' && options.preserveSlash {
			flush()
			continue
		}
		if segment.Len() > 0 {
			pendingSeparator = true
		}
	}
	flush()

	if options.preserveSlash {
		return strings.Join(segments, "/")
	}
	if len(segments) == 0 {
		return ""
	}
	return segments[0]
}

func isSlugLetterOrDigit(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
}

func templateSlugify(values ...any) (string, error) {
	if len(values) == 0 || len(values) > 2 {
		return "", fmt.Errorf("slugify expects a string and optional options object")
	}

	var value string
	valueSet := false
	var options map[string]any
	for _, candidate := range values {
		switch typed := candidate.(type) {
		case string:
			if valueSet {
				return "", fmt.Errorf("slugify expects one string value")
			}
			value = typed
			valueSet = true
		case map[string]any:
			if options != nil {
				return "", fmt.Errorf("slugify expects one options object")
			}
			options = typed
		case nil:
			if len(values) == 2 {
				continue
			}
			return "", fmt.Errorf("slugify expects a string value")
		default:
			return "", fmt.Errorf("slugify argument must be a string or options object, got %T", candidate)
		}
	}
	if !valueSet {
		return "", fmt.Errorf("slugify expects a string value")
	}
	return Slugify(value, options)
}

func exprSlugify(values ...any) (any, error) {
	if len(values) == 0 || len(values) > 2 {
		return nil, fmt.Errorf("slugify expects a string and optional options object")
	}
	value, ok := values[0].(string)
	if !ok {
		return nil, fmt.Errorf("slugify value must be a string, got %T", values[0])
	}
	var options map[string]any
	if len(values) == 2 {
		var ok bool
		options, ok = values[1].(map[string]any)
		if !ok {
			return nil, fmt.Errorf("slugify options must be an object, got %T", values[1])
		}
	}
	return Slugify(value, options)
}
