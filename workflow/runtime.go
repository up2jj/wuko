package workflow

import (
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"

	"github.com/up2jj/wuko/diagnostic"
)

// LoadOptions supplies the pre-run values used to resolve composite action references.
type LoadOptions struct {
	Vars map[string]any
	Env  map[string]string
	// Target selects one declared workflow target before preparation. Empty selects a legacy
	// workflow without targets and is rejected for workflows that declare targets.
	Target string
	// Lifecycle permits loading a targeted workflow for its workflow-level lifecycle hooks. The
	// loader selects a deterministic target only to provide an ordinary definition for validation;
	// install and uninstall steps themselves remain workflow-level.
	Lifecycle bool
	// BaseEnv overrides the current process environment when non-nil.
	BaseEnv map[string]string
	RunDir  string
	// Diagnostics receives opt-in workflow loading and preparation events.
	Diagnostics diagnostic.Reporter
	// RejectRemoteArchives prevents remote workflow loading from accepting archive payloads.
	RejectRemoteArchives bool
	sourceRoot           string
	sourceLabel          string
}

// PrepareValues applies workflow and caller overrides using the same precedence as execution.
func PrepareValues(definition *Definition, options LoadOptions) (map[string]any, map[string]string, error) {
	renderer, err := NewRenderer(definition.Templates)
	if err != nil {
		return nil, nil, err
	}
	vars := CloneMap(definition.Vars)
	for key, value := range options.Vars {
		vars[key] = Clone(value)
	}
	host := hostEnvironment()
	if options.BaseEnv != nil {
		host = maps.Clone(options.BaseEnv)
	}
	wfEnv := make(map[string]string, len(definition.Env))
	root := TemplateData(definition, options.RunDir, nil, vars, host, nil)
	keys := slices.Sorted(maps.Keys(definition.Env))
	for _, key := range keys {
		value, err := renderer.Render(definition.Env[key], root)
		if err != nil {
			return nil, nil, fmt.Errorf("rendering workflow environment %s: %w", key, err)
		}
		wfEnv[key] = value
	}
	environment := maps.Clone(host)
	maps.Copy(environment, wfEnv)
	maps.Copy(environment, options.Env)
	return vars, environment, nil
}

// TemplateData constructs the common Go-template roots for a workflow or action.
func TemplateData(definition *Definition, runDir string, inputs, vars map[string]any, environment map[string]string, steps map[string]any) map[string]any {
	return TemplateDataWithBindings(definition, runDir, inputs, vars, environment, steps, nil)
}

// TemplateDataWithBindings constructs template roots including active workflow-control bindings.
func TemplateDataWithBindings(definition *Definition, runDir string, inputs, vars map[string]any, environment map[string]string, steps, bindings map[string]any) map[string]any {
	return TemplateDataWithDependencies(definition, runDir, inputs, vars, environment, steps, nil, bindings)
}

// TemplateDataWithDependencies constructs template roots including direct workflow dependencies.
func TemplateDataWithDependencies(definition *Definition, runDir string, inputs, vars map[string]any, environment map[string]string, steps map[string]any, dependencies map[string]map[string]any, bindings map[string]any) map[string]any {
	if inputs == nil {
		inputs = map[string]any{}
	}
	if vars == nil {
		vars = map[string]any{}
	}
	if steps == nil {
		steps = map[string]any{}
	}
	if dependencies == nil {
		dependencies = map[string]map[string]any{}
	}
	result := map[string]any{
		"inputs":       inputs,
		"vars":         vars,
		"env":          EnvironmentValues(environment),
		"steps":        steps,
		"dependencies": CloneDependencies(dependencies),
		"workflow": map[string]any{
			"name": definition.Name,
			"dir":  definition.Dir,
		},
		"run": map[string]any{"dir": runDir},
	}
	for key, value := range bindings {
		result[key] = Clone(value)
	}
	return result
}

// CloneDependencies recursively clones dependency output maps.
func CloneDependencies(source map[string]map[string]any) map[string]map[string]any {
	result := make(map[string]map[string]any, len(source))
	for alias, outputs := range source {
		result[alias] = CloneMap(outputs)
	}
	return result
}

// RenderString renders one strict Go-template string.
func RenderString(value string, data map[string]any) (string, error) {
	renderer, err := NewRenderer(nil)
	if err != nil {
		return "", err
	}
	return renderer.Render(value, data)
}

// EnvironmentValues converts a string environment to template-compatible values.
func EnvironmentValues(values map[string]string) map[string]any {
	result := make(map[string]any, len(values))
	for key, value := range values {
		result[key] = value
	}
	return result
}

// CloneMap returns a recursively cloned JSON/YAML-style map.
func CloneMap(source map[string]any) map[string]any {
	result := make(map[string]any, len(source))
	for key, value := range source {
		result[key] = Clone(value)
	}
	return result
}

// Clone returns a recursive clone of JSON/YAML-style maps and arrays.
func Clone(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return CloneMap(typed)
	case []any:
		result := make([]any, len(typed))
		for i, item := range typed {
			result[i] = Clone(item)
		}
		return result
	default:
		return value
	}
}

func hostEnvironment() map[string]string {
	result := make(map[string]string)
	for _, value := range os.Environ() {
		key, item, found := strings.Cut(value, "=")
		if found {
			result[key] = item
		}
	}
	return result
}
