package expression

import (
	cryptorand "crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"math/big"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	defaultRandomAlphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	passwordLower         = "abcdefghijklmnopqrstuvwxyz"
	passwordUpper         = "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	passwordDigits        = "0123456789"
	passwordSymbols       = "!@#$%^&*()-_=+[]{};:,.<>?"
	passwordAmbiguous     = "lI1O0o|`'\";:.,"
)

// UUID generates a version 4 or version 7 UUID.
func UUID(options map[string]any) (string, error) {
	return generateUUID(options, cryptorand.Reader)
}

func generateUUID(options map[string]any, reader io.Reader) (string, error) {
	version := 4
	uppercase := false
	compact := false
	for name, raw := range options {
		switch name {
		case "version":
			value, err := integerArgument(fmt.Sprintf("UUID option %q", name), raw)
			if err != nil {
				return "", err
			}
			version = value
		case "uppercase":
			value, ok := raw.(bool)
			if !ok {
				return "", fmt.Errorf("UUID option %q must be a boolean, got %T", name, raw)
			}
			uppercase = value
		case "compact":
			value, ok := raw.(bool)
			if !ok {
				return "", fmt.Errorf("UUID option %q must be a boolean, got %T", name, raw)
			}
			compact = value
		default:
			return "", fmt.Errorf("unknown UUID option %q", name)
		}
	}
	var value uuid.UUID
	var err error
	switch version {
	case 4:
		value, err = uuid.NewRandomFromReader(reader)
	case 7:
		value, err = uuid.NewV7FromReader(reader)
	default:
		return "", fmt.Errorf("UUID version must be 4 or 7")
	}
	if err != nil {
		return "", fmt.Errorf("generating UUID: %w", err)
	}
	result := value.String()
	if compact {
		result = strings.ReplaceAll(result, "-", "")
	}
	if uppercase {
		result = strings.ToUpper(result)
	}
	return result, nil
}

// RandomString returns length cryptographically random characters from charset.
func RandomString(length int, charset string) (string, error) {
	return randomString(cryptorand.Reader, length, charset)
}

func randomString(reader io.Reader, length int, charset string) (string, error) {
	alphabet := []rune(charset)
	if len(alphabet) == 0 {
		return "", fmt.Errorf("random string alphabet must not be empty")
	}
	if length < 0 {
		return "", fmt.Errorf("random string length must not be negative")
	}
	if length > maxTextResultBytes {
		return "", fmt.Errorf("random string result exceeds memory budget")
	}
	maximumWidth := 1
	for _, character := range alphabet {
		maximumWidth = max(maximumWidth, len(string(character)))
	}
	if length > maxTextResultBytes/maximumWidth {
		return "", fmt.Errorf("random string result exceeds memory budget")
	}
	result := make([]rune, length)
	for index := range result {
		selected, err := randomIndex(reader, len(alphabet))
		if err != nil {
			return "", fmt.Errorf("generating random string: %w", err)
		}
		result[index] = alphabet[selected]
	}
	if len(string(result)) > maxTextResultBytes {
		return "", fmt.Errorf("random string result exceeds memory budget")
	}
	return string(result), nil
}

// RandomInt returns a cryptographically random integer in the inclusive range [minimum, maximum].
func RandomInt(minimum, maximum int64) (int64, error) {
	return randomInt(cryptorand.Reader, minimum, maximum)
}

func randomInt(reader io.Reader, minimum, maximum int64) (int64, error) {
	if minimum > maximum {
		return 0, fmt.Errorf("random integer minimum must not exceed maximum")
	}
	span := new(big.Int).Sub(big.NewInt(maximum), big.NewInt(minimum))
	span.Add(span, big.NewInt(1))
	selected, err := cryptorand.Int(reader, span)
	if err != nil {
		return 0, fmt.Errorf("generating random integer: %w", err)
	}
	selected.Add(selected, big.NewInt(minimum))
	return selected.Int64(), nil
}

// RandomToken returns securely generated random bytes in the requested encoding.
func RandomToken(byteCount int, encoding string) (string, error) {
	return randomToken(cryptorand.Reader, byteCount, encoding)
}

