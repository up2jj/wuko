package step

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/up2jj/wuko/process"
	"gopkg.in/yaml.v3"
)

type Request struct {
	StepID       string
	WorkflowName string
	// WorkflowSource is the stable logical source of the workflow or action definition.
	WorkflowSource string
	WorkflowDir    string
	RunDir         string
	LocalValueDir  string
	GlobalValueDir string
	Vars           map[string]any
	Inputs         map[string]any
	Env            map[string]string
	Steps          map[string]any
	// Dependencies contains outputs from direct prerequisite workflows keyed by alias.
	Dependencies map[string]map[string]any
	// Bindings contains active lifecycle and workflow-control roots such as batch, error, finally, foreach, and matrix.
	Bindings    map[string]any
	Stdin       io.Reader
	Stdout      io.Writer
	Stderr      io.Writer
	Interactive bool
	Attempt     int
	MaxAttempts int
	OperationID string
	// Executor runs process-backed work. A nil executor means local execution.
	Executor process.Executor
	// PreviousAttempt is the most recent failed attempt that produced a complete result.
	// It is nil on the first attempt and remains immutable for the duration of Run.
	PreviousAttempt *Result
}

// Result carries a step's outputs and the workflow variables it writes. Both are cloned
// before they reach workflow state, so a step may keep using the values it returned.
//
// Prefer the YAML/JSON shapes -- map[string]any, []any, and scalars -- for everything
// nested inside. Templates and expressions are written against those, and they are the
// shapes the cloner copies without reflection. A []map[string]any still works, but
// convert it: every other step does, and it keeps the hot path free of reflect.
type Result struct {
	Outputs   map[string]any
	Variables map[string]any
}

// Runner executes one step. Run must stop spawned work and return promptly when ctx is canceled;
// it must not continue producing output or external side effects after returning.
type Runner interface {
	Run(context.Context, Request) (Result, error)
}

// Cleaner is an optional runner lifecycle implemented by steps that create managed resources.
// Cleanup is called once for each successful Run, in reverse completion order, after the root
// workflow and its finally steps finish. Implementations must derive the resource to remove from
// result rather than mutable runner state because one runner may produce multiple results.
//
// The context is detached from the run's cancellation -- cleanup still runs after Ctrl-C -- but
// carries its values, so implementations must not treat it as cancellable and must not substitute
// context.Background(). It has no deadline of its own: bound the work per resource, because a
// shared budget across an unknown number of resources would starve the last ones into leaking.
type Cleaner interface {
	Cleanup(context.Context, Result) error
}

// Validator performs request-dependent validation without executing the step.
// Static configuration validation belongs in Builder.
type Validator interface {
	Validate(context.Context, Request) error
}

// ExecutorAware marks a runner that can execute through Request.Executor.
// Executor scopes reject runners without this capability rather than silently running them locally.
type ExecutorAware interface {
	ExecutorAware()
}

// ObservationError reports that a failed runner still produced a complete, usable observation.
// Control steps such as wait may evaluate the accompanying Result instead of failing immediately.
type ObservationError interface {
	error
	ObservationAvailable() bool
}

const (
	AttemptEnv     = "WUKO_STEP_ATTEMPT"
	MaxAttemptsEnv = "WUKO_STEP_MAX_ATTEMPTS"
	OperationIDEnv = "WUKO_STEP_OPERATION_ID"
)

// ApplyAttemptEnvironment adds reserved execution metadata after step-specific environment
// overlays have been applied.
func ApplyAttemptEnvironment(environment map[string]string, request Request) map[string]string {
	if environment == nil {
		environment = make(map[string]string, 3)
	}
	attempt := max(request.Attempt, 1)
	maximum := max(request.MaxAttempts, attempt)
	environment[AttemptEnv] = strconv.Itoa(attempt)
	environment[MaxAttemptsEnv] = strconv.Itoa(maximum)
	environment[OperationIDEnv] = request.OperationID
	return environment
}

// Builder strictly decodes and validates one step configuration.
type Builder func(raw map[string]any) (Runner, error)

type Registry struct {
	builders map[string]Builder
}

func NewRegistry() *Registry { return &Registry{builders: make(map[string]Builder)} }

func (r *Registry) Register(name string, builder Builder) error {
	if name == "" || builder == nil {
		return fmt.Errorf("step registration requires a name and builder")
	}
	if _, exists := r.builders[name]; exists {
		return fmt.Errorf("step type %q is already registered", name)
	}
	r.builders[name] = builder
	return nil
}

func (r *Registry) Build(name string, raw map[string]any) (Runner, error) {
	builder, ok := r.builders[name]
	if !ok {
		return nil, fmt.Errorf("unknown step type %q", name)
	}
	runner, err := builder(raw)
	if err != nil {
		return nil, fmt.Errorf("decoding %s step: %w", name, err)
	}
	return runner, nil
}

// DecodeConfig converts an untyped YAML mapping into a strict typed step configuration.
func DecodeConfig(raw map[string]any, target any) error {
	data, err := yaml.Marshal(raw)
	if err != nil {
		return fmt.Errorf("encoding step configuration: %w", err)
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(target); err != nil {
		return err
	}
	return nil
}

// Lookup resolves a dotted path rooted at vars or steps.
func Lookup(request Request, path string) (any, error) {
	parts := strings.Split(path, ".")
	if len(parts) < 2 {
		return nil, fmt.Errorf("path %q must start with vars. or steps.", path)
	}
	var current any
	switch parts[0] {
	case "vars":
		current = request.Vars
	case "steps":
		current = request.Steps
	default:
		return nil, fmt.Errorf("path %q must start with vars. or steps.", path)
	}
	return lookupParts(current, parts[1:], path)
}

func LookupValue(value any, path string) (any, error) {
	if path == "" {
		return value, nil
	}
	return lookupParts(value, strings.Split(path, "."), path)
}

func lookupParts(current any, parts []string, original string) (any, error) {
	for _, part := range parts {
		mapping, ok := current.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("path %q reaches non-object at %q", original, part)
		}
		value, exists := mapping[part]
		if !exists {
			return nil, fmt.Errorf("path %q has no field %q", original, part)
		}
		current = value
	}
	return current, nil
}
