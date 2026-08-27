package workflow

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// TryBlock is the primary sequence of a named recovery control.
type TryBlock struct {
	Steps []Step `yaml:"steps"`
}

// CatchBlock is the rescue sequence of a named recovery control.
type CatchBlock struct {
	Steps []Step `yaml:"steps"`
}

func (block *TryBlock) UnmarshalYAML(node *yaml.Node) error {
	return decodeRecoveryBlock(node, "try", &block.Steps)
}

func (block *CatchBlock) UnmarshalYAML(node *yaml.Node) error {
	return decodeRecoveryBlock(node, "catch", &block.Steps)
}

func decodeRecoveryBlock(node *yaml.Node, name string, steps *[]Step) error {
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("%s must be an object", name)
	}
	if err := rejectUnknownFields(node, name, map[string]bool{"steps": true}); err != nil {
		return err
	}
	value := mappingValue(node, "steps")
	if value != nil && value.Kind != yaml.SequenceNode {
		return fmt.Errorf("%s steps must be a list", name)
	}
	var decoded struct {
		Steps []Step `yaml:"steps"`
	}
	if err := node.Decode(&decoded); err != nil {
		return err
	}
	*steps = decoded.Steps
	return nil
}

func validateTryCatchDeclaration(workflowStep Step) error {
	if workflowStep.Try == nil || workflowStep.Catch == nil {
		return fmt.Errorf("try and catch must be declared together")
	}
	if len(workflowStep.Try.Steps) == 0 {
		return fmt.Errorf("try must contain at least one step")
	}
	if len(workflowStep.Catch.Steps) == 0 {
		return fmt.Errorf("catch must contain at least one step")
	}
	return nil
}

func tryCatchContainsForbidden(steps []Step) error {
	for _, workflowStep := range steps {
		switch {
		case workflowStep.IsTryCatch():
			return fmt.Errorf("nested try/catch controls are not supported")
		case workflowStep.Return != nil:
			return fmt.Errorf("return is not supported inside try/catch")
		}
		for _, child := range workflowStep.ChildSequences() {
			if child.Role == ChildDefer || child.Role == ChildFinally {
				continue
			}
			if err := tryCatchContainsForbidden(child.Steps); err != nil {
				return err
			}
		}
	}
	return nil
}
