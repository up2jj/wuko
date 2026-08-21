package workflow

import (
	"path/filepath"
	"strconv"
	"strings"

	"github.com/up2jj/wuko/diagnostic"
	"gopkg.in/yaml.v3"
)

func annotateDefinitionLocations(data []byte, definition *Definition, source string) {
	root := yamlRoot(data)
	if root == nil {
		return
	}
	definition.Location = nodeLocation(root, source)
	annotateSteps(definition.Steps, mappingValue(root, "steps"), source)
	annotateSteps(definition.Finally, mappingValue(root, "finally"), source)
}

func annotateActionLocations(data []byte, action *Action, source string) {
	root := yamlRoot(data)
	if root == nil {
		return
	}
	action.Location = nodeLocation(root, source)
	annotateSteps(action.Steps, mappingValue(root, "steps"), source)
	annotateSteps(action.Finally, mappingValue(root, "finally"), source)
}

func annotateFragmentLocations(data []byte, steps []Step, source string) {
	root := yamlRoot(data)
	if root == nil {
		return
	}
	if root.Kind == yaml.MappingNode {
		root = mappingValue(root, "steps")
	}
	annotateSteps(steps, root, source)
}

func yamlRoot(data []byte) *yaml.Node {
	var document yaml.Node
	if yaml.Unmarshal(data, &document) != nil || len(document.Content) != 1 {
		return nil
	}
	return document.Content[0]
}

func annotateSteps(steps []Step, sequence *yaml.Node, source string) {
	if sequence == nil || sequence.Kind != yaml.SequenceNode {
		return
	}
	for i := range min(len(steps), len(sequence.Content)) {
		node := sequence.Content[i]
		steps[i].Location = nodeLocation(node, source)
		if node.Kind != yaml.MappingNode {
			continue
		}
		if steps[i].IsWorkingDirectoryBlock() {
			annotateSteps(steps[i].Steps, mappingValue(node, "steps"), source)
		}
		if steps[i].IsConditionalBlock() {
			annotateSteps(steps[i].Steps, mappingValue(node, "steps"), source)
		}
		if steps[i].Concurrent != nil {
			group := mappingValue(node, "concurrent")
			annotateSteps(steps[i].Concurrent.Steps, mappingValue(group, "steps"), source)
		}
		if steps[i].Foreach != nil {
			group := mappingValue(node, "foreach")
			annotateSteps(steps[i].Foreach.Steps, mappingValue(group, "steps"), source)
		}
		if steps[i].Matrix != nil {
			group := mappingValue(node, "matrix")
			annotateSteps(steps[i].Matrix.Steps, mappingValue(group, "steps"), source)
		}
	}
}

func mappingValue(mapping *yaml.Node, key string) *yaml.Node {
	if mapping == nil || mapping.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			return mapping.Content[i+1]
		}
	}
	return nil
}

func nodeLocation(node *yaml.Node, source string) diagnostic.Location {
	if node == nil {
		return diagnostic.Location{Source: source}
	}
	return diagnostic.Location{Source: source, Line: node.Line, Column: node.Column}
}

func remapDefinitionLocations(definition *Definition, materializedRoot, logicalSource string) {
	definition.Location.Source = remapSource(definition.Location.Source, materializedRoot, logicalSource)
	remapStepLocations(definition.Steps, materializedRoot, logicalSource)
	remapStepLocations(definition.Finally, materializedRoot, logicalSource)
}

func remapStepLocations(steps []Step, materializedRoot, logicalSource string) {
	for i := range steps {
		steps[i].Location.Source = remapSource(steps[i].Location.Source, materializedRoot, logicalSource)
		if steps[i].IsWorkingDirectoryBlock() {
			remapStepLocations(steps[i].Steps, materializedRoot, logicalSource)
		}
		if steps[i].IsConditionalBlock() {
			remapStepLocations(steps[i].Steps, materializedRoot, logicalSource)
		}
		if steps[i].Concurrent != nil {
			remapStepLocations(steps[i].Concurrent.Steps, materializedRoot, logicalSource)
		}
		if steps[i].Foreach != nil {
			remapStepLocations(steps[i].Foreach.Steps, materializedRoot, logicalSource)
		}
		if steps[i].Matrix != nil {
			remapStepLocations(steps[i].Matrix.Steps, materializedRoot, logicalSource)
		}
	}
}

func remapSource(source, root, logical string) string {
	if source == "" {
		return logical
	}
	relative, err := filepath.Rel(root, source)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return source
	}
	if filepath.ToSlash(relative) == defaultRemoteWorkflowFile {
		return logical
	}
	return logical + "::" + filepath.ToSlash(relative)
}

func validationLocation(definition *Definition, err error) diagnostic.Location {
	if err == nil {
		return definition.Location
	}
	message := err.Error()
	indexedSteps := definition.Steps
	indexedMessage := message
	if strings.HasPrefix(message, "finally: ") {
		indexedSteps = definition.Finally
		indexedMessage = strings.TrimPrefix(message, "finally: ")
	}
	allSteps := append(flattenSteps(definition.Steps), flattenSteps(definition.Finally)...)
	for _, workflowStep := range allSteps {
		quotedID := strconv.Quote(workflowStep.ID)
		if workflowStep.ID != "" && (strings.Contains(message, "step "+quotedID) || strings.Contains(message, "step id "+quotedID)) {
			return workflowStep.Location
		}
	}
	if strings.HasPrefix(indexedMessage, "step ") {
		number, _, found := strings.Cut(strings.TrimPrefix(indexedMessage, "step "), ":")
		if found {
			index, parseErr := strconv.Atoi(number)
			if parseErr == nil && index > 0 && index <= len(indexedSteps) {
				return indexedSteps[index-1].Location
			}
		}
	}
	return definition.Location
}

func flattenSteps(steps []Step) []Step {
	var flattened []Step
	for _, workflowStep := range steps {
		if workflowStep.IsWorkingDirectoryBlock() {
			flattened = append(flattened, flattenSteps(workflowStep.Steps)...)
			continue
		}
		if workflowStep.IsConditionalBlock() {
			flattened = append(flattened, flattenSteps(workflowStep.Steps)...)
			continue
		}
		if workflowStep.Concurrent != nil {
			flattened = append(flattened, flattenSteps(workflowStep.Concurrent.Steps)...)
			continue
		}
		if workflowStep.Foreach != nil {
			flattened = append(flattened, workflowStep)
			flattened = append(flattened, flattenSteps(workflowStep.Foreach.Steps)...)
			continue
		}
		if workflowStep.Matrix != nil {
			flattened = append(flattened, workflowStep)
			flattened = append(flattened, flattenSteps(workflowStep.Matrix.Steps)...)
			continue
		}
		flattened = append(flattened, workflowStep)
	}
	return flattened
}
