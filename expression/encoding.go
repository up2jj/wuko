package expression

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"html"
	"strings"
	"unicode/utf8"
)

// Base64Encode encodes UTF-8 text using the configured Base64 alphabet and padding.
func Base64Encode(value string, options map[string]any) (string, error) {
	encoding, err := base64Encoding(options)
	if err != nil {
		return "", err
	}
	if len(value) > maxTextResultBytes || encoding.EncodedLen(len(value)) > maxTextResultBytes {
		return "", fmt.Errorf("base64 result exceeds memory budget")
	}
	return encoding.EncodeToString([]byte(value)), nil
}

// Base64Decode decodes Base64 text and requires the result to be valid UTF-8.
func Base64Decode(value string, options map[string]any) (string, error) {
	encoding, err := base64Encoding(options)
	if err != nil {
		return "", err
	}
	if encoding.DecodedLen(len(value)) > maxTextResultBytes {
		return "", fmt.Errorf("base64 result exceeds memory budget")
	}
	decoded, err := encoding.DecodeString(value)
	if err != nil {
		return "", fmt.Errorf("decoding base64: %w", err)
	}
	if !utf8.Valid(decoded) {
		return "", fmt.Errorf("decoded base64 is not valid UTF-8")
	}
	return string(decoded), nil
}

func base64Encoding(options map[string]any) (*base64.Encoding, error) {
	alphabet := "standard"
	padding := true
	for name, raw := range options {
		switch name {
		case "alphabet":
			var ok bool
			alphabet, ok = raw.(string)
			if !ok {
				return nil, fmt.Errorf("base64 option %q must be a string, got %T", name, raw)
			}
		case "padding":
			var ok bool
			padding, ok = raw.(bool)
			if !ok {
				return nil, fmt.Errorf("base64 option %q must be a boolean, got %T", name, raw)
			}
		default:
			return nil, fmt.Errorf("unknown base64 option %q", name)
		}
	}
	var encoding *base64.Encoding
	switch alphabet {
	case "standard":
		encoding = base64.StdEncoding
	case "url":
		encoding = base64.URLEncoding
	default:
		return nil, fmt.Errorf("base64 alphabet must be %q or %q", "standard", "url")
	}
	if !padding {
		encoding = encoding.WithPadding(base64.NoPadding)
	}
	return encoding.Strict(), nil
}

// HexEncode encodes UTF-8 text as hexadecimal.
func HexEncode(value string, uppercase bool) (string, error) {
	if len(value) > maxTextResultBytes/2 {
		return "", fmt.Errorf("hex result exceeds memory budget")
	}
	result := hex.EncodeToString([]byte(value))
	if uppercase {
		result = strings.ToUpper(result)
	}
	return result, nil
}

// HexDecode decodes hexadecimal text and requires the result to be valid UTF-8.
func HexDecode(value string) (string, error) {
	if len(value)/2 > maxTextResultBytes {
		return "", fmt.Errorf("hex result exceeds memory budget")
	}
	decoded, err := hex.DecodeString(value)
	if err != nil {
		return "", fmt.Errorf("decoding hex: %w", err)
	}
	if !utf8.Valid(decoded) {
		return "", fmt.Errorf("decoded hex is not valid UTF-8")
	}
	return string(decoded), nil
}

// URLEncode percent-encodes a URI component according to RFC 3986.
func URLEncode(value string) (string, error) {
	const digits = "0123456789ABCDEF"
	if len(value) > maxTextResultBytes {
		return "", fmt.Errorf("URL-encoded result exceeds memory budget")
	}
	encodedBytes := 0
	for _, b := range []byte(value) {
		if uriUnreserved(b) {
			encodedBytes++
		} else {
			encodedBytes += 3
		}
	}
	if encodedBytes > maxTextResultBytes {
		return "", fmt.Errorf("URL-encoded result exceeds memory budget")
	}
	var result strings.Builder
	result.Grow(encodedBytes)
	for _, b := range []byte(value) {
		if uriUnreserved(b) {
			result.WriteByte(b)
			continue
		}
		result.WriteByte('%')
		result.WriteByte(digits[b>>4])
		result.WriteByte(digits[b&15])
	}
	return result.String(), nil
}

// URLDecode decodes RFC 3986 percent escapes without treating plus as a space.
func URLDecode(value string) (string, error) {
	decoded := make([]byte, 0, len(value))
	for index := 0; index < len(value); index++ {
		if value[index] != '%' {
			decoded = append(decoded, value[index])
			continue
		}
		if index+2 >= len(value) {
			return "", fmt.Errorf("decoding URL component: incomplete escape at byte %d", index)
		}
		high, ok := hexNibble(value[index+1])
		if !ok {
			return "", fmt.Errorf("decoding URL component: invalid escape at byte %d", index)
		}
		low, ok := hexNibble(value[index+2])
		if !ok {
			return "", fmt.Errorf("decoding URL component: invalid escape at byte %d", index)
		}
		decoded = append(decoded, high<<4|low)
		index += 2
	}
	if !utf8.Valid(decoded) {
		return "", fmt.Errorf("decoded URL component is not valid UTF-8")
	}
	return string(decoded), nil
}

func uriUnreserved(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' ||
		value >= '0' && value <= '9' || strings.ContainsRune("-._~", rune(value))
}

func hexNibble(value byte) (byte, bool) {
	switch {
	case value >= '0' && value <= '9':
		return value - '0', true
	case value >= 'a' && value <= 'f':
		return value - 'a' + 10, true
	case value >= 'A' && value <= 'F':
		return value - 'A' + 10, true
	default:
		return 0, false
	}
}

// HTMLEncode escapes the five HTML-significant characters in value.
func HTMLEncode(value string) (string, error) {
	// Escaping expands at most sixfold ("&" becomes "&amp;"), so bound the input before
	// html.EscapeString allocates a result that the budget would then reject.
	if len(value) > maxTextResultBytes/6 {
		return "", fmt.Errorf("HTML-encoded result exceeds memory budget")
	}
	result := html.EscapeString(value)
	if len(result) > maxTextResultBytes {
		return "", fmt.Errorf("HTML-encoded result exceeds memory budget")
	}
	return result, nil
}

// HTMLDecode resolves named and numeric HTML entities.
func HTMLDecode(value string) (string, error) {
	// Unescaping never grows the input, so the input bound is the result bound.
	if len(value) > maxTextResultBytes {
		return "", fmt.Errorf("HTML-decoded result exceeds memory budget")
	}
	return html.UnescapeString(value), nil
}
