package workflow

import (
	"path/filepath"
	"strings"

	"github.com/up2jj/wuko/diagnostic"
	"github.com/up2jj/wuko/validation"
	"gopkg.in/yaml.v3"
)

func annotateDefinitionLocations(data []byte, definition *Definition, source string) {
	root := yamlRoot(data)
	if root == nil {
		return
	}
	definition.Location = nodeLocation(root, source)
	annotateStepsAt(definition.Steps, mappingValue(root, "steps"), source, "steps")
	annotateStepsAt(definition.Finally, mappingValue(root, "finally"), source, "finally")
	targets := mappingValue(root, "targets")
	for name, target := range definition.Targets {
		targetNode := mappingValue(targets, name)
		base := validation.Path("targets").Field(name)
		annotateStepsAt(target.Steps, mappingValue(targetNode, "steps"), source, base.Field("steps"))
		annotateStepsAt(target.Finally, mappingValue(targetNode, "finally"), source, base.Field("finally"))
	}
	annotateStepsAt(definition.Install, mappingValue(root, "install"), source, "install")
	annotateStepsAt(definition.Uninstall, mappingValue(root, "uninstall"), source, "uninstall")
}

func annotateActionLocations(data []byte, action *Action, source string) {
	root := yamlRoot(data)
	if root == nil {
		return
	}
	action.Location = nodeLocation(root, source)
	annotateStepsAt(action.Steps, mappingValue(root, "steps"), source, "steps")
	annotateStepsAt(action.Finally, mappingValue(root, "finally"), source, "finally")
}

func annotateFragmentLocations(data []byte, steps []Step, source string) {
	root := yamlRoot(data)
	if root == nil {
		return
	}
	if root.Kind == yaml.MappingNode {
		root = mappingValue(root, "steps")
	}
	annotateStepsAt(steps, root, source, "steps")
}

func yamlRoot(data []byte) *yaml.Node {
	var document yaml.Node
	if yaml.Unmarshal(data, &document) != nil || len(document.Content) != 1 {
		return nil
	}
	return document.Content[0]
}

func annotateSteps(steps []Step, sequence *yaml.Node, source string) {
	annotateStepsAt(steps, sequence, source, "steps")
}

func annotateStepsAt(steps []Step, sequence *yaml.Node, source string, base validation.Path) {
	if sequence == nil || sequence.Kind != yaml.SequenceNode {
		return
	}
	for i := range min(len(steps), len(sequence.Content)) {
		node := sequence.Content[i]
		steps[i].Location = nodeLocation(node, source)
		steps[i].sourcePath = source
		steps[i].validationPath = base.Index(i)
		current := steps[i].validationPath
		if node.Kind != yaml.MappingNode {
			continue
		}
		if steps[i].IsExecutorBlock() {
			annotateStepsAt(steps[i].Steps, mappingValue(node, "steps"), source, current.Field("steps"))
			annotateStepsAt(steps[i].Finally, mappingValue(node, "finally"), source, current.Field("finally"))
		}
		if steps[i].IsCancelOn() {
			group := mappingValue(node, "cancel_on")
			annotateStepsAt(steps[i].CancelOn.Monitors, mappingValue(group, "monitors"), source, current.Field("cancel_on").Field("monitors"))
			annotateStepsAt(steps[i].CancelOn.Steps, mappingValue(group, "steps"), source, current.Field("cancel_on").Field("steps"))
		}
		if steps[i].IsObserve() {
			group := mappingValue(node, "observe")
			annotateStepsAt(steps[i].Observe.Steps, mappingValue(group, "steps"), source, current.Field("observe").Field("steps"))
		}
		if steps[i].IsTryCatch() {
			if steps[i].Try != nil {
				annotateStepsAt(steps[i].Try.Steps, mappingValue(mappingValue(node, "try"), "steps"), source, current.Field("try").Field("steps"))
			}
			if steps[i].Catch != nil {
				annotateStepsAt(steps[i].Catch.Steps, mappingValue(mappingValue(node, "catch"), "steps"), source, current.Field("catch").Field("steps"))
			}
		}
		annotateStepsAt(steps[i].Defer, mappingValue(node, "defer"), source, current.Field("defer"))
		if steps[i].IsEnvironmentBlock() {
			annotateStepsAt(steps[i].Steps, mappingValue(node, "steps"), source, current.Field("steps"))
		}
		if steps[i].IsWorkingDirectoryBlock() {
			annotateStepsAt(steps[i].Steps, mappingValue(node, "steps"), source, current.Field("steps"))
		}
		if steps[i].IsWorktreeBlock() {
			group := mappingValue(node, "worktree")
			annotateStepsAt(steps[i].Worktree.Steps, mappingValue(group, "steps"), source, current.Field("worktree").Field("steps"))
		}
		if steps[i].IsConditionalBlock() {
			annotateStepsAt(steps[i].Steps, mappingValue(node, "steps"), source, current.Field("steps"))
		}
		if steps[i].Concurrent != nil {
			group := mappingValue(node, "concurrent")
			annotateStepsAt(steps[i].Concurrent.Steps, mappingValue(group, "steps"), source, current.Field("concurrent").Field("steps"))
		}
		if steps[i].Batch != nil {
			group := mappingValue(node, "batch")
			annotateStepsAt(steps[i].Batch.Steps, mappingValue(group, "steps"), source, current.Field("batch").Field("steps"))
		}
		if steps[i].Foreach != nil {
			group := mappingValue(node, "foreach")
			annotateStepsAt(steps[i].Foreach.Steps, mappingValue(group, "steps"), source, current.Field("foreach").Field("steps"))
		}
		if steps[i].Matrix != nil {
			group := mappingValue(node, "matrix")
			annotateStepsAt(steps[i].Matrix.Steps, mappingValue(group, "steps"), source, current.Field("matrix").Field("steps"))
		}
		if steps[i].Once != nil {
			group := mappingValue(node, "once")
			annotateStepsAt(steps[i].Once.Steps, mappingValue(group, "steps"), source, current.Field("once").Field("steps"))
		}
		if steps[i].Attempt != nil {
			group := mappingValue(node, "attempt")
			annotateStepsAt(steps[i].Attempt.Steps, mappingValue(group, "steps"), source, current.Field("attempt").Field("steps"))
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
	for _, target := range definition.Targets {
		remapStepLocations(target.Steps, materializedRoot, logicalSource)
		remapStepLocations(target.Finally, materializedRoot, logicalSource)
	}
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
	if relative == "-" || filepath.ToSlash(relative) == defaultRemoteWorkflowFile {
		return logical
	}
	return logical + "::" + filepath.ToSlash(relative)
}
