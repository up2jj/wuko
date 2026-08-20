package engine

import (
	"fmt"

	"github.com/up2jj/wuko/workflow"
)

func renderString(renderer *workflow.Renderer, value string, data map[string]any) (string, error) {
	return renderer.Render(value, data)
}

func validateTemplates(renderer *workflow.Renderer, value any, skipSource bool) error {
	switch typed := value.(type) {
	case string:
		return renderer.Validate(typed)
	case []any:
		for _, item := range typed {
			if err := validateTemplates(renderer, item, false); err != nil {
				return err
			}
		}
	case map[string]any:
		for key, item := range typed {
			if skipSource && key == "source" {
				continue
			}
			if err := validateTemplates(renderer, item, false); err != nil {
				return fmt.Errorf("field %s: %w", key, err)
			}
		}
	}
	return nil
}

func renderValue(renderer *workflow.Renderer, value any, data map[string]any, skipSource bool) (any, error) {
	switch typed := value.(type) {
	case string:
		return renderString(renderer, typed, data)
	case []any:
		result := make([]any, len(typed))
		for i, item := range typed {
			rendered, err := renderValue(renderer, item, data, false)
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
			rendered, err := renderValue(renderer, item, data, false)
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

func environmentAsAny(values map[string]string) map[string]any {
	return workflow.EnvironmentValues(values)
}
