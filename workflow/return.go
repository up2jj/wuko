package workflow

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// ReturnControl finishes the current workflow or composite action successfully with explicit outputs.
type ReturnControl struct {
	Outputs map[string]string `yaml:"outputs"`
}

// UnmarshalYAML strictly decodes a return declaration.
func (control *ReturnControl) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("return must be an object")
	}
	seen := make(map[string]struct{}, len(node.Content)/2)
	for i := 0; i < len(node.Content); i += 2 {
		name := node.Content[i].Value
		if _, exists := seen[name]; exists {
			return fmt.Errorf("duplicate return field %q", name)
		}
		seen[name] = struct{}{}
	}
	if err := rejectUnknownFields(node, "return", map[string]bool{"outputs": true}); err != nil {
		return err
	}
	outputsNode := mappingValue(node, "outputs")
	if outputsNode == nil {
		return fmt.Errorf("return outputs are required")
	}
	if outputsNode.Kind != yaml.MappingNode {
		return fmt.Errorf("return outputs must be an object")
	}
	outputs := make(map[string]string, len(outputsNode.Content)/2)
	for i := 0; i < len(outputsNode.Content); i += 2 {
		nameNode, expressionNode := outputsNode.Content[i], outputsNode.Content[i+1]
		if nameNode.Tag != "!!str" {
			return fmt.Errorf("return output names must be strings")
		}
		if expressionNode.Kind != yaml.ScalarNode || expressionNode.Tag != "!!str" {
			return fmt.Errorf("return output %q must be an expression string", nameNode.Value)
		}
		if _, exists := outputs[nameNode.Value]; exists {
			return fmt.Errorf("duplicate return output %q", nameNode.Value)
		}
		outputs[nameNode.Value] = expressionNode.Value
	}
	control.Outputs = outputs
	return nil
}

// Validate checks the return output contract.
func (control ReturnControl) Validate() error {
	if control.Outputs == nil {
		return fmt.Errorf("return outputs are required")
	}
	for name, expression := range control.Outputs {
		if !identifierPattern.MatchString(name) {
			return fmt.Errorf("invalid return output name %q", name)
		}
		if strings.TrimSpace(expression) == "" {
			return fmt.Errorf("return output %q must be a non-empty expression", name)
		}
	}
	return nil
}

// ValidateReturnControl checks a return declaration outside parallel and cleanup scopes.
func (workflowStep Step) ValidateReturnControl() error {
	if workflowStep.Return == nil {
		return fmt.Errorf("return declaration is missing")
	}
	return validateReturnEntry(workflowStep, scopeTop)
}

func validateReturnEntry(workflowStep Step, scope stepScope) error {
	if workflowStep.ID != "" || workflowStep.Type != "" || !workflowStep.Uses.Empty() || workflowStep.Require != nil || workflowStep.Worktree != nil || workflowStep.Executor != nil || workflowStep.Finally != nil || workflowStep.Defer != nil || workflowStep.IsWorkingDirectoryBlock() || workflowStep.Steps != nil || workflowStep.Concurrent != nil || workflowStep.Batch != nil || workflowStep.Foreach != nil || workflowStep.Matrix != nil || workflowStep.Once != nil || workflowStep.SHA256 != "" || workflowStep.Timeout != nil || workflowStep.Retry != nil || workflowStep.With != nil {
		return fmt.Errorf("return cannot be combined with other step fields")
	}
	switch scope {
	case scopeTop, scopeExecutor:
	case scopeControl:
		return fmt.Errorf("return is not supported inside foreach or matrix controls or inside batch controls")
	case scopeConcurrent:
		return fmt.Errorf("return is not supported inside concurrent groups")
	case scopeFinally, scopeExecutorFinally:
		return fmt.Errorf("return is not supported inside finally")
	case scopeDefer, scopeExecutorDefer:
		return fmt.Errorf("return is not supported inside defer")
	case scopeExecutorControl:
		return fmt.Errorf("return is not supported inside foreach or matrix controls or inside batch controls")
	}
	return workflowStep.Return.Validate()
}
