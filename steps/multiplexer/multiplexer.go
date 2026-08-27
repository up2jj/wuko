// Package multiplexer exposes terminal multiplexer controls as a workflow step.
package multiplexer

import (
	"context"
	"fmt"
	"math"
	"regexp"
	"strings"

	mux "github.com/up2jj/wuko/multiplexer"
	"github.com/up2jj/wuko/step"
)

type Config struct {
	Provider  string  `yaml:"provider,omitempty"`
	Operation string  `yaml:"operation"`
	Title     string  `yaml:"title,omitempty"`
	Mode      string  `yaml:"mode,omitempty"`
	Body      string  `yaml:"body,omitempty"`
	Key       string  `yaml:"key,omitempty"`
	Value     string  `yaml:"value,omitempty"`
	Icon      string  `yaml:"icon,omitempty"`
	Color     string  `yaml:"color,omitempty"`
	Priority  *int    `yaml:"priority,omitempty"`
	Progress  float64 `yaml:"progress,omitempty"`
	Label     string  `yaml:"label,omitempty"`
	Level     string  `yaml:"level,omitempty"`
	Source    string  `yaml:"source,omitempty"`
	Message   string  `yaml:"message,omitempty"`

	DisplayAgent      string            `yaml:"display_agent,omitempty"`
	StateLabels       map[string]string `yaml:"state_labels,omitempty"`
	Tokens            map[string]string `yaml:"tokens,omitempty"`
	ClearTitle        bool              `yaml:"clear_title,omitempty"`
	ClearDisplayAgent bool              `yaml:"clear_display_agent,omitempty"`
	ClearStateLabels  bool              `yaml:"clear_state_labels,omitempty"`
	ClearTokens       []string          `yaml:"clear_tokens,omitempty"`
	TTLMilliseconds   int               `yaml:"ttl_ms,omitempty"`
}

type controller interface {
	Execute(context.Context, map[string]string, mux.Request) (mux.Result, error)
}

type Runner struct {
	config     Config
	present    map[string]bool
	controller controller
}

func Register(registry *step.Registry) error { return registry.Register("multiplexer", New) }

func New(raw map[string]any) (step.Runner, error) {
	return newRunner(raw, mux.New(nil))
}

func newRunner(raw map[string]any, controller controller) (*Runner, error) {
	var config Config
	if err := step.DecodeConfig(raw, &config); err != nil {
		return nil, err
	}
	present := make(map[string]bool, len(raw))
	for field := range raw {
		present[field] = true
	}
	runner := &Runner{config: config, present: present, controller: controller}
	if err := runner.validate(); err != nil {
		return nil, err
	}
	return runner, nil
}

func (runner *Runner) Run(ctx context.Context, request step.Request) (step.Result, error) {
	if err := ctx.Err(); err != nil {
		return step.Result{}, err
	}
	if err := runner.validate(); err != nil {
		return step.Result{}, err
	}
	provider, err := mux.ParseProvider(runner.config.Provider)
	if err != nil {
		return step.Result{}, err
	}
	operation, err := mux.ParseOperation(runner.config.Operation)
	if err != nil {
		return step.Result{}, err
	}
	source := runner.config.Source
	if operation == mux.OperationMetadata && source == "" {
		source = metadataSource(request.WorkflowName, request.StepID)
	}
	result, err := runner.controller.Execute(ctx, request.Env, mux.Request{
		Provider: provider, Operation: operation, Title: runner.config.Title, Mode: runner.config.Mode,
		Body: runner.config.Body, Key: runner.config.Key, Value: runner.config.Value,
		Icon: runner.config.Icon, Color: runner.config.Color, Priority: runner.config.Priority,
		Progress: runner.config.Progress, Label: runner.config.Label, Level: runner.config.Level,
		Source: source, Message: runner.config.Message, DisplayAgent: runner.config.DisplayAgent,
		StateLabels: runner.config.StateLabels, Tokens: runner.config.Tokens,
		ClearTitle: runner.config.ClearTitle, ClearDisplayAgent: runner.config.ClearDisplayAgent,
		ClearStateLabels: runner.config.ClearStateLabels, ClearTokens: runner.config.ClearTokens,
		TTLMilliseconds: runner.config.TTLMilliseconds,
	})
	if err != nil {
		return step.Result{}, err
	}
	return step.Result{Outputs: map[string]any{
		"active": result.Active, "provider": string(result.Provider), "operation": string(result.Operation),
		"target": result.Target, "changed": result.Changed,
	}}, nil
}

