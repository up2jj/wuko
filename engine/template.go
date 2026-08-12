package engine

import (
	"bytes"
	"fmt"
	"maps"
	"os"
	"strings"
	"text/template"
)

func renderString(value string, data map[string]any) (string, error) {
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

func validateTemplates(value any, skipSource bool) error {
	switch typed := value.(type) {
	case string:
		_, err := template.New("value").Option("missingkey=error").Parse(typed)
		return err
	case []any:
		for _, item := range typed {
			if err := validateTemplates(item, false); err != nil {
				return err
			}
		}
	case map[string]any:
		for key, item := range typed {
			if skipSource && key == "source" {
				continue
			}
			if err := validateTemplates(item, false); err != nil {
				return fmt.Errorf("field %s: %w", key, err)
			}
		}
	}
	return nil
}

func renderValue(value any, data map[string]any, skipSource bool) (any, error) {
	switch typed := value.(type) {
	case string:
		return renderString(typed, data)
	case []any:
		result := make([]any, len(typed))
		for i, item := range typed {
			rendered, err := renderValue(item, data, false)
			if err != nil {
				return nil, fmt.Errorf("item %d: %w", i, err)
			}
			result[i] = rendered
		}
		return result, nil
	case map[string]any:
		result := make(map[string]any, len(typed))
		for key, item := range typed {
			if skipSource && key == "source" {
				result[key] = item
				continue
			}
			rendered, err := renderValue(item, data, false)
			if err != nil {
				return nil, fmt.Errorf("field %s: %w", key, err)
			}
			result[key] = rendered
		}
		return result, nil
	default:
		return value, nil
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

func mergeEnvironment(base map[string]string, overlays ...map[string]string) map[string]string {
	result := maps.Clone(base)
	for _, overlay := range overlays {
		maps.Copy(result, overlay)
	}
	return result
}

func environmentAsAny(values map[string]string) map[string]any {
	result := make(map[string]any, len(values))
	for key, value := range values {
		result[key] = value
	}
	return result
}
