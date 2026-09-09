package expression

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// AlternateCase converts letters on each line to aLtErNaTiNg case, beginning with lowercase.
func AlternateCase(value string) string {
	return mapCaseLines(value, func(line string) string {
		upper := false
		return strings.Map(func(character rune) rune {
			if !unicode.IsLetter(character) {
				return character
			}
			upper = !upper
			if upper {
				return unicode.ToLower(character)
			}
			return unicode.ToUpper(character)
		}, line)
	})
}

// CamelCase converts words on each line to camelCase.
func CamelCase(value string) string {
	return mapCaseLines(value, func(line string) string {
		words := splitCaseWords(line)
		if len(words) == 0 {
			return ""
		}
		var result strings.Builder
		result.Grow(len(line))
		result.WriteString(strings.ToLower(words[0]))
		for _, word := range words[1:] {
			result.WriteString(capitalizeOnly(word))
		}
		return result.String()
	})
}

// Capitalize uppercases the first character of each whitespace-delimited word and preserves the rest.
func Capitalize(value string) string {
	return mapCaseLines(value, func(line string) string {
		return mapWhitespaceWords(line, capitalizeFirst)
	})
}

// ConstantCase converts words on each line to CONSTANT_CASE.
func ConstantCase(value string) string {
	return joinCaseWordsByLine(value, "_", strings.ToUpper)
}

// DotCase converts words on each line to dot.case.
func DotCase(value string) string {
	return joinCaseWordsByLine(value, ".", strings.ToLower)
}

// KebabCase converts words on each line to kebab-case.
func KebabCase(value string) string {
	return joinCaseWordsByLine(value, "-", strings.ToLower)
}

// PascalCase converts words on each line to PascalCase.
func PascalCase(value string) string {
	return mapCaseLines(value, func(line string) string {
		var result strings.Builder
		result.Grow(len(line))
		for _, word := range splitCaseWords(line) {
			result.WriteString(capitalizeOnly(word))
		}
		return result.String()
	})
}

// SentenceCase capitalizes the first letter of each sentence and lowercases the remaining letters.
func SentenceCase(value string) string {
	return mapCaseLines(value, func(line string) string {
		startOfSentence := true
		var result strings.Builder
		result.Grow(len(line))
		for _, character := range line {
			if startOfSentence && unicode.IsLetter(character) {
				result.WriteString(strings.ToUpper(string(character)))
				startOfSentence = false
			} else {
				result.WriteString(strings.ToLower(string(character)))
			}
			if character == '.' || character == '!' || character == '?' {
				startOfSentence = true
			}
		}
		return result.String()
	})
}

// SnakeCase converts words on each line to snake_case.
func SnakeCase(value string) string {
	return joinCaseWordsByLine(value, "_", strings.ToLower)
}

// SwapCase swaps the case of every cased character.
func SwapCase(value string) string {
	return mapCaseLines(value, func(line string) string {
		var result strings.Builder
		result.Grow(len(line))
		for _, character := range line {
			if unicode.IsUpper(character) {
				result.WriteString(strings.ToLower(string(character)))
			} else {
				result.WriteString(strings.ToUpper(string(character)))
			}
		}
		return result.String()
	})
}

// TitleCase capitalizes each whitespace-delimited word and lowercases its remaining characters.
func TitleCase(value string) string {
	return mapCaseLines(value, func(line string) string {
		return mapWhitespaceWords(line, capitalizeOnly)
	})
}

// TrainCase converts words on each line to Train-Case.
func TrainCase(value string) string {
	return joinCaseWordsByLine(value, "-", capitalizeOnly)
}

func mapCaseLines(value string, transform func(string) string) string {
	result, _ := mapTextLines(value, func(line string) (string, error) {
		return transform(line), nil
	})
	return result
}

func joinCaseWordsByLine(value, separator string, transform func(string) string) string {
	return mapCaseLines(value, func(line string) string {
		words := splitCaseWords(line)
		for index := range words {
			words[index] = transform(words[index])
		}
		return strings.Join(words, separator)
	})
}

func splitCaseWords(value string) []string {
	characters := []rune(value)
	words := make([]string, 0)
	var current strings.Builder
	flush := func() {
		if current.Len() == 0 {
			return
		}
		words = append(words, current.String())
		current.Reset()
	}
	for index, character := range characters {
		if !unicode.IsLetter(character) && !unicode.IsNumber(character) {
			flush()
			continue
		}
		if current.Len() > 0 {
			previous := characters[index-1]
			lowerToUpper := (unicode.IsLower(previous) || unicode.IsNumber(previous)) && unicode.IsUpper(character)
			acronymEnd := unicode.IsUpper(previous) && unicode.IsUpper(character) && index+1 < len(characters) && unicode.IsLower(characters[index+1])
			letterToNumber := unicode.IsLetter(previous) && unicode.IsNumber(character)
			if lowerToUpper || acronymEnd || letterToNumber {
				flush()
			}
		}
		current.WriteRune(character)
	}
	flush()
	return words
}

func mapWhitespaceWords(value string, transform func(string) string) string {
	var result strings.Builder
	result.Grow(len(value))
	wordStart := 0
	for index, character := range value {
		if !unicode.IsSpace(character) {
			continue
		}
		result.WriteString(transform(value[wordStart:index]))
		result.WriteRune(character)
		wordStart = index + utf8.RuneLen(character)
	}
	result.WriteString(transform(value[wordStart:]))
	return result.String()
}

func capitalizeFirst(word string) string {
	if word == "" {
		return ""
	}
	_, size := utf8.DecodeRuneInString(word)
	return strings.ToUpper(word[:size]) + word[size:]
}

func capitalizeOnly(word string) string {
	if word == "" {
		return ""
	}
	_, size := utf8.DecodeRuneInString(word)
	return strings.ToUpper(word[:size]) + strings.ToLower(word[size:])
}
