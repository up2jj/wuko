// Package executor registers lifecycle-bound command execution environments.
package executor

import (
	"context"
	"fmt"
	"io"

	"github.com/up2jj/wuko/process"
)

// Request describes the workflow context used to open an executor session.
type Request struct {
	WorkflowName string
	RunDir       string
	Env          map[string]string
	Stdout       io.Writer
	Stderr       io.Writer
}

// Session executes commands and owns resources that must be closed when its scope exits.
type Session interface {
	process.Executor
	Close(context.Context) error
}

// TaskRequest describes one task-graph invocation supported by an executor.
type TaskRequest struct {
	Name         string
	Mode         string
	Inputs       map[string]any
	Stdin        io.Reader
	Stdout       io.Writer
	Stderr       io.Writer
	CaptureLimit int64
}

// TaskRunner is an optional executor capability for providers that expose a
// native task graph, such as devenv.
type TaskRunner interface {
	RunTask(context.Context, TaskRequest) (process.Result, error)
}

// Provider opens sessions for one rendered executor configuration.
type Provider interface {
	Open(context.Context, Request) (Session, error)
}

// Validator performs context-dependent validation without opening external resources.
type Validator interface {
	Validate(context.Context, Request) error
}

// Builder strictly decodes and validates one executor configuration.
type Builder func(map[string]any) (Provider, error)

// Resolver lazily resolves an otherwise unknown executor type. Registered builders
// always take precedence.
type Resolver func(context.Context, string, map[string]any) (Provider, error)

// Registry maps executor type names to provider builders.
type Registry struct {
	builders map[string]Builder
	resolver Resolver
}

type RegistryOption func(*Registry)

// WithResolver adds a context-aware fallback for unknown types.
func WithResolver(resolver Resolver) RegistryOption {
	return func(registry *Registry) { registry.resolver = resolver }
}

func NewRegistry(options ...RegistryOption) *Registry {
	registry := &Registry{builders: make(map[string]Builder)}
	for _, option := range options {
		option(registry)
	}
	return registry
}

// SetResolver replaces the unknown-type fallback.
func (r *Registry) SetResolver(resolver Resolver) { r.resolver = resolver }

func (r *Registry) Register(name string, builder Builder) error {
	if name == "" || builder == nil {
		return fmt.Errorf("executor registration requires a name and builder")
	}
	if _, exists := r.builders[name]; exists {
		return fmt.Errorf("executor type %q is already registered", name)
	}
	r.builders[name] = builder
	return nil
}

func (r *Registry) Build(name string, raw map[string]any) (Provider, error) {
	return r.BuildContext(context.Background(), name, raw)
}

// BuildContext builds a registered executor or delegates an unknown type to the resolver.
func (r *Registry) BuildContext(ctx context.Context, name string, raw map[string]any) (Provider, error) {
	if r == nil {
		return nil, fmt.Errorf("unknown executor type %q", name)
	}
	builder, ok := r.builders[name]
	if !ok {
		if r.resolver != nil {
			return r.resolver(ctx, name, raw)
		}
		return nil, fmt.Errorf("unknown executor type %q", name)
	}
	provider, err := builder(raw)
	if err != nil {
		return nil, fmt.Errorf("decoding %s executor: %w", name, err)
	}
	return provider, nil
}
