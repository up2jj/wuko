// Package multiplexer controls the terminal context hosting a Wuko invocation.
package multiplexer

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"unicode"

	"github.com/up2jj/wuko/process"
)

type Provider string

const (
	ProviderAuto  Provider = "auto"
	ProviderTmux  Provider = "tmux"
	ProviderHerdr Provider = "herdr"
	ProviderCmux  Provider = "cmux"
)

// Scope selects which part of the hosting terminal an operation addresses.
// Panes are the default because every provider can label one; tab support is
// gated on what the detected provider advertises.
type Scope string

const (
	ScopePane Scope = "pane"
	ScopeTab  Scope = "tab"
)

type Operation string

const (
	OperationTitle         Operation = "title"
	OperationClearTitle    Operation = "clear_title"
	OperationZoom          Operation = "zoom"
	OperationNotify        Operation = "notify"
	OperationStatus        Operation = "status"
	OperationClearStatus   Operation = "clear_status"
	OperationProgress      Operation = "progress"
	OperationClearProgress Operation = "clear_progress"
	OperationLog           Operation = "log"
	OperationClearLog      Operation = "clear_log"
	OperationMetadata      Operation = "metadata"
)

type Target struct {
	Provider  Provider
	ID        string
	Workspace string
}

type Request struct {
	Provider  Provider
	Operation Operation
	Scope     Scope
	Title     string
	Mode      string
	Body      string
	Key       string
	Value     string
	Icon      string
	Color     string
	Priority  *int
	Progress  float64
	Label     string
	Level     string
	Source    string
	Message   string

	DisplayAgent      string
	StateLabels       map[string]string
	Tokens            map[string]string
	ClearTitle        bool
	ClearDisplayAgent bool
	ClearStateLabels  bool
	ClearTokens       []string
	TTLMilliseconds   int
}

type Result struct {
	Active    bool
	Provider  Provider
	Operation Operation
	Scope     Scope
	Target    string
	Changed   bool
	// PreviousTitle is the label the target carried before a title operation
	// replaced it, so a later step can put it back. It is empty when the target
	// had no label or the provider cannot report one.
	PreviousTitle string
}

// Outcome carries the provider-specific detail an adapter observed while
// running one request.
type Outcome struct {
	PreviousTitle string
}

type UnsupportedError struct {
	Provider  Provider
	Operation Operation
	Detail    string
}

func (e *UnsupportedError) Error() string {
	message := fmt.Sprintf("multiplexer provider %q does not support operation %q", e.Provider, e.Operation)
	if e.Detail != "" {
		message += ": " + e.Detail
	}
	return message
}

type commandExecutor interface {
	Run(context.Context, process.Options) (process.Result, error)
}

type Adapter interface {
	Provider() Provider
	Detect(map[string]string) (Target, bool)
	Execute(context.Context, Target, Request, map[string]string) (Outcome, error)
}

type Controller struct {
	adapters []Adapter
}

func New(executor process.Executor) *Controller {
	if executor == nil {
		executor = process.LocalExecutor{}
	}
	return &Controller{adapters: []Adapter{
		tmuxAdapter{executor: executor},
		herdrAdapter{executor: executor},
		cmuxAdapter{executor: executor},
	}}
}

func ParseProvider(value string) (Provider, error) {
	provider := Provider(value)
	if provider == "" {
		return ProviderAuto, nil
	}
	switch provider {
	case ProviderAuto, ProviderTmux, ProviderHerdr, ProviderCmux:
		return provider, nil
	default:
		return "", fmt.Errorf("provider must be auto, tmux, herdr, or cmux")
	}
}

func ParseScope(value string) (Scope, error) {
	scope := Scope(value)
	if scope == "" {
		return ScopePane, nil
	}
	switch scope {
	case ScopePane, ScopeTab:
		return scope, nil
	default:
		return "", fmt.Errorf("scope must be pane or tab")
	}
}

func ParseOperation(value string) (Operation, error) {
	operation := Operation(value)
	switch operation {
	case OperationTitle, OperationClearTitle, OperationZoom, OperationNotify,
		OperationStatus, OperationClearStatus, OperationProgress, OperationClearProgress,
		OperationLog, OperationClearLog, OperationMetadata:
		return operation, nil
	default:
		return "", fmt.Errorf("unknown multiplexer operation %q", value)
	}
}

func Detect(environment map[string]string, requested Provider) (Target, bool) {
	return New(nil).Detect(environment, requested)
}

func (controller *Controller) Detect(environment map[string]string, requested Provider) (Target, bool) {
	if requested == "" {
		requested = ProviderAuto
	}
	for _, candidate := range controller.adapters {
		if requested != ProviderAuto && candidate.Provider() != requested {
			continue
		}
		if target, ok := candidate.Detect(environment); ok {
			return target, true
		}
	}
	return Target{}, false
}

func (controller *Controller) Execute(ctx context.Context, environment map[string]string, request Request) (Result, error) {
	if request.Scope == "" {
		request.Scope = ScopePane
	}
	target, active := controller.Detect(environment, request.Provider)
	result := Result{Operation: request.Operation, Scope: request.Scope}
	if !active {
		return result, nil
	}
	result.Active = true
	result.Provider = target.Provider
	result.Target = target.ID
	for _, candidate := range controller.adapters {
		if candidate.Provider() != target.Provider {
			continue
		}
		outcome, err := candidate.Execute(ctx, target, request, environment)
		if err != nil {
			return result, err
		}
		result.PreviousTitle = outcome.PreviousTitle
		result.Changed = true
		return result, nil
	}
	return result, fmt.Errorf("multiplexer adapter %q is unavailable", target.Provider)
}

func runCommand(ctx context.Context, executor commandExecutor, environment map[string]string, command string, args ...string) (process.Result, error) {
	result, err := executor.Run(ctx, process.Options{
		Command:      command,
		Args:         args,
		Env:          environment,
		CaptureLimit: 64 * 1024,
	})
	if err == nil {
		return result, nil
	}
	detail := strings.TrimSpace(result.Stderr)
	if detail == "" {
		detail = strings.TrimSpace(result.Stdout)
	}
	if detail == "" {
		return result, fmt.Errorf("running %s: %w", command, err)
	}
	return result, fmt.Errorf("running %s: %s: %w", command, detail, err)
}

func ValidateDisplayText(field, value string, required bool) error {
	if required && strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s is required", field)
	}
	if strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return fmt.Errorf("%s must not contain control characters", field)
	}
	return nil
}

var metadataSourcePattern = regexp.MustCompile(`^[A-Za-z0-9:._-]+$`)

func ValidateMetadataSource(value string) error {
	if value == "" {
		return fmt.Errorf("source is required")
	}
	if len(value) > 80 || !metadataSourcePattern.MatchString(value) {
		return fmt.Errorf("source must be at most 80 ASCII letters, digits, colons, dots, underscores, or hyphens")
	}
	return nil
}
