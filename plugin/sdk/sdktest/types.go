package sdktest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/up2jj/wuko/plugin/sdk"
)

// StepOperation selects one of the SDK step protocol methods.
type StepOperation string

const (
	// StepRun invokes a step's Run handler. A zero-value operation also runs the step.
	StepRun StepOperation = "step.run"
	// StepValidate invokes a step's Validate handler.
	StepValidate StepOperation = "step.validate"
	// StepCleanup invokes a step's Cleanup handler.
	StepCleanup StepOperation = "step.cleanup"
	// StepService requests a step's service policy.
	StepService StepOperation = "step.service"
)

// StepRequest describes one framed step call.
type StepRequest struct {
	Operation StepOperation
	Type      string
	With      any
	Context   sdk.StepContext
	Result    sdk.Result
}

// Initialization is the typed result of the initialize handshake performed by Start.
type Initialization struct {
	Protocol  string                `json:"protocol"`
	Namespace string                `json:"namespace"`
	Lifecycle bool                  `json:"lifecycle,omitempty"`
	Steps     []StepDeclaration     `json:"steps"`
	Executors []ExecutorDeclaration `json:"executors"`
	Helpers   []HelperDeclaration   `json:"helpers,omitempty"`
	Actions   []string              `json:"actions,omitempty"`
}

// StepDeclaration describes a step advertised during initialization.
type StepDeclaration struct {
	Type          string               `json:"type"`
	Cleanup       bool                 `json:"cleanup,omitempty"`
	Service       bool                 `json:"service,omitempty"`
	HostCallbacks []sdk.HostCapability `json:"host_callbacks,omitempty"`
	Outputs       *sdk.OutputSchema    `json:"outputs,omitempty"`
}

// ExecutorDeclaration describes an executor advertised during initialization.
type ExecutorDeclaration struct {
	Type               string `json:"type"`
	CancelStopsProcess bool   `json:"cancel_stops_process"`
}

// HelperDeclaration describes a helper advertised during initialization.
type HelperDeclaration struct {
	Name string `json:"name"`
}

// Event is a decoded plugin event. Data contains decoded stdout, stderr, or
// started event bytes; Result contains the JSON result of events such as ready.
type Event struct {
	Name   string
	Data   []byte
	Result json.RawMessage
}

// DecodeResult decodes the event result while preserving JSON numbers.
func (event Event) DecodeResult(target any) error {
	return decodeResult(event.Result, target)
}

// Response is the terminal response of one call.
type Response struct {
	Result json.RawMessage
	Error  *ResponseError
}

// DecodeResult returns the remote error, if any, then decodes the result while
// preserving JSON numbers.
func (response Response) DecodeResult(target any) error {
	if response.Error != nil {
		return response.Error
	}
	return decodeResult(response.Result, target)
}

// ResponseError is a plugin protocol error.
type ResponseError struct {
	Code    string `json:"code,omitempty"`
	Message string `json:"message"`
}

func (protocolError *ResponseError) Error() string {
	if protocolError == nil {
		return ""
	}
	return protocolError.Message
}

// Callbacks provides typed fakes for plugin-to-host callbacks. A nil callback
// returns a host_callback error to the plugin.
type Callbacks struct {
	ValidateTemplate func(context.Context, string) error
	RenderTemplate   func(context.Context, string, map[string]any) (string, error)
	CallFunction     func(context.Context, string, []any) (any, error)
}

type options struct {
	timeout   time.Duration
	callbacks Callbacks
}

// Option configures a Harness.
type Option func(*options) error

// WithTimeout sets the maximum time for one await or shutdown phase.
func WithTimeout(timeout time.Duration) Option {
	return func(options *options) error {
		if timeout <= 0 {
			return fmt.Errorf("timeout must be positive")
		}
		options.timeout = timeout
		return nil
	}
}

// WithCallbacks installs callback fakes for requests made by plugin steps.
func WithCallbacks(callbacks Callbacks) Option {
	return func(options *options) error {
		options.callbacks = callbacks
		return nil
	}
}

func decodeResult(raw json.RawMessage, target any) error {
	if target == nil {
		return nil
	}
	if len(raw) == 0 {
		return fmt.Errorf("response has no result")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return fmt.Errorf("decoding result: trailing JSON data")
	}
	return nil
}
