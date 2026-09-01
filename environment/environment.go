// Package environment prepares the host environment used to load and run workflows.
package environment

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
)

const (
	LoaderAuto   = "auto"
	LoaderNone   = "none"
	LoaderMise   = "mise"
	LoaderASDF   = "asdf"
	LoaderDirenv = "direnv"
)

var loaderNames = []string{LoaderMise, LoaderASDF, LoaderDirenv}

// Request is the progressively prepared environment presented to one loader.
type Request struct {
	Dir     string
	Env     map[string]string
	Applied []string
}

// Changes is an environment overlay. A nil value removes the variable.
type Changes map[string]*string

// Result describes the changes produced by one loader.
type Result struct {
	Changes Changes
	Applied bool
}

// Probe reports whether a loader can be executed in the presented environment.
type Probe struct {
	Available bool
}

// Loader prepares one kind of invocation environment.
type Loader interface {
	Name() string
	Probe(context.Context, Request) (Probe, error)
	Load(context.Context, Request) (Result, error)
}

// InvocationEnvironment is the completed environment and its ordered provenance.
type InvocationEnvironment struct {
	Values  map[string]string
	Loaders []string
}

// InvocationLoader prepares a complete invocation environment according to a policy.
type InvocationLoader interface {
	Load(context.Context, string, map[string]string, Policy) (InvocationEnvironment, error)
}

// InvocationLoaderFunc adapts a function to InvocationLoader.
type InvocationLoaderFunc func(context.Context, string, map[string]string, Policy) (InvocationEnvironment, error)

func (loader InvocationLoaderFunc) Load(ctx context.Context, dir string, base map[string]string, policy Policy) (InvocationEnvironment, error) {
	return loader(ctx, dir, base, policy)
}

// Chain applies loaders in order.
type Chain []Loader

func (chain Chain) Load(ctx context.Context, request Request) (InvocationEnvironment, error) {
	values := maps.Clone(request.Env)
	if values == nil {
		values = make(map[string]string)
	}
	applied := slices.Clone(request.Applied)
	for _, loader := range chain {
		if loader == nil {
			continue
		}
		result, err := loader.Load(ctx, Request{Dir: request.Dir, Env: maps.Clone(values), Applied: slices.Clone(applied)})
		if err != nil {
			return InvocationEnvironment{}, fmt.Errorf("loading %s environment: %w", loader.Name(), err)
		}
		apply(values, result.Changes)
		if result.Applied && !slices.Contains(applied, loader.Name()) {
			applied = append(applied, loader.Name())
		}
	}
	return InvocationEnvironment{Values: values, Loaders: applied}, nil
}

func apply(environment map[string]string, changes Changes) {
	for name, value := range changes {
		if value == nil {
			delete(environment, name)
			continue
		}
		environment[name] = *value
	}
}

// Policy selects automatic, disabled, or explicit environment loading.
type Policy struct {
	automatic bool
	disabled  bool
	loaders   []string
}

func AutomaticPolicy() Policy { return Policy{automatic: true} }

func DisabledPolicy() Policy { return Policy{disabled: true} }

func ExplicitPolicy(names ...string) (Policy, error) { return ParsePolicy(names) }

func (policy Policy) Automatic() bool { return policy.automatic }

func (policy Policy) Disabled() bool { return policy.disabled }

func (policy Policy) Loaders() []string { return slices.Clone(policy.loaders) }

func (policy Policy) String() string {
	switch {
	case policy.automatic:
		return LoaderAuto
	case policy.disabled:
		return LoaderNone
	default:
		return strings.Join(policy.loaders, ",")
	}
}

// ParsePolicy validates ordered loader names. An empty list selects automatic loading.
func ParsePolicy(names []string) (Policy, error) {
	if len(names) == 0 {
		return AutomaticPolicy(), nil
	}
	normalized := make([]string, len(names))
	seen := make(map[string]struct{}, len(names))
	manager := ""
	direnvSeen := false
	for i, name := range names {
		name = strings.TrimSpace(name)
		if !slices.Contains(append([]string{LoaderAuto, LoaderNone}, loaderNames...), name) {
			return Policy{}, fmt.Errorf("unknown environment loader %q (available: auto, none, mise, asdf, direnv)", name)
		}
		if _, exists := seen[name]; exists {
			return Policy{}, fmt.Errorf("environment loader %q is configured more than once", name)
		}
		seen[name] = struct{}{}
		normalized[i] = name
		switch name {
		case LoaderAuto, LoaderNone:
			if len(names) != 1 {
				return Policy{}, fmt.Errorf("environment loader %q must be used alone", name)
			}
		case LoaderMise, LoaderASDF:
			if manager != "" {
				return Policy{}, fmt.Errorf("environment loaders %q and %q cannot be combined", manager, name)
			}
			if direnvSeen {
				return Policy{}, fmt.Errorf("environment loader %q must precede direnv", name)
			}
			manager = name
		case LoaderDirenv:
			direnvSeen = true
		}
	}
	if normalized[0] == LoaderAuto {
		return AutomaticPolicy(), nil
	}
	if normalized[0] == LoaderNone {
		return DisabledPolicy(), nil
	}
	return Policy{loaders: normalized}, nil
}

// ParsePolicyValue parses the comma-separated WUKO_ENV_LOADERS value.
func ParsePolicyValue(value string) (Policy, error) {
	if strings.TrimSpace(value) == "" {
		return AutomaticPolicy(), nil
	}
	return ParsePolicy(strings.Split(value, ","))
}
