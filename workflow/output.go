package workflow

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// WorkflowOutput declares one typed value exported after a successful workflow run.
type WorkflowOutput struct {
	Type        string `yaml:"type"`
	Description string `yaml:"description,omitempty"`
	Value       string `yaml:"value"`
}

// UnmarshalYAML strictly decodes a workflow output declaration.
func (output *WorkflowOutput) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("workflow output must be an object")
	}
	if err := rejectUnknownFields(node, "workflow output", map[string]bool{
		"type": true, "description": true, "value": true,
	}); err != nil {
		return err
	}
	type plain WorkflowOutput
	var decoded plain
	if err := node.Decode(&decoded); err != nil {
		return err
	}
	*output = WorkflowOutput(decoded)
	return nil
}

// Validate checks an output declaration independently of runtime state.
func (output WorkflowOutput) Validate(name string) error {
	if !identifierPattern.MatchString(name) {
		return fmt.Errorf("invalid output name %q", name)
	}
	if !SupportedOutputType(output.Type) {
		return fmt.Errorf("output %q has unsupported type %q", name, output.Type)
	}
	if strings.TrimSpace(output.Value) == "" {
		return fmt.Errorf("output %q value is required", name)
	}
	return nil
}

// SupportedOutputType reports whether kind is part of the workflow output contract.
func SupportedOutputType(kind string) bool {
	switch kind {
	case "string", "boolean", "number", "array", "object":
		return true
	default:
		return false
	}
}

// OutputValueMatches reports whether value satisfies a declared workflow output type.
func OutputValueMatches(kind string, value any) bool {
	return actionValueMatches(kind, value)
}

// OutputPlaceholder returns a deterministic validation value for a declared type.
func OutputPlaceholder(kind string) any {
	switch kind {
	case "string":
		return ""
	case "boolean":
		return false
	case "number":
		return 0
	case "array":
		return []any{}
	case "object":
		return map[string]any{}
	default:
		return nil
	}
}

// OutputPlaceholders returns validation values for a workflow output contract.
func OutputPlaceholders(outputs map[string]WorkflowOutput) map[string]any {
	values := make(map[string]any, len(outputs))
	for name, output := range outputs {
		values[name] = OutputPlaceholder(output.Type)
	}
	return values
}

// ValidateOutputContract checks declared outputs and early returns against the contract.
func (definition *Definition) ValidateOutputContract() error {
	for name, output := range definition.Outputs {
		if err := output.Validate(name); err != nil {
			return err
		}
	}
	if len(definition.Outputs) == 0 {
		return nil
	}
	return validateWorkflowReturnContracts(definition.Steps, definition.Outputs)
}

func validateWorkflowReturnContracts(steps []Step, outputs map[string]WorkflowOutput) error {
	for _, workflowStep := range steps {
		if workflowStep.IsExecutorBlock() || workflowStep.IsEnvironmentBlock() || workflowStep.IsWorkingDirectoryBlock() || workflowStep.IsWorktreeBlock() || workflowStep.IsConditionalBlock() || workflowStep.IsCancelOn() || workflowStep.IsTryCatch() {
			for _, child := range workflowStep.ChildSequences() {
				if child.Role == ChildFinally || child.Role == ChildDefer {
					continue
				}
				if err := validateWorkflowReturnContracts(child.Steps, outputs); err != nil {
					return err
				}
			}
			continue
		}
		if workflowStep.Return == nil {
			continue
		}
		for name := range outputs {
			if _, exists := workflowStep.Return.Outputs[name]; !exists {
				return fmt.Errorf("return outputs do not match workflow outputs: missing %q", name)
			}
		}
		for name := range workflowStep.Return.Outputs {
			if _, exists := outputs[name]; !exists {
				return fmt.Errorf("return outputs do not match workflow outputs: unexpected %q", name)
			}
		}
	}
	return nil
}
