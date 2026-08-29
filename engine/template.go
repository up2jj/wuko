package engine

import (
	"fmt"
	"sync"

	"github.com/up2jj/wuko/workflow"
)

// boundTemplateRenderer binds one step's template data to the workflow or action renderer.
// The data is assembled on first use because building it deep-copies every dependency output
// and control binding, and most steps never render a template through this interface.
type boundTemplateRenderer struct {
	renderer *workflow.Renderer
	build    func() map[string]any
	once     sync.Once
	data     map[string]any
}

func newBoundTemplateRenderer(renderer *workflow.Renderer, build func() map[string]any) *boundTemplateRenderer {
	return &boundTemplateRenderer{renderer: renderer, build: build}
}

func (renderer *boundTemplateRenderer) Validate(value string) error {
	return renderer.renderer.Validate(value)
}

func (renderer *boundTemplateRenderer) Render(value string) (string, error) {
	return renderer.renderer.Render(value, renderer.templateData())
}

func (renderer *boundTemplateRenderer) ValidateContent(value string) error {
	return renderer.renderer.ValidateUncached(value)
}

func (renderer *boundTemplateRenderer) RenderContent(value string) (string, error) {
	return renderer.renderer.RenderUncached(value, renderer.templateData())
}

func (renderer *boundTemplateRenderer) templateData() map[string]any {
	renderer.once.Do(func() {
		renderer.data = renderer.build()
		renderer.build = nil
	})
	return renderer.data
}

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