func (runner *Runner) validate() error {
	if strings.TrimSpace(runner.config.Operation) == "" {
		return fmt.Errorf("operation is required")
	}
	if templated(runner.config.Operation) || templated(runner.config.Provider) {
		return nil
	}
	if _, err := mux.ParseProvider(runner.config.Provider); err != nil {
		return err
	}
	operation, err := mux.ParseOperation(runner.config.Operation)
	if err != nil {
		return err
	}
	allowed := map[string]bool{"provider": true, "operation": true}
	require := func(field string) error {
		if !runner.present[field] {
			return fmt.Errorf("%s is required for %s", field, operation)
		}
		return nil
	}
	switch operation {
	case mux.OperationTitle:
		allowed["title"] = true
		if err := require("title"); err != nil {
			return err
		}
		if err := mux.ValidateDisplayText("title", runner.config.Title, true); err != nil {
			return err
		}
	case mux.OperationClearTitle, mux.OperationClearProgress, mux.OperationClearLog:
	case mux.OperationZoom:
		allowed["mode"] = true
		if err := require("mode"); err != nil {
			return err
		}
		if !templated(runner.config.Mode) && runner.config.Mode != "on" && runner.config.Mode != "off" && runner.config.Mode != "toggle" {
			return fmt.Errorf("mode must be on, off, or toggle")
		}
	case mux.OperationNotify:
		allowed["title"], allowed["body"] = true, true
		if err := require("title"); err != nil {
			return err
		}
		if err := validateTexts(map[string]string{"title": runner.config.Title, "body": runner.config.Body}, "title"); err != nil {
			return err
		}
	case mux.OperationStatus:
		for _, field := range []string{"key", "value", "icon", "color", "priority"} {
			allowed[field] = true
		}
		for _, field := range []string{"key", "value"} {
			if err := require(field); err != nil {
				return err
			}
		}
		if err := validateTexts(map[string]string{"key": runner.config.Key, "value": runner.config.Value, "icon": runner.config.Icon}, "key", "value"); err != nil {
			return err
		}
		if runner.config.Color != "" && !hexColorPattern.MatchString(runner.config.Color) {
			return fmt.Errorf("color must use #RRGGBB")
		}
	case mux.OperationClearStatus:
		allowed["key"] = true
		if err := require("key"); err != nil {
			return err
		}
		if err := mux.ValidateDisplayText("key", runner.config.Key, true); err != nil {
			return err
		}
	case mux.OperationProgress:
		allowed["progress"], allowed["label"] = true, true
		if err := require("progress"); err != nil {
			return err
		}
		if math.IsNaN(runner.config.Progress) || math.IsInf(runner.config.Progress, 0) || runner.config.Progress < 0 || runner.config.Progress > 1 {
			return fmt.Errorf("progress must be between 0 and 1")
		}
		if err := mux.ValidateDisplayText("label", runner.config.Label, false); err != nil {
			return err
		}
	case mux.OperationLog:
		allowed["level"], allowed["source"], allowed["message"] = true, true, true
		if err := require("message"); err != nil {
			return err
		}
		if runner.config.Level == "" {
			runner.config.Level = "info"
		}
		if !templated(runner.config.Level) && !validLogLevel(runner.config.Level) {
			return fmt.Errorf("level must be info, progress, success, warning, or error")
		}
		if err := validateTexts(map[string]string{"source": runner.config.Source, "message": runner.config.Message}, "message"); err != nil {
			return err
		}
	case mux.OperationMetadata:
		for _, field := range []string{"source", "title", "display_agent", "state_labels", "tokens", "clear_title", "clear_display_agent", "clear_state_labels", "clear_tokens", "ttl_ms"} {
			allowed[field] = true
		}
		if err := runner.validateMetadata(); err != nil {
			return err
		}
	}
	for field := range runner.present {
		if !allowed[field] {
			return fmt.Errorf("%s is not allowed for %s", field, operation)
		}
	}
	return nil
}

