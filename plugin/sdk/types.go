package sdk

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
)

// Protocol is the newest protocol the SDK speaks. A plugin that uses no v4 capability also
// answers a host that offers ProtocolV3, so rebuilding a plugin pinned to v3 keeps it working.
const Protocol = "wuko.plugin/v4"

// ProtocolV3 is the previous protocol, still answered for plugins without v4 capabilities.
const ProtocolV3 = "wuko.plugin/v3"

// Error is a protocol error with a stable machine-readable code.
type Error struct {
	Code string
	Err  error
}

func (err *Error) Error() string {
	if err == nil || err.Err == nil {
		return ""
	}
	return err.Err.Error()
}

func (err *Error) Unwrap() error { return err.Err }

// Coded wraps err with a stable protocol error code.
func Coded(code string, err error) error {
	if err == nil {
		return nil
	}
	return &Error{Code: code, Err: err}
}

type Result struct {
	Outputs   map[string]any `json:"outputs,omitempty"`
	Variables map[string]any `json:"variables,omitempty"`
}

type StepContext struct {
	StepID              string                    `json:"step_id"`
	WorkflowName        string                    `json:"workflow_name"`
	WorkflowSource      string                    `json:"workflow_source"`
	WorkflowDir         string                    `json:"workflow_dir"`
	WorkflowDirBorrowed bool                      `json:"workflow_dir_borrowed"`
	WorkflowTimezone    string                    `json:"workflow_timezone"`
	RunDir              string                    `json:"run_dir"`
	EnvironmentLoaders  []string                  `json:"environment_loaders"`
	LocalValueDir       string                    `json:"local_value_dir"`
	GlobalValueDir      string                    `json:"global_value_dir"`
	Vars                map[string]any            `json:"vars"`
	PresetVars          map[string]any            `json:"preset_vars"`
	Inputs              map[string]any            `json:"inputs"`
	Env                 map[string]string         `json:"env"`
	Steps               map[string]any            `json:"steps"`
	Dependencies        map[string]map[string]any `json:"dependencies"`
	Bindings            map[string]any            `json:"bindings"`
	Providers           map[string]any            `json:"providers"`
	Helpers             []string                  `json:"helpers"`
	Attempt             int                       `json:"attempt"`
	MaxAttempts         int                       `json:"max_attempts"`
	OperationID         string                    `json:"operation_id"`
	PreviousAttempt     *Result                   `json:"previous_attempt,omitempty"`
}

// ValidateRequest contains the capabilities available while validating a step.
type ValidateRequest[Config any] struct {
	Config  Config
	Context StepContext
	Host    Host
}

// RunRequest contains the capabilities available while running a step.
// Stdout and Stderr are always non-nil when the SDK invokes a handler.
type RunRequest[Config any] struct {
	Config  Config
	Context StepContext
	Host    Host
	Stdout  io.Writer
	Stderr  io.Writer
	ready   func(Result) error
}

// Ready reports that a service step has completed startup. It returns an error
// when called by a step that did not register a Service handler.
func (request RunRequest[Config]) Ready(result Result) error {
	if request.ready == nil {
		return fmt.Errorf("step readiness is unavailable: no service handler is registered")
	}
	return request.ready(result)
}

// ServiceRequest contains the configuration available while discovering a
// step's service policy. Host callbacks are unavailable during this phase.
type ServiceRequest[Config any] struct {
	Config  Config
	Context StepContext
}

type CleanupRequest[Config any] struct {
	Config Config
	Result Result
}

type Service struct {
	Kind      string `json:"kind"`
	KeepAlive bool   `json:"keep_alive"`
	FailFast  bool   `json:"fail_fast"`
	ExitOnEnd bool   `json:"exit_on_end"`
}

// StepHandler groups the optional operations of a plugin step. Run is required.
type StepHandler[Config any] struct {
	Validate func(context.Context, ValidateRequest[Config]) error
	Run      func(context.Context, RunRequest[Config]) (Result, error)
	Cleanup  func(context.Context, CleanupRequest[Config]) error
	Service  func(context.Context, ServiceRequest[Config]) (Service, error)
}

type HelperFunc func(context.Context, []any) (any, error)

