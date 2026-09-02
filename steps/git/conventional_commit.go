package git

import (
	"context"
	"fmt"
	"maps"
	"regexp"
	"strings"

	"github.com/up2jj/wuko/expression"
	"github.com/up2jj/wuko/step"
)

const (
	conventionalCommitCreate   = "create"
	conventionalCommitValidate = "validate"
)

var conventionalCommitVariablePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

type conventionalCommitConfig struct {
	Operation  string   `yaml:"operation"`
	Type       string   `yaml:"type,omitempty"`
	Scope      string   `yaml:"scope,omitempty"`
	Subject    string   `yaml:"subject,omitempty"`
	Breaking   bool     `yaml:"breaking,omitempty"`
	Body       string   `yaml:"body,omitempty"`
	Types      []string `yaml:"types,omitempty"`
	Scopes     []string `yaml:"scopes,omitempty"`
	ForceScope bool     `yaml:"force_scope,omitempty"`
	Strict     bool     `yaml:"strict,omitempty"`
	Task       string   `yaml:"task,omitempty"`
	TaskRegex  string   `yaml:"task_regex,omitempty"`
	Message    string   `yaml:"message,omitempty"`
	Variable   string   `yaml:"variable,omitempty"`
}

type conventionalCommitRunner struct {
	config conventionalCommitConfig
	raw    map[string]any
}

// NewConventionalCommit builds a git_conventional_commit step.
func NewConventionalCommit(raw map[string]any) (step.Runner, error) {
	var config conventionalCommitConfig
	if err := step.DecodeConfig(raw, &config); err != nil {
		return nil, err
	}
	if strings.TrimSpace(config.Operation) == "" {
		return nil, fmt.Errorf("operation is required")
	}
	if strings.Contains(config.Operation, "{{") {
		return nil, fmt.Errorf("operation must not be templated")
	}

	switch config.Operation {
	case conventionalCommitCreate:
		if err := validateConventionalCommitCreateFields(raw); err != nil {
			return nil, err
		}
		if strings.TrimSpace(config.Type) == "" {
			return nil, fmt.Errorf("type is required for create")
		}
		if strings.TrimSpace(config.Subject) == "" {
			return nil, fmt.Errorf("subject is required for create")
		}
		if _, present := raw["task_regex"]; present {
			if _, hasTask := raw["task"]; !hasTask {
				return nil, fmt.Errorf("task_regex requires task for create")
			}
		}
	case conventionalCommitValidate:
		if err := validateConventionalCommitValidateFields(raw); err != nil {
			return nil, err
		}
		if strings.TrimSpace(config.Message) == "" {
			return nil, fmt.Errorf("message is required for validate")
		}
	default:
		return nil, fmt.Errorf("operation must be create or validate")
	}
	if _, present := raw["variable"]; present {
		if strings.TrimSpace(config.Variable) == "" {
			return nil, fmt.Errorf("variable must not be blank")
		}
		if !strings.Contains(config.Variable, "{{") && !conventionalCommitVariablePattern.MatchString(config.Variable) {
			return nil, fmt.Errorf("invalid variable name %q", config.Variable)
		}
	}

	runner := &conventionalCommitRunner{config: config, raw: maps.Clone(raw)}
	if conventionalCommitHasTemplates(raw) {
		return runner, nil
	}
	if config.Operation == conventionalCommitCreate {
		if _, err := expression.BuildConventionalCommit(runner.createConfiguration()); err != nil {
			return nil, err
		}
		return runner, nil
	}
	if _, err := expression.InspectConventionalCommit(config.Message, runner.validationOptions()); err != nil {
		return nil, err
	}
	return runner, nil
}

