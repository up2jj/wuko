// Package dataformat parses YAML and TOML documents for every surface that reads them as
// data, so the decode step and Lua report the same errors with the same locations.
package dataformat

import (
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/pelletier/go-toml/v2"
	"go.yaml.in/yaml/v3"
)

var yamlLinePattern = regexp.MustCompile(`(?:^|\s)line ([0-9]+)(?::|\s|$)`)

// DecodeYAML parses exactly one YAML document into yaml.v3's generic values. Strict parsing
// also requires UTF-8, rejects tags outside the YAML core schema, and treats empty input as a
// null document instead of an error.
func DecodeYAML(text string, strict bool) (any, error) {
	if strict && !utf8.ValidString(text) {
		return nil, fmt.Errorf("YAML must be UTF-8")
	}
	decoder := yaml.NewDecoder(strings.NewReader(text))
	var document yaml.Node
	if err := decoder.Decode(&document); err != nil {
		if strict && err == io.EOF {
			return nil, nil
		}
		return nil, yamlLocationError(text, err)
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			line, column := 1, 1
			if len(extra.Content) > 0 {
				line, column = extra.Content[0].Line, extra.Content[0].Column
			}
			return nil, fmt.Errorf("line %d, column %d: multiple YAML documents are not supported", line, column)
		}
		return nil, yamlLocationError(text, err)
	}
	if strict {
		if err := validateYAMLTags(&document); err != nil {
			return nil, err
		}
	}
	var value any
	if err := document.Decode(&value); err != nil {
		return nil, yamlLocationError(text, err)
	}
	return value, nil
}

func validateYAMLTags(node *yaml.Node) error {
	switch node.Tag {
	case "", "!!map", "!!seq", "!!str", "!!bool", "!!int", "!!float", "!!null", "!!timestamp", "!!binary", "!!set", "!!omap", "!!pairs", "!!merge":
	default:
		return fmt.Errorf("line %d: unsupported YAML tag %q", node.Line, node.Tag)
	}
	for _, child := range node.Content {
		if err := validateYAMLTags(child); err != nil {
			return err
		}
	}
	return nil
}

// DecodeTOML parses a TOML document into go-toml's generic values.
func DecodeTOML(text string) (map[string]any, error) {
	value := make(map[string]any)
	if err := toml.Unmarshal([]byte(text), &value); err != nil {
		return nil, tomlLocationError(err)
	}
	return value, nil
}

func tomlLocationError(err error) error {
	var decodeErr *toml.DecodeError
	if errors.As(err, &decodeErr) {
		line, column := decodeErr.Position()
		return fmt.Errorf("line %d, column %d: %w", line, column, err)
	}
	return err
}

// yamlLocationError prefixes a yaml.v3 error with its line and that line's end column, since
// yaml.v3 reports lines only inside its message.
func yamlLocationError(text string, err error) error {
	line := 1
	if match := yamlLinePattern.FindStringSubmatch(err.Error()); len(match) == 2 {
		line, _ = strconv.Atoi(match[1])
	}
	column := lineEndColumn(text, line)
	return fmt.Errorf("line %d, column %d: %w", line, column, err)
}

func lineEndColumn(text string, line int) int {
	if line < 1 {
		return 1
	}
	lines := strings.Split(text, "\n")
	if line > len(lines) {
		line = len(lines)
	}
	if line == 0 {
		return 1
	}
	return utf8.RuneCountInString(strings.TrimSuffix(lines[line-1], "\r")) + 1
}
