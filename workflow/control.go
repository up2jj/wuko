package workflow

import (
	"fmt"
	"strings"

	controlpkg "github.com/up2jj/wuko/control"
	"gopkg.in/yaml.v3"
)

// ForeachGroup repeats a child step block for each item returned by an expression.
type ForeachGroup struct {
	Items          string    `yaml:"items"`
	Steps          []Step    `yaml:"steps"`
	MaxConcurrency int       `yaml:"max_concurrency,omitempty"`
	MaxIterations  int       `yaml:"max_iterations,omitempty"`
	Timeout        *Duration `yaml:"timeout,omitempty"`
	FailFast       bool      `yaml:"fail_fast"`
}

func (group *ForeachGroup) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("foreach must be an object")
	}
	if err := rejectUnknownFields(node, "foreach", map[string]bool{
		"items": true, "steps": true, "max_concurrency": true, "max_iterations": true, "timeout": true, "fail_fast": true,
	}); err != nil {
		return err
	}
	type plain ForeachGroup
	decoded := plain{MaxConcurrency: 1, MaxIterations: controlpkg.DefaultMaxIterations, FailFast: true}
	if err := node.Decode(&decoded); err != nil {
		return err
	}
	if decoded.MaxIterations == 0 && hasMappingField(node, "max_iterations") {
		return fmt.Errorf("foreach max_iterations must be between 1 and %d", controlpkg.MaxIterations)
	}
	*group = ForeachGroup(decoded)
	return nil
}

// MatrixAxis declares one ordered Cartesian dimension from literal values or an expression.
type MatrixAxis struct {
	Name       string
	Values     []any
	Expression string
}

// MatrixAxes preserves YAML declaration order for deterministic Cartesian expansion.
type MatrixAxes []MatrixAxis

func (axes *MatrixAxes) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("matrix axes must be an object")
	}
	result := make(MatrixAxes, 0, len(node.Content)/2)
	seen := make(map[string]struct{}, len(node.Content)/2)
	for i := 0; i < len(node.Content); i += 2 {
		nameNode, valueNode := node.Content[i], node.Content[i+1]
		if nameNode.Tag != "!!str" || !identifierPattern.MatchString(nameNode.Value) {
			return fmt.Errorf("invalid matrix axis name %q", nameNode.Value)
		}
		if _, exists := seen[nameNode.Value]; exists {
			return fmt.Errorf("duplicate matrix axis %q", nameNode.Value)
		}
		seen[nameNode.Value] = struct{}{}
		axis := MatrixAxis{Name: nameNode.Value}
		switch valueNode.Kind {
		case yaml.SequenceNode:
			if err := valueNode.Decode(&axis.Values); err != nil {
				return fmt.Errorf("matrix axis %q: %w", axis.Name, err)
			}
		case yaml.ScalarNode:
			if valueNode.Tag != "!!str" || strings.TrimSpace(valueNode.Value) == "" {
				return fmt.Errorf("matrix axis %q must be a list or non-empty expression", axis.Name)
			}
			axis.Expression = valueNode.Value
		default:
			return fmt.Errorf("matrix axis %q must be a list or non-empty expression", axis.Name)
		}
		result = append(result, axis)
	}
	*axes = result
	return nil
}

// MatrixGroup repeats a child step block for the Cartesian product of ordered axes.
type MatrixGroup struct {
	Axes           MatrixAxes `yaml:"axes"`
	Steps          []Step     `yaml:"steps"`
	MaxConcurrency int        `yaml:"max_concurrency,omitempty"`
	MaxIterations  int        `yaml:"max_iterations,omitempty"`
	Timeout        *Duration  `yaml:"timeout,omitempty"`
	FailFast       bool       `yaml:"fail_fast"`
}

func (group *MatrixGroup) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("matrix must be an object")
	}
	if err := rejectUnknownFields(node, "matrix", map[string]bool{
		"axes": true, "steps": true, "max_concurrency": true, "max_iterations": true, "timeout": true, "fail_fast": true,
	}); err != nil {
		return err
	}
	type plain MatrixGroup
	decoded := plain{MaxConcurrency: 1, MaxIterations: controlpkg.DefaultMaxIterations, FailFast: true}
	if err := node.Decode(&decoded); err != nil {
		return err
	}
	if decoded.MaxIterations == 0 && hasMappingField(node, "max_iterations") {
		return fmt.Errorf("matrix max_iterations must be between 1 and %d", controlpkg.MaxIterations)
	}
	*group = MatrixGroup(decoded)
	return nil
}

func rejectUnknownFields(node *yaml.Node, kind string, allowed map[string]bool) error {
	for i := 0; i < len(node.Content); i += 2 {
		if !allowed[node.Content[i].Value] {
			return fmt.Errorf("field %s not found in %s group", node.Content[i].Value, kind)
		}
	}
	return nil
}

func hasMappingField(node *yaml.Node, name string) bool {
	for i := 0; i < len(node.Content); i += 2 {
		if node.Content[i].Value == name {
			return true
		}
	}
	return false
}

func validateFanoutPolicy(kind string, childCount, maxConcurrency, maxIterations int, timeout *Duration) error {
	if childCount == 0 {
		return fmt.Errorf("%s group must contain at least one step", kind)
	}
	if maxConcurrency < 1 || maxConcurrency > 100 {
		return fmt.Errorf("%s max_concurrency must be between 1 and 100", kind)
	}
	if maxIterations == 0 {
		maxIterations = controlpkg.DefaultMaxIterations
	}
	if maxIterations < 1 || maxIterations > controlpkg.MaxIterations {
		return fmt.Errorf("%s max_iterations must be between 1 and %d", kind, controlpkg.MaxIterations)
	}
	if timeout != nil && timeout.Value() <= 0 {
		return fmt.Errorf("%s timeout must be greater than zero", kind)
	}
	return nil
}

// Validate checks the foreach declaration and execution limits.
func (group ForeachGroup) Validate() error {
	if strings.TrimSpace(group.Items) == "" {
		return fmt.Errorf("foreach items must be a non-empty expression")
	}
	return validateFanoutPolicy("foreach", len(group.Steps), group.MaxConcurrency, group.MaxIterations, group.Timeout)
}

// Validate checks the matrix declaration and execution limits.
func (group MatrixGroup) Validate() error {
	if len(group.Axes) == 0 {
		return fmt.Errorf("matrix requires at least one axis")
	}
	return validateFanoutPolicy("matrix", len(group.Steps), group.MaxConcurrency, group.MaxIterations, group.Timeout)
}
