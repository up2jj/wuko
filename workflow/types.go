package workflow

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

var (
	identifierPattern  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	environmentPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

// Definition is a fully loaded workflow document.
type Definition struct {
	Version     int            `yaml:"version"`
	Name        string         `yaml:"name"`
	Description string         `yaml:"description,omitempty"`
	Vars        map[string]any `yaml:"vars,omitempty"`
	Env         Environment    `yaml:"env,omitempty"`
	Steps       []Step         `yaml:"steps"`
	Path        string         `yaml:"-"`
	Dir         string         `yaml:"-"`
}

// Environment is a strictly string-valued environment overlay.
type Environment map[string]string

func (e *Environment) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("environment must be an object")
	}
	result := make(Environment, len(node.Content)/2)
	for i := 0; i < len(node.Content); i += 2 {
		keyNode, valueNode := node.Content[i], node.Content[i+1]
		if keyNode.Tag != "!!str" || valueNode.Tag != "!!str" {
			return fmt.Errorf("environment names and values must be strings")
		}
		result[keyNode.Value] = valueNode.Value
	}
	*e = result
	return nil
}

// Condition is a boolean expression controlling whether a step runs.
type Condition string

func (c *Condition) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.ScalarNode || (node.Tag != "!!str" && node.Tag != "!!bool") {
		return fmt.Errorf("if must be a boolean expression")
	}
	if strings.TrimSpace(node.Value) == "" {
		return fmt.Errorf("if must not be empty")
	}
	*c = Condition(node.Value)
	return nil
}

// Step declares either a concrete registered step type or a resolved remote composite action.
type Step struct {
	ID     string         `yaml:"id"`
	Type   string         `yaml:"type,omitempty"`
	Uses   ActionSource   `yaml:"uses,omitempty"`
	SHA256 string         `yaml:"sha256,omitempty"`
	If     Condition      `yaml:"if,omitempty"`
	With   map[string]any `yaml:"with,omitempty"`
	Action *Action        `yaml:"-"`
}

// ActionSource identifies action bytes fetched from HTTPS or produced by a local command.
type ActionSource struct {
	URL     string
	Command string
	Args    []string
}

func (source *ActionSource) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.ScalarNode:
		if node.Tag != "!!str" || strings.TrimSpace(node.Value) == "" {
			return fmt.Errorf("uses must be a non-empty HTTPS URL or command object")
		}
		source.URL = node.Value
		return nil
	case yaml.MappingNode:
		allowed := map[string]bool{"command": true, "args": true}
		for i := 0; i < len(node.Content); i += 2 {
			if !allowed[node.Content[i].Value] {
				return fmt.Errorf("field %s not found in action command source", node.Content[i].Value)
			}
		}
		var raw struct {
			Command string   `yaml:"command"`
			Args    []string `yaml:"args,omitempty"`
		}
		if err := node.Decode(&raw); err != nil {
			return err
		}
		if strings.TrimSpace(raw.Command) == "" {
			return fmt.Errorf("uses command is required")
		}
		source.Command, source.Args = raw.Command, raw.Args
		return nil
	default:
		return fmt.Errorf("uses must be a non-empty HTTPS URL or command object")
	}
}

// Empty reports whether no action source was declared.
func (source ActionSource) Empty() bool { return source.URL == "" && source.Command == "" }

// Display returns a safe description that excludes command arguments and URL query strings.
func (source ActionSource) Display() string {
	if source.URL != "" {
		parsed, err := url.Parse(source.URL)
		if err == nil {
			parsed.RawQuery, parsed.Fragment = "", ""
			return parsed.String()
		}
		return source.URL
	}
	return source.Command
}

// Load reads and validates the workflow-level schema. Step-specific validation is performed by
// the step registry.
func Load(path string) (*Definition, error) {
	runDir, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("finding run directory: %w", err)
	}
	return NewLoader(nil).Load(context.Background(), path, LoadOptions{RunDir: runDir})
}

func loadLocal(path string) (*Definition, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading workflow %s: %w", path, err)
	}

	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	var definition Definition
	if err := decoder.Decode(&definition); err != nil {
		return nil, fmt.Errorf("decoding workflow %s: %w", path, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("decoding workflow %s: multiple YAML documents are not supported", path)
		}
		return nil, fmt.Errorf("decoding workflow %s: %w", path, err)
	}
	if err := validateDefinition(&definition, true); err != nil {
		return nil, fmt.Errorf("validating workflow %s: %w", path, err)
	}

	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolving workflow path %s: %w", path, err)
	}
	definition.Path = abs
	definition.Dir = filepath.Dir(abs)
	if definition.Vars == nil {
		definition.Vars = make(map[string]any)
	}
	if definition.Env == nil {
		definition.Env = make(Environment)
	}
	return &definition, nil
}

func validateDefinition(definition *Definition, allowActions bool) error {
	if definition.Version != 1 {
		return fmt.Errorf("unsupported version %d (want 1)", definition.Version)
	}
	if definition.Name == "" {
		return fmt.Errorf("name is required")
	}
	if len(definition.Steps) == 0 {
		return fmt.Errorf("at least one step is required")
	}

	seen := make(map[string]struct{}, len(definition.Steps))
	for i, workflowStep := range definition.Steps {
		if !identifierPattern.MatchString(workflowStep.ID) {
			return fmt.Errorf("step %d has invalid id %q", i+1, workflowStep.ID)
		}
		if _, ok := seen[workflowStep.ID]; ok {
			return fmt.Errorf("duplicate step id %q", workflowStep.ID)
		}
		seen[workflowStep.ID] = struct{}{}
		if (workflowStep.Type == "") == workflowStep.Uses.Empty() {
			return fmt.Errorf("step %q must set exactly one of type or uses", workflowStep.ID)
		}
		if !workflowStep.Uses.Empty() && !allowActions {
			return fmt.Errorf("step %q: nested remote actions are not supported", workflowStep.ID)
		}
		if workflowStep.Uses.Empty() && workflowStep.SHA256 != "" {
			return fmt.Errorf("step %q: sha256 requires uses", workflowStep.ID)
		}
		if workflowStep.With == nil {
			workflowStep.With = make(map[string]any)
			definition.Steps[i] = workflowStep
		}
	}
	for name := range definition.Env {
		if !environmentPattern.MatchString(name) {
			return fmt.Errorf("invalid environment name %q", name)
		}
	}
	return nil
}

// ValidEnvironmentName reports whether name is a portable POSIX-style environment name.
func ValidEnvironmentName(name string) bool { return environmentPattern.MatchString(name) }
