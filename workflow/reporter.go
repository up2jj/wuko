package workflow

import (
	"fmt"
	"strings"

	"github.com/up2jj/wuko/internal/yamlstrict"
	"go.yaml.in/yaml/v3"
)

// ReporterConfig selects a reporter and supplies shared rendering preferences.
// Empty Type leaves reporter selection to the caller (plain in the CLI).
type ReporterConfig struct {
	Type        string   `yaml:"type,omitempty"`
	Quiet       []string `yaml:"quiet,omitempty"`
	HideSkipped bool     `yaml:"hide_skipped,omitempty"`
}

func (config *ReporterConfig) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode && node.Tag == "!!str" {
		if strings.TrimSpace(node.Value) == "" {
			return fmt.Errorf("reporter must not be empty")
		}
		*config = ReporterConfig{Type: node.Value}
		return nil
	}
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("reporter must be a name or an object")
	}
	for index := 0; index < len(node.Content); index += 2 {
		name, value := node.Content[index].Value, node.Content[index+1]
		switch name {
		case "type":
			if value.Kind != yaml.ScalarNode || value.Tag != "!!str" || strings.TrimSpace(value.Value) == "" {
				return fmt.Errorf("reporter type must be a non-empty name")
			}
		case "hide_skipped":
			if value.Kind != yaml.ScalarNode || value.Tag != "!!bool" {
				return fmt.Errorf("reporter hide_skipped must be a boolean")
			}
		case "quiet":
			if value.Kind != yaml.SequenceNode {
				return fmt.Errorf("reporter quiet must be a list of step types")
			}
			for _, item := range value.Content {
				if item.Kind != yaml.ScalarNode || item.Tag != "!!str" {
					return fmt.Errorf("reporter quiet must contain step type names")
				}
			}
		}
	}
	type plain ReporterConfig
	var value plain
	if err := yamlstrict.Convert(node, &value); err != nil {
		return err
	}
	for _, name := range value.Quiet {
		if strings.TrimSpace(name) == "" {
			return fmt.Errorf("reporter quiet step types must not be empty")
		}
	}
	*config = ReporterConfig(value)
	return nil
}
