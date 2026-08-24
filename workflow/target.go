package workflow

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// TargetDefinition overrides workflow execution metadata for one named target.
type TargetDefinition struct {
	Description string                    `yaml:"description,omitempty"`
	DependsOn   map[string]string         `yaml:"depends_on,omitempty"`
	Outputs     map[string]WorkflowOutput `yaml:"outputs,omitempty"`
	Form        yaml.Node                 `yaml:"form,omitempty"`
	Cron        string                    `yaml:"cron,omitempty"`
	Timezone    string                    `yaml:"timezone,omitempty"`
	Vars        map[string]any            `yaml:"vars,omitempty"`
	Env         Environment               `yaml:"env,omitempty"`
	Steps       []Step                    `yaml:"steps"`
	Finally     []Step                    `yaml:"finally,omitempty"`
}

// HasTargets reports whether the workflow declares named execution targets.
func (definition *Definition) HasTargets() bool {
	return definition != nil && len(definition.Targets) > 0
}

// TargetNames returns target names in deterministic order.
func (definition *Definition) TargetNames() []string {
	if definition == nil {
		return nil
	}
	return slices.Sorted(maps.Keys(definition.Targets))
}

// SelectTarget returns an executable definition with one target applied. Target selection is
// intentionally a workflow concern; the engine only receives the returned ordinary definition.
func (definition *Definition) SelectTarget(name string) (*Definition, error) {
	if definition == nil {
		return nil, fmt.Errorf("workflow definition is required")
	}
	if !definition.HasTargets() {
		if name != "" {
			return nil, fmt.Errorf("workflow %q does not declare targets", definition.Name)
		}
		return definition, nil
	}
	if name == "" {
		return nil, fmt.Errorf("workflow %q requires a target (%s)", definition.Name, formatTargetNames(definition.TargetNames()))
	}
	target, ok := definition.Targets[name]
	if !ok {
		return nil, fmt.Errorf("workflow %q has no target %q (available: %s)", definition.Name, name, formatTargetNames(definition.TargetNames()))
	}

	selected := *definition
	selected.Targets = nil
	selected.Description = firstNonEmpty(target.Description, definition.Description)
	selected.Cron = firstNonEmpty(target.Cron, definition.Cron)
	selected.Timezone = firstNonEmpty(target.Timezone, definition.Timezone)
	selected.Form = target.Form
	if !target.HasForm() {
		selected.Form = definition.Form
	}
	selected.DependsOn = maps.Clone(definition.DependsOn)
	if target.DependsOn != nil {
		selected.DependsOn = maps.Clone(target.DependsOn)
	}
	selected.Outputs = maps.Clone(definition.Outputs)
	if target.Outputs != nil {
		selected.Outputs = maps.Clone(target.Outputs)
	}
	selected.Vars = CloneMap(definition.Vars)
	maps.Copy(selected.Vars, target.Vars)
	selected.Env = maps.Clone(definition.Env)
	if selected.Env == nil {
		selected.Env = make(Environment)
	}
	maps.Copy(selected.Env, target.Env)
	selected.Steps = slices.Clone(target.Steps)
	selected.Finally = slices.Clone(target.Finally)
	return &selected, nil
}

func (target TargetDefinition) HasForm() bool {
	return target.Form.Kind != 0 && target.Form.Tag != "!!null"
}

func firstNonEmpty(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}

func formatTargetNames(names []string) string {
	if len(names) == 0 {
		return "none"
	}
	return strings.Join(names, ", ")
}
