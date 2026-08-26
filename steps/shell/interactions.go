package shell

import (
	"fmt"
	"time"

	"github.com/up2jj/wuko/ptyinteract"
	"gopkg.in/yaml.v3"
)

type interactionConfig struct {
	Expect    *string `yaml:"expect,omitempty"`
	Send      *string `yaml:"send"`
	Newline   bool    `yaml:"newline,omitempty"`
	Timeout   *string `yaml:"timeout,omitempty"`
	Sensitive bool    `yaml:"sensitive,omitempty"`
}

func (config *interactionConfig) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("interaction must be an object")
	}
	allowed := map[string]bool{"expect": true, "send": true, "newline": true, "timeout": true, "sensitive": true}
	for i := 0; i < len(node.Content); i += 2 {
		field := node.Content[i].Value
		value := node.Content[i+1]
		if !allowed[field] {
			return fmt.Errorf("field %s not found in type shell.interactionConfig", field)
		}
		switch field {
		case "expect", "send", "timeout":
			if value.Kind != yaml.ScalarNode || value.Tag != "!!str" {
				return fmt.Errorf("interaction %s must be a string", field)
			}
		case "newline", "sensitive":
			if value.Kind != yaml.ScalarNode || value.Tag != "!!bool" {
				return fmt.Errorf("interaction %s must be a boolean", field)
			}
		}
	}
	type plain interactionConfig
	var decoded plain
	if err := node.Decode(&decoded); err != nil {
		return err
	}
	*config = interactionConfig(decoded)
	return nil
}

func compileInteractions(configs []interactionConfig) (*ptyinteract.Plan, error) {
	specs := make([]ptyinteract.Spec, len(configs))
	unresolved := false
	for i, config := range configs {
		if config.Send == nil {
			return nil, fmt.Errorf("interaction %d: send is required and must be a string", i+1)
		}
		spec := ptyinteract.Spec{Send: *config.Send, Newline: config.Newline, Sensitive: config.Sensitive}
		if config.Expect != nil {
			spec.HasExpect = true
			spec.Expect = *config.Expect
			if templated(spec.Expect) {
				unresolved = true
				spec.Expect = "(?:)"
			} else if spec.Expect == "" {
				return nil, fmt.Errorf("interaction %d: expect must not be empty", i+1)
			}
		}
		if config.Timeout != nil {
			if !spec.HasExpect {
				return nil, fmt.Errorf("interaction %d: timeout requires expect", i+1)
			}
			spec.TimeoutSet = true
			if templated(*config.Timeout) {
				unresolved = true
				spec.Timeout = time.Second
			} else {
				timeout, err := time.ParseDuration(*config.Timeout)
				if err != nil {
					return nil, fmt.Errorf("interaction %d: invalid timeout %q: %w", i+1, *config.Timeout, err)
				}
				spec.Timeout = timeout
			}
		}
		specs[i] = spec
	}
	plan, err := ptyinteract.Compile(specs)
	if err != nil {
		return nil, err
	}
	if unresolved {
		return nil, nil
	}
	return plan, nil
}
