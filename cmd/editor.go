package cmd

import (
	"fmt"
	"strings"
	"unicode"
)

// splitEditorCommand handles quoted arguments without shell expansion. Backslashes
// escape quotes, whitespace, and backslashes; other backslashes are preserved so
// Windows paths work without requiring every separator to be doubled.
func splitEditorCommand(spec string) ([]string, error) {
	var args []string
	var word strings.Builder
	var quote rune
	started := false
	characters := []rune(spec)
	for i := 0; i < len(characters); i++ {
		character := characters[i]
		if character == '\\' && quote != '\'' {
			if i+1 == len(characters) {
				return nil, fmt.Errorf("unfinished escape")
			}
			next := characters[i+1]
			if next == '\\' || next == '"' || (quote == 0 && (next == '\'' || unicode.IsSpace(next))) {
				word.WriteRune(next)
				started = true
				i++
				continue
			}
		}
		if quote != 0 {
			if character == quote {
				quote = 0
			} else {
				word.WriteRune(character)
			}
			continue
		}
		switch {
		case character == '\'' || character == '"':
			quote = character
			started = true
		case unicode.IsSpace(character):
			if started {
				args = append(args, word.String())
				word.Reset()
				started = false
			}
		default:
			word.WriteRune(character)
			started = true
		}
	}
	if quote != 0 {
		return nil, fmt.Errorf("unterminated quote")
	}
	if started {
		args = append(args, word.String())
	}
	if len(args) == 0 || args[0] == "" {
		return nil, fmt.Errorf("editor command is empty")
	}
	return args, nil
}
