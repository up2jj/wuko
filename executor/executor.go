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

// Provider opens sessions for one rendered executor configuration.
type Provider interface {
	Open(context.Context, Request) (Session, error)
}

// Validator performs context-dependent validation without opening external resources.
type Validator interface {
	Validate(context.Context, Request) error
}

// Builder strictly decodes one executor configuration.
type Builder func(map[string]any) (Provider, error)

// Registry maps executor type names to provider builders.
type Registry struct {
	builders map[string]Builder
}

func NewRegistry() *Registry { return &Registry{builders: make(map[string]Builder)} }

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
	if r == nil {
		return nil, fmt.Errorf("unknown executor type %q", name)
	}
	builder, ok := r.builders[name]
	if !ok {
		return nil, fmt.Errorf("unknown executor type %q", name)
	}
	provider, err := builder(raw)
	if err != nil {
		return nil, fmt.Errorf("decoding %s executor: %w", name, err)
	}
	return provider, nil
}
