package environment

import (
	"context"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type marker uint8

const (
	markerNone marker = iota
	markerMise
	markerToolVersions
)

// Resolver selects and composes registered loaders according to a policy.
type Resolver struct {
	loaders map[string]Loader
}

func NewResolver(loaders ...Loader) *Resolver {
	resolver := &Resolver{loaders: make(map[string]Loader, len(loaders))}
	for _, loader := range loaders {
		if loader != nil {
			resolver.loaders[loader.Name()] = loader
		}
	}
	return resolver
}

func NewDefaultResolver() *Resolver {
	return NewResolver(NewMiseLoader(), NewASDFLoader(), NewDirenvLoader())
}

func (resolver *Resolver) Load(ctx context.Context, dir string, base map[string]string, policy Policy) (InvocationEnvironment, error) {
	if policy.Disabled() {
		return InvocationEnvironment{Values: maps.Clone(base), Loaders: []string{}}, nil
	}
	if policy.Automatic() {
		return resolver.loadAutomatic(ctx, Request{Dir: dir, Env: maps.Clone(base)})
	}
	return resolver.loadNames(ctx, Request{Dir: dir, Env: maps.Clone(base)}, policy.Loaders(), true)
}

func (resolver *Resolver) loadNames(ctx context.Context, request Request, names []string, required bool) (InvocationEnvironment, error) {
	current := InvocationEnvironment{Values: maps.Clone(request.Env), Loaders: append([]string(nil), request.Applied...)}
	for _, name := range names {
		loader := resolver.loaders[name]
		if loader == nil {
			return InvocationEnvironment{}, fmt.Errorf("environment loader %q is not registered", name)
		}
		probe, err := loader.Probe(ctx, Request{Dir: request.Dir, Env: current.Values, Applied: current.Loaders})
		if err != nil {
			return InvocationEnvironment{}, fmt.Errorf("probing %s environment: %w", name, err)
		}
		if !probe.Available {
			if required {
				return InvocationEnvironment{}, unavailable(name, exec.ErrNotFound)
			}
			continue
		}
		loaded, loadErr := (Chain{loader}).Load(ctx, Request{Dir: request.Dir, Env: current.Values, Applied: current.Loaders})
		if loadErr != nil {
			return InvocationEnvironment{}, loadErr
		}
		current = loaded
	}
	return current, nil
}

func (resolver *Resolver) loadAutomatic(ctx context.Context, request Request) (InvocationEnvironment, error) {
	manager := ""
	switch markerFor(request.Dir, request.Env) {
	case markerMise:
		available, err := resolver.available(ctx, LoaderMise, request)
		if err != nil {
			return InvocationEnvironment{}, err
		}
		if !available {
			return InvocationEnvironment{}, unavailable(LoaderMise, exec.ErrNotFound)
		}
		manager = LoaderMise
	case markerToolVersions:
		available, err := resolver.available(ctx, LoaderASDF, request)
		if err != nil {
			return InvocationEnvironment{}, err
		}
		if available {
			manager = LoaderASDF
		} else {
			available, err = resolver.available(ctx, LoaderMise, request)
			if err != nil {
				return InvocationEnvironment{}, err
			}
			if !available {
				return InvocationEnvironment{}, fmt.Errorf(".tool-versions requires asdf or mise, but neither executable was found in PATH")
			}
			manager = LoaderMise
		}
	}
	current := InvocationEnvironment{Values: maps.Clone(request.Env), Loaders: []string{}}
	if manager != "" {
		var err error
		current, err = resolver.loadNames(ctx, request, []string{manager}, true)
		if err != nil {
			return InvocationEnvironment{}, err
		}
	}
	direnvRequest := Request{Dir: request.Dir, Env: current.Values, Applied: current.Loaders}
	available, err := resolver.available(ctx, LoaderDirenv, direnvRequest)
	if err != nil {
		return InvocationEnvironment{}, err
	}
	if available {
		return resolver.loadNames(ctx, direnvRequest, []string{LoaderDirenv}, false)
	}
	return current, nil
}

func (resolver *Resolver) available(ctx context.Context, name string, request Request) (bool, error) {
	loader := resolver.loaders[name]
	if loader == nil {
		return false, nil
	}
	probe, err := loader.Probe(ctx, request)
	if err != nil {
		return false, fmt.Errorf("probing %s environment: %w", name, err)
	}
	return probe.Available, nil
}

// markerFor finds the nearest manager configuration at or above dir. The search stops
// at the user's home directory, which both managers treat as the outermost scope: a
// marker in a shared parent such as /Users or / belongs to no project in particular.
func markerFor(dir string, environment map[string]string) marker {
	toolVersions := environment["ASDF_TOOL_VERSIONS_FILENAME"]
	if toolVersions == "" {
		toolVersions = ".tool-versions"
	}
	current, err := filepath.Abs(dir)
	if err != nil {
		current = filepath.Clean(dir)
	}
	boundary := ""
	if home := environment["HOME"]; home != "" {
		if absolute, err := filepath.Abs(home); err == nil {
			boundary = absolute
		}
	}
	for {
		if hasMiseMarker(current) {
			return markerMise
		}
		if regularFile(filepath.Join(current, toolVersions)) {
			return markerToolVersions
		}
		parent := filepath.Dir(current)
		if current == boundary || parent == current {
			return markerNone
		}
		current = parent
	}
}

func hasMiseMarker(dir string) bool {
	for _, relative := range []string{
		"mise.toml", ".mise.toml", "mise.local.toml", ".mise.local.toml",
		filepath.Join("mise", "config.toml"), filepath.Join(".mise", "config.toml"),
		filepath.Join(".config", "mise.toml"), filepath.Join(".config", "mise", "config.toml"),
	} {
		if regularFile(filepath.Join(dir, relative)) {
			return true
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		name := entry.Name()
		if !entry.IsDir() && strings.HasSuffix(name, ".toml") && (strings.HasPrefix(name, "mise.") || strings.HasPrefix(name, ".mise.")) {
			return true
		}
	}
	return false
}

func regularFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func directory(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