type Lifecycle struct {
	Start func(context.Context, map[string]any) error
	Stop  func(context.Context, StopReason) error
}

type StopReason string

const (
	StopCompleted StopReason = "completed"
	StopFailed    StopReason = "failed"
	StopCanceled  StopReason = "canceled"
)

type HostCapability string

const (
	HostTemplateValidate HostCapability = "host.template.validate"
	HostTemplateRender   HostCapability = "host.template.render"
	HostFunctionCall     HostCapability = "host.function.call"
	// HostTerminal (v4) lets a step take over the user's terminal with Host.Terminal.
	HostTerminal HostCapability = "host.terminal.acquire"
)

// Host exposes plugin-to-host callbacks. Methods reject calls whose capability
// was not declared when the step was registered.
type Host interface {
	ValidateTemplate(context.Context, string) error
	RenderTemplate(context.Context, string, map[string]any) (string, error)
	CallFunction(context.Context, string, []any) (any, error)
	// Secret requires the HostFunctionCall capability.
	Secret(context.Context, string) (string, error)
}

// OutputSchema describes one result value. A zero value is a scalar.
type OutputSchema struct {
	Open   bool                    `json:"open,omitempty"`
	Fields map[string]OutputSchema `json:"fields"`
	Items  *OutputSchema           `json:"items,omitempty"`
}

func Scalar() OutputSchema { return OutputSchema{} }

func Array(items OutputSchema) OutputSchema { return OutputSchema{Items: &items} }

func Object(fields map[string]OutputSchema) OutputSchema {
	if fields == nil {
		fields = map[string]OutputSchema{}
	}
	return OutputSchema{Fields: fields}
}

func OpenObject(fields map[string]OutputSchema) OutputSchema {
	if fields == nil {
		fields = map[string]OutputSchema{}
	}
	return OutputSchema{Open: true, Fields: fields}
}

type stepOptions struct {
	outputs       *OutputSchema
	hostCallbacks []HostCapability
}

type StepOption func(*stepOptions) error

func WithOutputSchema(schema OutputSchema) StepOption {
	return func(options *stepOptions) error {
		copy := cloneOutputSchema(schema)
		options.outputs = &copy
		return nil
	}
}

func cloneOutputSchema(schema OutputSchema) OutputSchema {
	result := OutputSchema{Open: schema.Open}
	if schema.Fields != nil {
		result.Fields = make(map[string]OutputSchema, len(schema.Fields))
		for name, child := range schema.Fields {
			result.Fields[name] = cloneOutputSchema(child)
		}
	}
	if schema.Items != nil {
		items := cloneOutputSchema(*schema.Items)
		result.Items = &items
	}
	return result
}

func WithHostCallbacks(callbacks ...HostCapability) StepOption {
	return func(options *stepOptions) error {
		seen := make(map[HostCapability]bool, len(callbacks))
		for _, callback := range callbacks {
			switch callback {
			case HostTemplateValidate, HostTemplateRender, HostFunctionCall:
			default:
				return fmt.Errorf("unsupported host callback %q", callback)
			}
			if seen[callback] {
				return fmt.Errorf("duplicate host callback %q", callback)
			}
			seen[callback] = true
		}
		options.hostCallbacks = append([]HostCapability(nil), callbacks...)
		return nil
	}
}

// DeclaredHostCallbacks reports the host callbacks that options declare, applying them exactly as
// RegisterStep does. Adapters use it to learn the step's trust boundary, for example whether host
// template rendering can reach secrets because HostFunctionCall was declared.
func DeclaredHostCallbacks(options ...StepOption) ([]HostCapability, error) {
	settings := stepOptions{}
	for _, option := range options {
		if option == nil {
			return nil, fmt.Errorf("nil step option")
		}
		if err := option(&settings); err != nil {
			return nil, err
		}
	}
	return settings.hostCallbacks, nil
}

type ExecutorContext struct {
	WorkflowName string            `json:"workflow_name"`
	WorkflowDir  string            `json:"workflow_dir"`
	RunDir       string            `json:"run_dir"`
	Env          map[string]string `json:"env"`
}

type ExecutorRequest[Config any] struct {
	Config  Config
	Context ExecutorContext
}

