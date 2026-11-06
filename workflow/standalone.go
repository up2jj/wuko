package workflow

import (
	"fmt"
	"path/filepath"
	"strings"

	"go.yaml.in/yaml/v3"
)

// ValidateStandalone checks that a workflow, given as its source YAML and decoded definition,
// works as a single file. A copy loses its directory, so it may not depend on required step
// files, file-backed templates, local actions or relative scripts that would be left behind.
func ValidateStandalone(data []byte, definition *Definition) error {
	var document yaml.Node
	if err := yaml.Unmarshal(data, &document); err != nil {
		return fmt.Errorf("checking workflow source: %w", err)
	}
	if yamlNodeHasKey(&document, "require") {
		return fmt.Errorf("standalone workflows cannot use required step files")
	}
	for name, template := range definition.Templates {
		if template.File != "" {
			return fmt.Errorf("standalone workflows cannot use file-backed template %q", name)
		}
	}
	if reference := localActionReference(definition.Steps); reference != "" {
		return fmt.Errorf("standalone workflows cannot use local action %q", reference)
	}
	if reference := localActionReference(definition.Finally); reference != "" {
		return fmt.Errorf("standalone workflows cannot use local action %q", reference)
	}
	for name, target := range definition.Targets {
		if reference := localActionReference(target.Steps); reference != "" {
			return fmt.Errorf("target %q cannot use local action %q", name, reference)
		}
		if reference := localActionReference(target.Finally); reference != "" {
			return fmt.Errorf("target %q cannot use local action %q", name, reference)
		}
	}
	if reference := localActionReference(definition.Install); reference != "" {
		return fmt.Errorf("standalone workflows cannot use local action %q", reference)
	}
	if reference := localActionReference(definition.Uninstall); reference != "" {
		return fmt.Errorf("standalone workflows cannot use local action %q", reference)
	}
	if script := relativeShellScript(definition.Steps); script != "" {
		return fmt.Errorf("standalone workflows cannot use relative shell script %q", script)
	}
	if script := relativeShellScript(definition.Finally); script != "" {
		return fmt.Errorf("standalone workflows cannot use relative shell script %q", script)
	}
	if script := relativeShellScript(definition.Install); script != "" {
		return fmt.Errorf("standalone workflows cannot use relative lifecycle script %q", script)
	}
	if script := relativeShellScript(definition.Uninstall); script != "" {
		return fmt.Errorf("standalone workflows cannot use relative lifecycle script %q", script)
	}
	for name, target := range definition.Targets {
		if script := relativeShellScript(target.Steps); script != "" {
			return fmt.Errorf("target %q cannot use relative shell script %q", name, script)
		}
		if script := relativeShellScript(target.Finally); script != "" {
			return fmt.Errorf("target %q cannot use relative shell script %q", name, script)
		}
	}
	return nil
}

func yamlNodeHasKey(node *yaml.Node, key string) bool {
	if node == nil {
		return false
	}
	if node.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(node.Content); i += 2 {
			if node.Content[i].Value == key || yamlNodeHasKey(node.Content[i+1], key) {
				return true
			}
		}
	}
	for _, child := range node.Content {
		if yamlNodeHasKey(child, key) {
			return true
		}
	}
	return false
}

func localActionReference(steps []Step) string {
	for _, workflowStep := range steps {
		if workflowStep.Uses.Path != "" {
			return workflowStep.Uses.Path
		}
		for _, child := range workflowStep.ChildSequences() {
			if reference := localActionReference(child.Steps); reference != "" {
				return reference
			}
		}
	}
	return ""
}

func relativeShellScript(steps []Step) string {
	for _, workflowStep := range steps {
		if workflowStep.Type == "shell" {
			if script, ok := workflowStep.With["script"].(string); ok {
				script = strings.TrimSpace(script)
				if isStandaloneShellScriptReference(script) {
					return script
				}
			}
		}
		for _, child := range workflowStep.ChildSequences() {
			if script := relativeShellScript(child.Steps); script != "" {
				return script
			}
		}
	}
	return ""
}

func isStandaloneShellScriptReference(script string) bool {
	if strings.HasPrefix(script, "./") || strings.HasPrefix(script, "../") || filepath.IsAbs(script) {
		return true
	}
	return !strings.ContainsAny(script, " \t\r\n;|&<>$`'\"") && filepath.Ext(script) != ""
}
