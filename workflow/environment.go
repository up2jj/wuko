package workflow

import (
	"fmt"
	"maps"
	"path"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

// ScopedEnvironment is an ordered sequence of environment changes. A YAML map is
// shorthand for a single set operation; workflow-level Environment stays string-only.
type ScopedEnvironment []EnvironmentOperation

// EnvironmentOperation either sets named values or removes matching names.
type EnvironmentOperation struct {
	Set       map[string]EnvironmentValue `yaml:"set,omitempty"`
	Unset     *EnvironmentUnset           `yaml:"unset,omitempty"`
	shorthand bool
}

// EnvironmentValue holds a string, a removal (nil Literal), or an Expr expression.
type EnvironmentValue struct {
	Literal    *string
	Expression string
}

// EnvironmentUnset selects names with literal/template patterns or an Expr list.
type EnvironmentUnset struct {
	Patterns   []string
	Expression string
}

// ScopedEnvironmentValues constructs the flat-map shorthand programmatically.
func ScopedEnvironmentValues(values Environment) ScopedEnvironment {
	set := make(map[string]EnvironmentValue, len(values))
	for name, value := range values {
		set[name] = EnvironmentValue{Literal: &value}
	}
	return ScopedEnvironment{{Set: set, shorthand: true}}
}

func (environment *ScopedEnvironment) UnmarshalYAML(node *yaml.Node) error {
	var operations ScopedEnvironment
	switch node.Kind {
	case yaml.MappingNode:
		var set map[string]EnvironmentValue
		if err := decodeEnvironmentSet(node, &set); err != nil {
			return err
		}
		operations = ScopedEnvironment{{Set: set, shorthand: true}}
	case yaml.SequenceNode:
		operations = make(ScopedEnvironment, len(node.Content))
		for i, item := range node.Content {
			if item.Kind != yaml.MappingNode || len(item.Content) != 2 {
				return fmt.Errorf("env operation %d must contain exactly one of set or unset", i+1)
			}
			switch item.Content[0].Value {
			case "set":
				if err := decodeEnvironmentSet(item.Content[1], &operations[i].Set); err != nil {
					return fmt.Errorf("env operation %d set: %w", i+1, err)
				}
			case "unset":
				operations[i].Unset = &EnvironmentUnset{}
				if err := operations[i].Unset.UnmarshalYAML(item.Content[1]); err != nil {
					return fmt.Errorf("env operation %d unset: %w", i+1, err)
				}
			default:
				return fmt.Errorf("env operation %d must contain exactly one of set or unset", i+1)
			}
		}
	default:
		return fmt.Errorf("environment must be an object or a list of operations")
	}
	if err := operations.Validate(); err != nil {
		return err
	}
	*environment = operations
	return nil
}

func decodeEnvironmentSet(node *yaml.Node, set *map[string]EnvironmentValue) error {
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("environment set must be an object")
	}
	for i := 0; i < len(node.Content); i += 2 {
		if node.Content[i].Tag != "!!str" {
			return fmt.Errorf("environment names must be strings")
		}
	}
	return node.Decode(set)
}

func (value *EnvironmentValue) UnmarshalYAML(node *yaml.Node) error {
	*value = EnvironmentValue{}
	switch {
	case node.Kind == yaml.ScalarNode && node.Tag == "!!null":
		return nil
	case node.Kind == yaml.ScalarNode && node.Tag == "!!str":
		literal := node.Value
		value.Literal = &literal
		return nil
	case isEnvironmentExpression(node):
		value.Expression = node.Content[1].Value
		return nil
	default:
		return fmt.Errorf("environment values must be strings, null, or a non-empty expr object")
	}
}

func isEnvironmentExpression(node *yaml.Node) bool {
	return node.Kind == yaml.MappingNode && len(node.Content) == 2 &&
		node.Content[0].Tag == "!!str" && node.Content[0].Value == "expr" &&
		node.Content[1].Tag == "!!str" && strings.TrimSpace(node.Content[1].Value) != ""
}

func (unset *EnvironmentUnset) UnmarshalYAML(node *yaml.Node) error {
	*unset = EnvironmentUnset{}
	if isEnvironmentExpression(node) {
		unset.Expression = node.Content[1].Value
		return nil
	}
	if node.Kind != yaml.SequenceNode || len(node.Content) == 0 {
		return fmt.Errorf("unset must be a non-empty string list or a non-empty expr object")
	}
	for _, item := range node.Content {
		if item.Kind != yaml.ScalarNode || item.Tag != "!!str" {
			return fmt.Errorf("unset entries must be strings")
		}
		unset.Patterns = append(unset.Patterns, item.Value)
	}
	return nil
}

