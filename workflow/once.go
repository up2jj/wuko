package workflow

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	OnceBusyError = "error"
	OnceBusyWait  = "wait"
)

// OnceGroup runs one private child sequence after claiming a persistent key.
type OnceGroup struct {
	Key    string `yaml:"key"`
	Scope  string `yaml:"scope"`
	OnBusy string `yaml:"on_busy,omitempty"`
	Steps  []Step `yaml:"steps"`
}

func (group *OnceGroup) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("once must be an object")
	}
	if err := rejectUnknownFields(node, "once", map[string]bool{
		"key": true, "scope": true, "on_busy": true, "steps": true,
	}); err != nil {
		return err
	}
	for _, name := range []string{"key", "scope", "on_busy"} {
		value := mappingValue(node, name)
		if value != nil && (value.Kind != yaml.ScalarNode || value.Tag != "!!str") {
			return fmt.Errorf("once %s must be a string", name)
		}
	}
	if steps := mappingValue(node, "steps"); steps != nil && steps.Kind != yaml.SequenceNode {
		return fmt.Errorf("once steps must be a list")
	}
	type plain OnceGroup
	decoded := plain{OnBusy: OnceBusyError}
	if err := node.Decode(&decoded); err != nil {
		return err
	}
	*group = OnceGroup(decoded)
	return nil
}

// Validate checks the declaration independently of rendered runtime state.
func (group OnceGroup) Validate() error {
	if strings.TrimSpace(group.Key) == "" {
		return fmt.Errorf("once key is required")
	}
	switch group.Scope {
	case "local", "global":
	default:
		return fmt.Errorf("once scope must be %q or %q", "local", "global")
	}
	switch group.OnBusy {
	case OnceBusyError, OnceBusyWait:
	default:
		return fmt.Errorf("once on_busy must be %q or %q", OnceBusyError, OnceBusyWait)
	}
	if len(group.Steps) == 0 {
		return fmt.Errorf("once must contain at least one step")
	}
	return nil
}
