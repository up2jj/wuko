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
	annotateSteps(definition.Install, mappingValue(root, "install"), source)
	annotateSteps(definition.Uninstall, mappingValue(root, "uninstall"), source)
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
		steps[i].sourcePath = source
		if node.Kind != yaml.MappingNode {
			continue
		}
		if steps[i].IsExecutorBlock() {
			annotateSteps(steps[i].Steps, mappingValue(node, "steps"), source)
			annotateSteps(steps[i].Finally, mappingValue(node, "finally"), source)
		}
		annotateSteps(steps[i].Defer, mappingValue(node, "defer"), source)
		if steps[i].IsWorkingDirectoryBlock() {
			annotateSteps(steps[i].Steps, mappingValue(node, "steps"), source)
		}
		if steps[i].IsWorktreeBlock() {
			group := mappingValue(node, "worktree")
			annotateSteps(steps[i].Worktree.Steps, mappingValue(group, "steps"), source)
		}
		if steps[i].IsConditionalBlock() {
			annotateSteps(steps[i].Steps, mappingValue(node, "steps"), source)
		}
		if steps[i].Concurrent != nil {
			group := mappingValue(node, "concurrent")
			annotateSteps(steps[i].Concurrent.Steps, mappingValue(group, "steps"), source)
		}
		if steps[i].Batch != nil {
			group := mappingValue(node, "batch")
			annotateSteps(steps[i].Batch.Steps, mappingValue(group, "steps"), source)
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
	remapStepLocations(definition.Install, materializedRoot, logicalSource)
	remapStepLocations(definition.Uninstall, materializedRoot, logicalSource)
}

func remapStepLocations(steps []Step, materializedRoot, logicalSource string) {
	for i := range steps {
		steps[i].Location.Source = remapSource(steps[i].Location.Source, materializedRoot, logicalSource)
		for _, child := range steps[i].ChildSequences() {
			remapStepLocations(child.Steps, materializedRoot, logicalSource)
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
	for i := len(allSteps) - 1; i >= 0; i-- {
		workflowStep := allSteps[i]
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
		children := workflowStep.ChildSequences()
		if len(children) == 0 {
			flattened = append(flattened, workflowStep)
			continue
		}
		if !workflowStep.IsExecutorBlock() && !workflowStep.IsWorkingDirectoryBlock() && !workflowStep.IsConditionalBlock() && workflowStep.Concurrent == nil {
			flattened = append(flattened, workflowStep)
		}
		for _, child := range children {
			flattened = append(flattened, flattenSteps(child.Steps)...)
		}
	}
	return flattened
}
