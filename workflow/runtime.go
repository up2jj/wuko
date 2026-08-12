package workflow

import (
	"bytes"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"
	"text/template"
)

// LoadOptions supplies the pre-run values used to resolve remote action references.
type LoadOptions struct {
	Vars   map[string]any
	Env    map[string]string
	RunDir string
}

// PrepareValues applies workflow and caller overrides using the same precedence as execution.
func PrepareValues(definition *Definition, options LoadOptions) (map[string]any, map[string]string, error) {
	vars := CloneMap(definition.Vars)
	for key, value := range options.Vars {
		vars[key] = Clone(value)
	}
	host := hostEnvironment()
	wfEnv := make(map[string]string, len(definition.Env))
	root := TemplateData(definition, options.RunDir, nil, vars, host, nil)
	keys := slices.Sorted(maps.Keys(definition.Env))
	for _, key := range keys {
		value, err := RenderString(definition.Env[key], root)
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
	if inputs == nil {
		inputs = map[string]any{}
	}
	if vars == nil {
		vars = map[string]any{}
	}
	if steps == nil {
		steps = map[string]any{}
	}
	return map[string]any{
		"inputs": inputs,
		"vars":   vars,
		"env":    EnvironmentValues(environment),
		"steps":  steps,
		"workflow": map[string]any{
			"name": definition.Name,
			"dir":  definition.Dir,
		},
		"run": map[string]any{"dir": runDir},
	}
}

// RenderString renders one strict Go-template string.
func RenderString(value string, data map[string]any) (string, error) {
	tmpl, err := template.New("value").Option("missingkey=error").Parse(value)
	if err != nil {
		return "", err
	}
	var rendered bytes.Buffer
	if err := tmpl.Execute(&rendered, data); err != nil {
		return "", err
	}
	return rendered.String(), nil
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
