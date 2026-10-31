package reference

import (
	"fmt"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Semantic annotations describe unconditional requirements and reviewed defaults.
// They complement native config types rather than implementing another validator.
type semantics struct {
	Required []string            `yaml:"required"`
	Rules    []string            `yaml:"rules"`
	Defaults map[string]any      `yaml:"defaults"`
	Choices  map[string][]string `yaml:"choices"`
}

func readSemantics() (map[string]semantics, error) {
	data, err := exampleFiles.ReadFile("metadata.yaml")
	if err != nil {
		return nil, err
	}
	var metadata map[string]semantics
	decoder := yaml.NewDecoder(strings.NewReader(string(data)))
	decoder.KnownFields(true)
	if err := decoder.Decode(&metadata); err != nil {
		return nil, fmt.Errorf("reading step metadata: %w", err)
	}
	return metadata, nil
}

func annotate(fields []Field, spec semantics) ([]Field, error) {
	known := map[string]bool{}
	for i := range fields {
		field := &fields[i]
		known[field.Name] = true
		for _, name := range spec.Required {
			if name == field.Name {
				required := true
				field.Required = &required
			}
		}
		field.Default = spec.Defaults[field.Name]
		field.Choices = spec.Choices[field.Name]
	}
	for _, name := range spec.Required {
		if !known[name] {
			return nil, fmt.Errorf("required metadata names unknown field %q", name)
		}
	}
	for name := range spec.Defaults {
		if !known[name] {
			return nil, fmt.Errorf("default metadata names unknown field %q", name)
		}
	}
	for name := range spec.Choices {
		if !known[name] {
			return nil, fmt.Errorf("choice metadata names unknown field %q", name)
		}
	}
	return fields, nil
}
