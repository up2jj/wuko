package step

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"

	"gopkg.in/yaml.v3"
)

type Request struct {
	StepID       string
	WorkflowName string
	WorkflowDir  string
	RunDir       string
	Vars         map[string]any
	Env          map[string]string
	Steps        map[string]any
	Stdin        io.Reader
	Stdout       io.Writer
	Stderr       io.Writer
	Interactive  bool
}

type Result struct {
	Outputs   map[string]any
	Variables map[string]any
}

type Runner interface {
	Run(context.Context, Request) (Result, error)
}

type Validator interface {
	Validate(context.Context, Request) error
}

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
