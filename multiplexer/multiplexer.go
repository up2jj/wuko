// Package multiplexer controls the terminal context hosting a Wuko invocation.
package multiplexer

import (
	"context"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strconv"
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
	OperationTitle          Operation = "title"
	OperationClearTitle     Operation = "clear_title"
	OperationZoom           Operation = "zoom"
	OperationNotify         Operation = "notify"
	OperationStatus         Operation = "status"
	OperationClearStatus    Operation = "clear_status"
	OperationProgress       Operation = "progress"
	OperationClearProgress  Operation = "clear_progress"
	OperationLog            Operation = "log"
	OperationClearLog       Operation = "clear_log"
	OperationMetadata       Operation = "metadata"
	OperationRead           Operation = "read"
	OperationSendText       Operation = "send_text"
	OperationSendKeys       Operation = "send_keys"
	OperationSplit          Operation = "split"
	OperationResize         Operation = "resize"
	OperationFocusPane      Operation = "focus_pane"
	OperationFocusDirection Operation = "focus_direction"
	OperationClosePane      Operation = "close_pane"
	OperationCloseSurface   Operation = "close_surface"
)

type Target struct {
	Provider  Provider
	ID        string
	Workspace string
}

type Request struct {
	Provider   Provider
	Operation  Operation
	Scope      Scope
	Target     string
	Title      string
	Mode       string
	Body       string
	Key        string
	Value      string
	Icon       string
	Color      string
	Priority   *int
	Progress   float64
	Label      string
	Level      string
	Source     string
	Message    string
	Text       string
	Keys       []string
	Direction  string
	Scrollback bool
	Lines      int
	Amount     *float64

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
	Active        bool
	Provider      Provider
	Operation     Operation
	Scope         Scope
	Target        string
	Changed       bool
	Text          string
	CreatedTarget string
	CreatedPane   string
	// PreviousTitle is the label the target carried before a title operation
	// replaced it, so a later step can put it back. It is empty when the target
	// had no label or the provider cannot report one.
	PreviousTitle string
}

// Outcome carries the provider-specific detail an adapter observed while
// running one request.
type Outcome struct {
	PreviousTitle string
	Text          string
	CreatedTarget string
	CreatedPane   string
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
		OperationLog, OperationClearLog, OperationMetadata, OperationRead, OperationSendText,
		OperationSendKeys, OperationSplit, OperationResize, OperationFocusPane,
		OperationFocusDirection, OperationClosePane, OperationCloseSurface:
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
	if request.Target != "" {
		result.Target = request.Target
	}
	if err := validateProviderRequest(target.Provider, request); err != nil {
		return result, err
	}
	for _, candidate := range controller.adapters {
		if candidate.Provider() != target.Provider {
			continue
		}
		outcome, err := candidate.Execute(ctx, target, request, environment)
		if err != nil {
			return result, err
		}
		result.PreviousTitle = outcome.PreviousTitle
		result.Text = outcome.Text
		result.CreatedTarget = outcome.CreatedTarget
		result.CreatedPane = outcome.CreatedPane
		result.Changed = request.Operation != OperationRead
		return result, nil
	}
	return result, fmt.Errorf("multiplexer adapter %q is unavailable", target.Provider)
}

func validateProviderRequest(provider Provider, request Request) error {
	if request.Amount == nil {
		return nil
	}
	amount := *request.Amount
	if math.IsNaN(amount) || math.IsInf(amount, 0) || amount <= 0 {
		return fmt.Errorf("amount must be greater than zero")
	}
	switch provider {
	case ProviderTmux, ProviderCmux:
		if amount != math.Trunc(amount) {
			return fmt.Errorf("amount must be a whole number for %s", provider)
		}
	case ProviderHerdr:
		if amount > 1 {
			return fmt.Errorf("amount must be at most 1 for herdr")
		}
	}
	return nil
}

func runCommand(ctx context.Context, executor commandExecutor, environment map[string]string, command string, args ...string) (process.Result, error) {
	return runCommandWithLimit(ctx, executor, environment, 64*1024, command, args...)
}

func runReadCommand(ctx context.Context, executor commandExecutor, environment map[string]string, command string, args ...string) (process.Result, error) {
	result, err := runCommandWithLimit(ctx, executor, environment, 1<<20, command, args...)
	if err != nil {
		return result, err
	}
	if result.StdoutTruncated {
		return result, fmt.Errorf("reading multiplexer output exceeded the 1 MiB capture limit")
	}
	return result, nil
}

func runCommandWithLimit(ctx context.Context, executor commandExecutor, environment map[string]string, captureLimit int64, command string, args ...string) (process.Result, error) {
	result, err := executor.Run(ctx, process.Options{
		Command:      command,
		Args:         args,
		Env:          environment,
		CaptureLimit: captureLimit,
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

func targetID(detected Target, requested string) string {
	if requested != "" {
		return requested
	}
	return detected.ID
}

func amountArgument(amount *float64) string {
	if amount == nil {
		return ""
	}
	return strconv.FormatFloat(*amount, 'f', -1, 64)
}

func limitedLines(text string, request Request) string {
	if !request.Scrollback || request.Lines < 1 {
		return text
	}
	lines := strings.SplitAfter(text, "\n")
	// Providers such as tmux pad the visible screen with empty rows, so the
	// newest content can sit far above the end of the capture. Drop that
	// padding before counting, or a small window returns only blank rows.
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) > request.Lines {
		lines = lines[len(lines)-request.Lines:]
	}
	return strings.Join(lines, "")
}

var (
	keyAliases = map[string]string{"esc": "escape", "return": "enter", "pgup": "pageup", "pgdn": "pagedown", "backtab": "shift+tab"}
	namedKeys  = []string{"enter", "escape", "tab", "backspace", "delete", "insert", "home", "end", "pageup", "pagedown", "left", "right", "up", "down", "space", "shift+tab"}
)

// NormalizeKey accepts a deliberately small, provider-portable key vocabulary.
func NormalizeKey(value string) (string, error) {
	key := strings.ToLower(strings.TrimSpace(value))
	if alias := keyAliases[key]; alias != "" {
		key = alias
	}
	if slices.Contains(namedKeys, key) {
		return key, nil
	}
	if len(key) == 2 && key[0] == 'f' && key[1] >= '1' && key[1] <= '9' {
		return key, nil
	}
	if len(key) == 3 && key[0] == 'f' && key[1] == '1' && key[2] >= '0' && key[2] <= '2' {
		return key, nil
	}
	for _, prefix := range []string{"ctrl+", "alt+"} {
		if letter, ok := strings.CutPrefix(key, prefix); ok && len(letter) == 1 && letter[0] >= 'a' && letter[0] <= 'z' {
			return key, nil
		}
	}
	return "", fmt.Errorf("unsupported key %q", value)
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