func (runner *Runner) validateMetadata() error {
	if runner.config.Source != "" {
		if err := mux.ValidateMetadataSource(runner.config.Source); err != nil {
			return err
		}
	}
	if runner.present["ttl_ms"] && (runner.config.TTLMilliseconds < 1 || runner.config.TTLMilliseconds > 86_400_000) {
		return fmt.Errorf("ttl_ms must be between 1 and 86400000 when set")
	}
	hasPatch := runner.config.Title != "" || runner.config.DisplayAgent != "" || len(runner.config.StateLabels) > 0 || len(runner.config.Tokens) > 0 ||
		runner.config.ClearTitle || runner.config.ClearDisplayAgent || runner.config.ClearStateLabels || len(runner.config.ClearTokens) > 0
	if !hasPatch {
		return fmt.Errorf("metadata must set or clear at least one field")
	}
	if runner.config.Title != "" && runner.config.ClearTitle {
		return fmt.Errorf("metadata title and clear_title cannot be combined")
	}
	if runner.config.DisplayAgent != "" && runner.config.ClearDisplayAgent {
		return fmt.Errorf("metadata display_agent and clear_display_agent cannot be combined")
	}
	if len(runner.config.StateLabels) > 0 && runner.config.ClearStateLabels {
		return fmt.Errorf("metadata state_labels and clear_state_labels cannot be combined")
	}
	if err := validateTexts(map[string]string{"title": runner.config.Title, "display_agent": runner.config.DisplayAgent}); err != nil {
		return err
	}
	for status, label := range runner.config.StateLabels {
		if status != "idle" && status != "working" && status != "blocked" && status != "done" && status != "unknown" {
			return fmt.Errorf("state label status %q is invalid", status)
		}
		if err := mux.ValidateDisplayText("state label", label, true); err != nil {
			return err
		}
	}
	for name, value := range runner.config.Tokens {
		if !metadataNamePattern.MatchString(name) {
			return fmt.Errorf("metadata token name %q is invalid", name)
		}
		if err := mux.ValidateDisplayText("metadata token", value, true); err != nil {
			return err
		}
	}
	for _, name := range runner.config.ClearTokens {
		if !metadataNamePattern.MatchString(name) {
			return fmt.Errorf("metadata token name %q is invalid", name)
		}
		if _, set := runner.config.Tokens[name]; set {
			return fmt.Errorf("metadata token %q cannot be set and cleared together", name)
		}
	}
	return nil
}

func validateTexts(values map[string]string, required ...string) error {
	requiredSet := make(map[string]bool, len(required))
	for _, field := range required {
		requiredSet[field] = true
	}
	for field, value := range values {
		if err := mux.ValidateDisplayText(field, value, requiredSet[field]); err != nil {
			return err
		}
	}
	return nil
}

func validLogLevel(value string) bool {
	return value == "info" || value == "progress" || value == "success" || value == "warning" || value == "error"
}

func metadataSource(workflowName, stepID string) string {
	value := "wuko." + workflowName + "." + stepID
	value = strings.Map(func(character rune) rune {
		if character >= 'A' && character <= 'Z' || character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || strings.ContainsRune(":._-", character) {
			return character
		}
		return '-'
	}, value)
	if len(value) > 80 {
		value = value[:80]
	}
	return value
}

func templated(value string) bool { return strings.Contains(value, "{{") }

var (
	hexColorPattern     = regexp.MustCompile(`^#[0-9A-Fa-f]{6}$`)
	metadataNamePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)
)

var _ step.Runner = (*Runner)(nil)