type Command struct {
	Command      string            `json:"command"`
	Args         []string          `json:"args"`
	Dir          string            `json:"dir"`
	Env          map[string]string `json:"env"`
	Stdin        []byte            `json:"-"`
	CaptureLimit int64             `json:"capture_limit"`
	StdoutPolicy int               `json:"stdout_policy"`
	StderrPolicy int               `json:"stderr_policy"`
	Stdout       io.Writer         `json:"-"`
	Stderr       io.Writer         `json:"-"`
	Started      func()            `json:"-"`
}

type CommandResult struct {
	Stdout          string `json:"stdout"`
	Stderr          string `json:"stderr"`
	ExitCode        int    `json:"exit_code"`
	StdoutTruncated bool   `json:"stdout_truncated"`
	StderrTruncated bool   `json:"stderr_truncated"`
}

// ExecutorSession is returned by Open and receives commands until Close.
type ExecutorSession interface {
	Run(context.Context, Command) (CommandResult, error)
	Close(context.Context) error
}

type ExecutorHandler[Config any] struct {
	Validate           func(context.Context, ExecutorRequest[Config]) error
	Open               func(context.Context, ExecutorRequest[Config]) (ExecutorSession, error)
	CancelStopsProcess bool
	// FileSystem (v4) declares that every session implements ExecutorFileSystem, so file
	// steps run against the session's target instead of being rejected.
	FileSystem bool
	// Stdin (v4) declares that sessions read Command.Stdin as a stream, which the host
	// supplies on demand instead of reading it in full before the command starts.
	Stdin bool
	// TTY (v4) declares that sessions run commands with Command.TTY set in a terminal.
	TTY bool
}

type hostClient struct {
	allowed map[HostCapability]struct{}
}

func newHostClient(capabilities []HostCapability) hostClient {
	allowed := make(map[HostCapability]struct{}, len(capabilities))
	for _, capability := range capabilities {
		allowed[capability] = struct{}{}
	}
	return hostClient{allowed: allowed}
}

func (host hostClient) require(capability HostCapability) error {
	if _, ok := host.allowed[capability]; !ok {
		return fmt.Errorf("host callback %q is unavailable: declare it with WithHostCallbacks", capability)
	}
	return nil
}

func (host hostClient) ValidateTemplate(ctx context.Context, content string) error {
	if err := host.require(HostTemplateValidate); err != nil {
		return err
	}
	return callHost(ctx, string(HostTemplateValidate), map[string]any{"content": content}, nil)
}

func (host hostClient) RenderTemplate(ctx context.Context, content string, extra map[string]any) (string, error) {
	if err := host.require(HostTemplateRender); err != nil {
		return "", err
	}
	var result struct {
		Value string `json:"value"`
	}
	err := callHost(ctx, string(HostTemplateRender), map[string]any{"content": content, "extra": extra}, &result)
	return result.Value, err
}

func (host hostClient) CallFunction(ctx context.Context, name string, args []any) (any, error) {
	if err := host.require(HostFunctionCall); err != nil {
		return nil, err
	}
	var result struct {
		Value any `json:"value"`
	}
	err := callHost(ctx, string(HostFunctionCall), map[string]any{"name": name, "args": args}, &result)
	return result.Value, err
}

func (host hostClient) Secret(ctx context.Context, reference string) (string, error) {
	if err := host.require(HostFunctionCall); err != nil {
		return "", fmt.Errorf("resolving a secret: %w", err)
	}
	value, err := host.CallFunction(ctx, "secret", []any{reference})
	if err != nil {
		return "", err
	}
	secret, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("host returned a non-string secret")
	}
	return secret, nil
}

func decodeConfig[Config any](raw json.RawMessage) (Config, error) {
	var config Config
	if len(raw) == 0 {
		raw = []byte("{}")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	// A Config field typed any, map[string]any, or []any must see the same dynamic value shapes
	// as the request context does, so numbers stay json.Number here too: decoding a workflow's
	// `with: {id: 9007199254740993}` into float64 would silently round it.
	decoder.UseNumber()
	if err := decoder.Decode(&config); err != nil {
		return config, err
	}
	return config, nil
}