func (runner *conventionalCommitRunner) Run(ctx context.Context, _ step.Request) (step.Result, error) {
	if err := ctx.Err(); err != nil {
		return step.Result{}, err
	}
	if conventionalCommitHasStructuralTemplate(runner.raw) {
		return step.Result{}, fmt.Errorf("conventional commit configuration contains an unresolved template")
	}

	var inspected expression.ConventionalCommitResult
	switch runner.config.Operation {
	case conventionalCommitCreate:
		message, err := expression.BuildConventionalCommit(runner.createConfiguration())
		if err != nil {
			return step.Result{}, err
		}
		inspected, err = expression.InspectConventionalCommit(message, runner.validationOptions())
		if err != nil {
			return step.Result{}, err
		}
		if strings.TrimSpace(runner.config.Task) != "" && strings.TrimSpace(runner.config.TaskRegex) == "" {
			inspected.Task = strings.TrimSpace(runner.config.Task)
			inspected.Subject = strings.TrimSpace(runner.config.Subject)
		}
	case conventionalCommitValidate:
		var err error
		inspected, err = expression.InspectConventionalCommit(runner.config.Message, runner.validationOptions())
		if err != nil {
			return step.Result{}, err
		}
	default:
		return step.Result{}, fmt.Errorf("operation %q was not resolved", runner.config.Operation)
	}

	result := step.Result{Outputs: conventionalCommitOutputs(inspected)}
	if runner.config.Operation == conventionalCommitCreate && runner.config.Variable != "" {
		result.Variables = map[string]any{runner.config.Variable: inspected.Message}
	}
	return result, nil
}

func (runner *conventionalCommitRunner) createConfiguration() map[string]any {
	result := map[string]any{
		"type": runner.config.Type, "subject": runner.config.Subject,
	}
	copyConventionalCommitConfiguredFields(result, runner.raw, runner.config, []string{
		"scope", "breaking", "body", "types", "scopes", "force_scope", "task", "task_regex",
	})
	return result
}

func (runner *conventionalCommitRunner) validationOptions() map[string]any {
	result := make(map[string]any, 5)
	copyConventionalCommitConfiguredFields(result, runner.raw, runner.config, []string{
		"types", "scopes", "force_scope", "strict", "task_regex",
	})
	return result
}

func copyConventionalCommitConfiguredFields(target, raw map[string]any, config conventionalCommitConfig, fields []string) {
	values := map[string]any{
		"scope": config.Scope, "breaking": config.Breaking, "body": config.Body,
		"types": config.Types, "scopes": config.Scopes, "force_scope": config.ForceScope,
		"strict": config.Strict, "task": config.Task, "task_regex": config.TaskRegex,
	}
	for _, field := range fields {
		if _, present := raw[field]; present {
			target[field] = values[field]
		}
	}
}

func validateConventionalCommitCreateFields(raw map[string]any) error {
	for _, field := range []string{"message", "strict"} {
		if _, present := raw[field]; present {
			return fmt.Errorf("create does not accept %s", field)
		}
	}
	return nil
}

func validateConventionalCommitValidateFields(raw map[string]any) error {
	for _, field := range []string{"type", "scope", "subject", "breaking", "body", "task", "variable"} {
		if _, present := raw[field]; present {
			return fmt.Errorf("validate does not accept %s", field)
		}
	}
	return nil
}

// conventionalCommitStructuralFields hold identifiers and enumerations, so a "{{" left in
// one of them is an unresolved template. The free-text fields (subject, body, message) are
// excluded: a commit message may legitimately talk about template syntax.
var conventionalCommitStructuralFields = []string{
	"operation", "type", "scope", "types", "scopes", "task", "task_regex", "variable",
}

func conventionalCommitHasStructuralTemplate(raw map[string]any) bool {
	for _, field := range conventionalCommitStructuralFields {
		if conventionalCommitHasTemplates(raw[field]) {
			return true
		}
	}
	return false
}

func conventionalCommitHasTemplates(value any) bool {
	switch typed := value.(type) {
	case string:
		return strings.Contains(typed, "{{")
	case []any:
		return slicesContainsConventionalCommitTemplate(typed)
	case []string:
		for _, item := range typed {
			if strings.Contains(item, "{{") {
				return true
			}
		}
	case map[string]any:
		for _, item := range typed {
			if conventionalCommitHasTemplates(item) {
				return true
			}
		}
	}
	return false
}

func slicesContainsConventionalCommitTemplate(values []any) bool {
	for _, value := range values {
		if conventionalCommitHasTemplates(value) {
			return true
		}
	}
	return false
}

func conventionalCommitOutputs(result expression.ConventionalCommitResult) map[string]any {
	return map[string]any{
		"valid": result.Valid, "value": result.Message, "message": result.Message,
		"cleaned_message": result.CleanedMessage, "classification": result.Classification,
		"type": result.Type, "scope": result.Scope, "subject": result.Subject,
		"breaking": result.Breaking, "body": result.Body, "task": result.Task,
	}
}
