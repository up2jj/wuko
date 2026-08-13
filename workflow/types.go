package workflow

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

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

// Step declares a concrete step, a remote composite action, or a local step-file requirement.
type Step struct {
	ID      string         `yaml:"id"`
	Type    string         `yaml:"type,omitempty"`
	Uses    ActionSource   `yaml:"uses,omitempty"`
	Require *string        `yaml:"require,omitempty"`
	SHA256  string         `yaml:"sha256,omitempty"`
	If      Condition      `yaml:"if,omitempty"`
	Timeout *Duration      `yaml:"timeout,omitempty"`
	Retry   *RetryPolicy   `yaml:"retry,omitempty"`
	With    map[string]any `yaml:"with,omitempty"`
	Action  *Action        `yaml:"-"`
}

// Duration is a YAML duration written using Go duration syntax, such as 500ms or 2m.
type Duration time.Duration

func (duration *Duration) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.ScalarNode || node.Tag != "!!str" {
		return fmt.Errorf("duration must be a string such as 500ms or 2m")
	}
	parsed, err := time.ParseDuration(node.Value)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", node.Value, err)
	}
	*duration = Duration(parsed)
	return nil
}

// Value returns the duration as a standard-library time.Duration.
func (duration Duration) Value() time.Duration { return time.Duration(duration) }

// String returns the duration in Go duration syntax.
func (duration Duration) String() string { return duration.Value().String() }

// RetryPolicy controls repeated execution of one logical workflow step.
type RetryPolicy struct {
	MaxAttempts       int      `yaml:"max_attempts"`
	InitialDelay      Duration `yaml:"initial_delay,omitempty"`
	BackoffMultiplier float64  `yaml:"backoff_multiplier,omitempty"`
	MaxDelay          Duration `yaml:"max_delay,omitempty"`
	Jitter            float64  `yaml:"jitter,omitempty"`
	MaxElapsedTime    Duration `yaml:"max_elapsed_time,omitempty"`
	OperationID       string   `yaml:"operation_id,omitempty"`
}

func (policy *RetryPolicy) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("retry must be an object")
	}
	allowed := map[string]bool{
		"max_attempts": true, "initial_delay": true, "backoff_multiplier": true,
		"max_delay": true, "jitter": true, "max_elapsed_time": true,
		"operation_id": true,
	}
	for i := 0; i < len(node.Content); i += 2 {
		if !allowed[node.Content[i].Value] {
			return fmt.Errorf("field %s not found in retry policy", node.Content[i].Value)
		}
	}
	type plainRetryPolicy RetryPolicy
	decoded := plainRetryPolicy{
		MaxAttempts: 3, InitialDelay: Duration(time.Second), BackoffMultiplier: 2,
		MaxDelay: Duration(30 * time.Second), Jitter: 0.2,
	}
	if err := node.Decode(&decoded); err != nil {
		return err
	}
	*policy = RetryPolicy(decoded)
	return nil
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
	if err := validateDefinitionHeader(&definition); err != nil {
		return nil, fmt.Errorf("validating workflow %s: %w", path, err)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolving workflow path %s: %w", path, err)
	}
	definition.Path = abs
	definition.Dir = filepath.Dir(abs)
	definition.Steps, err = expandRequiredSteps(definition.Steps, abs, nil)
	if err != nil {
		return nil, fmt.Errorf("loading workflow %s: %w", path, err)
	}
	if err := validateDefinition(&definition, true); err != nil {
		return nil, fmt.Errorf("validating workflow %s: %w", path, err)
	}
	if definition.Vars == nil {
		definition.Vars = make(map[string]any)
	}
	if definition.Env == nil {
		definition.Env = make(Environment)
	}
	return &definition, nil
}

func validateDefinition(definition *Definition, allowActions bool) error {
	if err := validateDefinitionHeader(definition); err != nil {
		return err
	}

	seen := make(map[string]struct{}, len(definition.Steps))
	for i, workflowStep := range definition.Steps {
		if workflowStep.Require != nil {
			return fmt.Errorf("step %d: require is only supported in workflow step files", i+1)
		}
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
		if err := workflowStep.ValidateExecutionPolicy(); err != nil {
			return fmt.Errorf("step %q: %w", workflowStep.ID, err)
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

// ValidateExecutionPolicy validates retry and timeout settings for a step.
func (workflowStep Step) ValidateExecutionPolicy() error {
	if workflowStep.Timeout != nil && workflowStep.Timeout.Value() <= 0 {
		return fmt.Errorf("timeout must be greater than zero")
	}
	policy := workflowStep.Retry
	if policy == nil {
		return nil
	}
	if policy.MaxAttempts < 1 || policy.MaxAttempts > 100 {
		return fmt.Errorf("retry max_attempts must be between 1 and 100")
	}
	if policy.InitialDelay.Value() < 0 {
		return fmt.Errorf("retry initial_delay cannot be negative")
	}
	if math.IsNaN(policy.BackoffMultiplier) || math.IsInf(policy.BackoffMultiplier, 0) || policy.BackoffMultiplier < 1 {
		return fmt.Errorf("retry backoff_multiplier must be at least 1")
	}
	if policy.MaxDelay.Value() < policy.InitialDelay.Value() {
		return fmt.Errorf("retry max_delay cannot be less than initial_delay")
	}
	if math.IsNaN(policy.Jitter) || math.IsInf(policy.Jitter, 0) || policy.Jitter < 0 || policy.Jitter > 1 {
		return fmt.Errorf("retry jitter must be between 0 and 1")
	}
	if policy.MaxElapsedTime.Value() < 0 {
		return fmt.Errorf("retry max_elapsed_time cannot be negative")
	}
	if policy.OperationID != "" && strings.TrimSpace(policy.OperationID) == "" {
		return fmt.Errorf("retry operation_id cannot be blank")
	}
	return nil
}

func validateDefinitionHeader(definition *Definition) error {
	if definition.Version != 1 {
		return fmt.Errorf("unsupported version %d (want 1)", definition.Version)
	}
	if definition.Name == "" {
		return fmt.Errorf("name is required")
	}
	if len(definition.Steps) == 0 {
		return fmt.Errorf("at least one step is required")
	}
	return nil
}

// ValidEnvironmentName reports whether name is a portable POSIX-style environment name.
func ValidEnvironmentName(name string) bool { return environmentPattern.MatchString(name) }