func randomToken(reader io.Reader, byteCount int, encoding string) (string, error) {
	if byteCount < 0 {
		return "", fmt.Errorf("random token byte count must not be negative")
	}
	if encoding == "" {
		encoding = "hex"
	}
	encodedLength := 0
	switch encoding {
	case "hex":
		if byteCount > maxTextResultBytes/2 {
			return "", fmt.Errorf("random token result exceeds memory budget")
		}
		encodedLength = hex.EncodedLen(byteCount)
	case "base64":
		if byteCount > maxTextResultBytes/4*3 {
			return "", fmt.Errorf("random token result exceeds memory budget")
		}
		encodedLength = base64.StdEncoding.EncodedLen(byteCount)
	case "base64url":
		if byteCount > maxTextResultBytes/4*3 {
			return "", fmt.Errorf("random token result exceeds memory budget")
		}
		encodedLength = base64.RawURLEncoding.EncodedLen(byteCount)
	default:
		return "", fmt.Errorf("random token encoding must be %q, %q, or %q", "hex", "base64", "base64url")
	}
	if encodedLength > maxTextResultBytes {
		return "", fmt.Errorf("random token result exceeds memory budget")
	}
	data := make([]byte, byteCount)
	if _, err := io.ReadFull(reader, data); err != nil {
		return "", fmt.Errorf("generating random token: %w", err)
	}
	switch encoding {
	case "hex":
		return hex.EncodeToString(data), nil
	case "base64":
		return base64.StdEncoding.EncodeToString(data), nil
	default:
		return base64.RawURLEncoding.EncodeToString(data), nil
	}
}

// Password creates a secure password containing every enabled character group.
func Password(length int, options map[string]any) (string, error) {
	return password(cryptorand.Reader, length, options)
}

func password(reader io.Reader, length int, options map[string]any) (string, error) {
	groups := map[string]bool{"lower": true, "upper": true, "digits": true, "symbols": true}
	excludeAmbiguous := false
	for name, raw := range options {
		switch name {
		case "lower", "upper", "digits", "symbols":
			value, ok := raw.(bool)
			if !ok {
				return "", fmt.Errorf("password option %q must be a boolean, got %T", name, raw)
			}
			groups[name] = value
		case "exclude_ambiguous":
			value, ok := raw.(bool)
			if !ok {
				return "", fmt.Errorf("password option %q must be a boolean, got %T", name, raw)
			}
			excludeAmbiguous = value
		default:
			return "", fmt.Errorf("unknown password option %q", name)
		}
	}
	if length < 0 {
		return "", fmt.Errorf("password length must not be negative")
	}
	if length > maxTextResultBytes {
		return "", fmt.Errorf("password result exceeds memory budget")
	}
	definitions := []struct{ name, characters string }{
		{"lower", passwordLower}, {"upper", passwordUpper}, {"digits", passwordDigits}, {"symbols", passwordSymbols},
	}
	selectedGroups := make([]string, 0, len(definitions))
	for _, definition := range definitions {
		if !groups[definition.name] {
			continue
		}
		characters := definition.characters
		if excludeAmbiguous {
			characters = strings.Map(func(character rune) rune {
				if strings.ContainsRune(passwordAmbiguous, character) {
					return -1
				}
				return character
			}, characters)
		}
		if characters != "" {
			selectedGroups = append(selectedGroups, characters)
		}
	}
	if len(selectedGroups) == 0 {
		return "", fmt.Errorf("password must enable at least one character group")
	}
	if length < len(selectedGroups) {
		return "", fmt.Errorf("password length %d is smaller than %d enabled character groups", length, len(selectedGroups))
	}
	alphabet := strings.Join(selectedGroups, "")
	result := make([]byte, 0, length)
	for _, group := range selectedGroups {
		index, err := randomIndex(reader, len(group))
		if err != nil {
			return "", fmt.Errorf("generating password: %w", err)
		}
		result = append(result, group[index])
	}
	for len(result) < length {
		index, err := randomIndex(reader, len(alphabet))
		if err != nil {
			return "", fmt.Errorf("generating password: %w", err)
		}
		result = append(result, alphabet[index])
	}
	for index := len(result) - 1; index > 0; index-- {
		other, err := randomIndex(reader, index+1)
		if err != nil {
			return "", fmt.Errorf("shuffling password: %w", err)
		}
		result[index], result[other] = result[other], result[index]
	}
	return string(result), nil
}

func randomIndex(reader io.Reader, size int) (int, error) {
	value, err := cryptorand.Int(reader, big.NewInt(int64(size)))
	if err != nil {
		return 0, err
	}
	return int(value.Int64()), nil
}

// CurrentTime returns the current UTC time as RFC3339Nano.
func CurrentTime() string { return currentTime(time.Now) }

func currentTime(now func() time.Time) string { return now().UTC().Format(time.RFC3339Nano) }

// UnixTimestamp returns the current Unix timestamp in seconds or milliseconds.
func UnixTimestamp(unit string) (int64, error) { return unixTimestamp(time.Now, unit) }

func unixTimestamp(now func() time.Time, unit string) (int64, error) {
	instant := now()
	switch unit {
	case "", "seconds":
		return instant.Unix(), nil
	case "milliseconds":
		return instant.UnixMilli(), nil
	default:
		return 0, fmt.Errorf("Unix timestamp unit must be %q or %q", "seconds", "milliseconds")
	}
}
