package workflow

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// CancelOnGroup races one body against named monitor branches and records the outcome.
type CancelOnGroup struct {
	Monitors []Step `yaml:"monitors"`
	Steps    []Step `yaml:"steps"`
	Collect  string `yaml:"collect,omitempty"`
}

func (group *CancelOnGroup) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("cancel_on must be an object")
	}
	if err := rejectUnknownFields(node, "cancel_on", map[string]bool{
		"monitors": true, "steps": true, "collect": true,
	}); err != nil {
		return err
	}
	for _, name := range []string{"monitors", "steps"} {
		value := mappingValue(node, name)
		if value != nil && value.Kind != yaml.SequenceNode {
			return fmt.Errorf("cancel_on %s must be a list", name)
		}
	}
	if collect := mappingValue(node, "collect"); collect != nil && (collect.Kind != yaml.ScalarNode || collect.Tag != "!!str") {
		return fmt.Errorf("cancel_on collect must be an expression string")
	}
	type plain CancelOnGroup
	var decoded plain
	if err := node.Decode(&decoded); err != nil {
		return err
	}
	*group = CancelOnGroup(decoded)
	return nil
}

// Validate checks the declaration independently of runtime state.
func (group CancelOnGroup) Validate() error {
	if len(group.Monitors) == 0 {
		return fmt.Errorf("cancel_on must contain at least one monitor")
	}
	if len(group.Steps) == 0 {
		return fmt.Errorf("cancel_on must contain at least one body step")
	}
	if group.Collect != "" && strings.TrimSpace(group.Collect) == "" {
		return fmt.Errorf("cancel_on collect must be a non-empty expression")
	}
	seen := make(map[string]struct{}, len(group.Monitors))
	for index, monitor := range group.Monitors {
		if !identifierPattern.MatchString(monitor.ID) {
			return fmt.Errorf("cancel_on monitor %d requires a valid id", index+1)
		}
		if _, exists := seen[monitor.ID]; exists {
			return fmt.Errorf("duplicate cancel_on monitor id %q", monitor.ID)
		}
		seen[monitor.ID] = struct{}{}
	}
	return nil
}

func cancelOnMonitorDeclaration(step Step) Step {
	if step.IsExecutorBlock() || step.IsWorkingDirectoryBlock() || step.IsConditionalBlock() || step.Concurrent != nil {
		step.ID = ""
	}
	return step
}

// MonitorDeclaration returns the executable declaration for a labeled monitor.
// Labels on normally anonymous blocks belong to cancel_on rather than the block itself.
func (group CancelOnGroup) MonitorDeclaration(index int) Step {
	return cancelOnMonitorDeclaration(group.Monitors[index])
}

func cancelOnContainsForbidden(steps []Step) error {
	for _, step := range steps {
		switch {
		case step.CancelOn != nil:
			return fmt.Errorf("nested cancel_on controls are not supported")
		case step.IsTryCatch():
			return fmt.Errorf("try/catch is not supported inside cancel_on")
		case step.Return != nil:
			return fmt.Errorf("return is not supported inside cancel_on")
		case step.Require != nil:
			return fmt.Errorf("require is not supported inside cancel_on")
		case step.Defer != nil:
			return fmt.Errorf("defer is not supported inside cancel_on")
		}
		for _, child := range step.ChildSequences() {
			if err := cancelOnContainsForbidden(child.Steps); err != nil {
				return err
			}
		}
	}
	return nil
}