// Validate checks declarations constructed through YAML or Go.
func (environment ScopedEnvironment) Validate() error {
	if len(environment) == 0 {
		return fmt.Errorf("env block must contain at least one variable or operation")
	}
	for i, operation := range environment {
		if (operation.Set == nil) == (operation.Unset == nil) {
			return fmt.Errorf("env operation %d must contain exactly one of set or unset", i+1)
		}
		if operation.Set != nil {
			if len(operation.Set) == 0 {
				return fmt.Errorf("env set must contain at least one variable")
			}
			for _, name := range slices.Sorted(maps.Keys(operation.Set)) {
				if !ValidEnvironmentName(name) {
					return fmt.Errorf("invalid environment name %q", name)
				}
				value := operation.Set[name]
				if value.Expression != "" && (strings.TrimSpace(value.Expression) == "" || value.Literal != nil) {
					return fmt.Errorf("env %q must contain a literal or a non-empty expression", name)
				}
			}
			continue
		}
		unset := operation.Unset
		if unset.Expression != "" {
			if strings.TrimSpace(unset.Expression) == "" || unset.Patterns != nil {
				return fmt.Errorf("unset must contain patterns or a non-empty expression")
			}
			continue
		}
		if len(unset.Patterns) == 0 {
			return fmt.Errorf("unset must contain at least one pattern")
		}
		for index, pattern := range unset.Patterns {
			if !strings.Contains(pattern, "{{") {
				if err := ValidateEnvironmentPattern(pattern); err != nil {
					return fmt.Errorf("env operation %d unset item %d: %w", i+1, index+1, err)
				}
			}
		}
	}
	return nil
}

// ValidateEnvironmentPattern validates a selector without including its potentially
// sensitive rendered contents in an error.
func ValidateEnvironmentPattern(pattern string) error {
	if pattern == "" {
		return fmt.Errorf("unset pattern must not be empty")
	}
	if _, err := path.Match(pattern, ""); err != nil {
		return fmt.Errorf("unset pattern is malformed: %w", err)
	}
	return nil
}

// WalkSources visits every Expr expression (isExpression) and template string in
// declaration order, labelled for error messages, so validators share one traversal.
func (environment ScopedEnvironment) WalkSources(visit func(label, source string, isExpression bool) error) error {
	for index, operation := range environment {
		for _, name := range slices.Sorted(maps.Keys(operation.Set)) {
			value := operation.Set[name]
			label := fmt.Sprintf("env operation %d set %q", index+1, name)
			var err error
			if value.Expression != "" {
				err = visit(label, value.Expression, true)
			} else if value.Literal != nil {
				err = visit(label, *value.Literal, false)
			}
			if err != nil {
				return err
			}
		}
		if operation.Unset == nil {
			continue
		}
		label := fmt.Sprintf("env operation %d unset", index+1)
		if operation.Unset.Expression != "" {
			if err := visit(label, operation.Unset.Expression, true); err != nil {
				return err
			}
		}
		for item, pattern := range operation.Unset.Patterns {
			if err := visit(fmt.Sprintf("%s item %d", label, item+1), pattern, false); err != nil {
				return err
			}
		}
	}
	return nil
}

// Label describes declarations for tree and dry-run output without rendering values.
func (environment ScopedEnvironment) Label() string {
	if len(environment) == 1 && environment[0].shorthand {
		return "env: " + strings.Join(slices.Sorted(maps.Keys(environment[0].Set)), ", ")
	}
	labels := make([]string, 0, len(environment))
	for _, operation := range environment {
		if operation.Set != nil {
			labels = append(labels, "set: "+strings.Join(slices.Sorted(maps.Keys(operation.Set)), ", "))
			continue
		}
		patterns := []string{"<dynamic>"}
		if operation.Unset != nil && operation.Unset.Expression == "" {
			patterns = slices.Clone(operation.Unset.Patterns)
			for i, pattern := range patterns {
				if strings.Contains(pattern, "{{") {
					patterns[i] = "<dynamic>"
				}
			}
		}
		labels = append(labels, "unset: "+strings.Join(patterns, ", "))
	}
	return "env: [" + strings.Join(labels, "; ") + "]"
}

func (value EnvironmentValue) MarshalYAML() (any, error) {
	if value.Expression != "" {
		return map[string]string{"expr": value.Expression}, nil
	}
	return value.Literal, nil
}

func (unset EnvironmentUnset) MarshalYAML() (any, error) {
	if unset.Expression != "" {
		return map[string]string{"expr": unset.Expression}, nil
	}
	return unset.Patterns, nil
}
