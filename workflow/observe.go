package workflow

import (
	"fmt"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	ObserveRestart = "restart"
	ObserveQueue   = "queue"
	ObserveSkip    = "skip"

	// ObserveFail stops observation on the first source failure and fails the workflow.
	// ObserveContinue reports the failure and keeps observing, for sources whose failures
	// are routinely transient, such as a polled command that times out once.
	ObserveFail     = "fail"
	ObserveContinue = "continue"

	defaultObserveDebounce = 300 * time.Millisecond
)

// ObserveSource selects one event producer and carries its source-specific configuration.
type ObserveSource struct {
	Type string         `yaml:"type"`
	With map[string]any `yaml:"with,omitempty"`
}

func (source *ObserveSource) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("observe source must be an object")
	}
	if err := rejectUnknownFields(node, "observe source", map[string]bool{"type": true, "with": true}); err != nil {
		return err
	}
	type plain ObserveSource
	if err := node.Decode((*plain)(source)); err != nil {
		return err
	}
	return nil
}

// ObserveGroup starts a supervised background body driven by a configured source.
type ObserveGroup struct {
	Source      ObserveSource `yaml:"source"`
	Debounce    Duration      `yaml:"debounce,omitempty"`
	OnChange    string        `yaml:"on_change,omitempty"`
	OnError     string        `yaml:"on_error,omitempty"`
	Steps       []Step        `yaml:"steps"`
	hasDebounce bool
}

func (group *ObserveGroup) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("observe must be an object")
	}
	if err := rejectUnknownFields(node, "observe", map[string]bool{
		"source": true, "debounce": true, "on_change": true, "on_error": true, "steps": true,
	}); err != nil {
		return err
	}
	if value := mappingValue(node, "steps"); value != nil && value.Kind != yaml.SequenceNode {
		return fmt.Errorf("observe steps must be a list")
	}
	type plain ObserveGroup
	decoded := plain{OnChange: ObserveRestart, OnError: ObserveFail, Debounce: Duration(defaultObserveDebounce)}
	if err := node.Decode(&decoded); err != nil {
		return err
	}
	*group = ObserveGroup(decoded)
	group.hasDebounce = hasMappingField(node, "debounce")
	return nil
}

func (group ObserveGroup) EffectiveDebounce() time.Duration {
	if !group.hasDebounce && group.Debounce == 0 {
		return defaultObserveDebounce
	}
	return group.Debounce.Value()
}

func (group ObserveGroup) EffectiveOnChange() string {
	if group.OnChange == "" {
		return ObserveRestart
	}
	return group.OnChange
}

// EffectiveOnError reports what a source failure does. Failures while opening the source are
// always fatal: a source that never started cannot be observed.
func (group ObserveGroup) EffectiveOnError() string {
	if group.OnError == "" {
		return ObserveFail
	}
	return group.OnError
}

func (group ObserveGroup) Validate() error {
	if group.Source.Type == "" || !identifierPattern.MatchString(group.Source.Type) {
		return fmt.Errorf("observe source requires a valid type")
	}
	if len(group.Steps) == 0 {
		return fmt.Errorf("observe must contain at least one step")
	}
	if group.EffectiveDebounce() < 0 {
		return fmt.Errorf("observe debounce must not be negative")
	}
	switch group.EffectiveOnChange() {
	case ObserveRestart, ObserveQueue, ObserveSkip:
	default:
		return fmt.Errorf("observe on_change must be restart, queue, or skip")
	}
	switch group.EffectiveOnError() {
	case ObserveFail, ObserveContinue:
	default:
		return fmt.Errorf("observe on_error must be fail or continue")
	}
	return nil
}
