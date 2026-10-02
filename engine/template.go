package engine

import (
	"fmt"
	"maps"
	"sync"

	"github.com/up2jj/wuko/step"
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

func (renderer *boundTemplateRenderer) RenderWith(value string, extra map[string]any) (string, error) {
	return renderer.renderer.Render(value, overlayTemplateData(renderer.templateData(), extra))
}

func (renderer *boundTemplateRenderer) RenderContentWith(value string, extra map[string]any) (string, error) {
	return renderer.renderer.RenderUncached(value, overlayTemplateData(renderer.templateData(), extra))
}

func (renderer *boundTemplateRenderer) Snapshot() step.DataTemplateRenderer {
	return &snapshotTemplateRenderer{renderer: renderer.renderer, data: workflow.CloneMap(renderer.templateData())}
}

// WithoutSecrets shares the assembled data rather than copying it: neither renderer writes to it,
// and the RenderWith family overlays extra roots onto a clone.
func (renderer *boundTemplateRenderer) WithoutSecrets() step.DataTemplateRenderer {
	return &snapshotTemplateRenderer{renderer: renderer.renderer.WithoutSecrets(), data: renderer.templateData()}
}

func (renderer *boundTemplateRenderer) templateData() map[string]any {
	renderer.once.Do(func() {
		renderer.data = renderer.build()
		renderer.build = nil
	})
	return renderer.data
}

type snapshotTemplateRenderer struct {
	renderer *workflow.Renderer
	data     map[string]any
}

func (renderer *snapshotTemplateRenderer) Validate(value string) error {
	return renderer.renderer.Validate(value)
}

func (renderer *snapshotTemplateRenderer) Render(value string) (string, error) {
	return renderer.renderer.Render(value, renderer.data)
}

func (renderer *snapshotTemplateRenderer) ValidateContent(value string) error {
	return renderer.renderer.ValidateUncached(value)
}

func (renderer *snapshotTemplateRenderer) RenderContent(value string) (string, error) {
	return renderer.renderer.RenderUncached(value, renderer.data)
}

func (renderer *snapshotTemplateRenderer) RenderWith(value string, extra map[string]any) (string, error) {
	return renderer.renderer.Render(value, overlayTemplateData(renderer.data, extra))
}

func (renderer *snapshotTemplateRenderer) RenderContentWith(value string, extra map[string]any) (string, error) {
	return renderer.renderer.RenderUncached(value, overlayTemplateData(renderer.data, extra))
}

func (renderer *snapshotTemplateRenderer) Snapshot() step.DataTemplateRenderer { return renderer }

func (renderer *snapshotTemplateRenderer) WithoutSecrets() step.DataTemplateRenderer {
	return &snapshotTemplateRenderer{renderer: renderer.renderer.WithoutSecrets(), data: renderer.data}
}

func overlayTemplateData(base, extra map[string]any) map[string]any {
	data := maps.Clone(base)
	maps.Copy(data, extra)
	return data
}

func renderString(renderer *workflow.Renderer, value string, data map[string]any) (string, error) {
	return renderer.Render(value, data)
}

func rawSourceStep(stepType string) bool {
	return stepType == "lua" || stepType == "template"
}

func validateStepTemplates(renderer *workflow.Renderer, value any, stepType string) error {
	if stepType != "dashboard" {
		// Only dashboard defers nested subtrees; skip path tracking on the hot path.
		return validateTemplates(renderer, value, rawSourceStep(stepType))
	}
	return walkStepTemplates(value, stepType, nil, func(text string) error {
		return renderer.Validate(text)
	})
}

func renderStepValue(renderer *workflow.Renderer, value any, data map[string]any, stepType string) (any, error) {
	if stepType != "dashboard" {
		return renderValue(renderer, value, data, rawSourceStep(stepType))
	}
	return transformStepTemplates(value, stepType, nil, func(text string) (string, error) {
		return renderer.Render(text, data)
	})
}

// deferredStepTemplateSubtree identifies configuration owned by a step that must be rendered
// later with step-local data. Dashboard callbacks are complete workflow fragments, while widget
// templates and confirmations receive live dashboard roots.
func deferredStepTemplateSubtree(stepType string, path []string) bool {
	if len(path) == 1 && path[0] == "source" && rawSourceStep(stepType) {
		return true
	}
	if stepType != "dashboard" || len(path) == 0 {
		return false
	}
	last := path[len(path)-1]
	if last == "template" || last == "confirm" {
		return true
	}
	if last != "steps" && last != "finally" && last != "outputs" {
		return false
	}
	for _, component := range path {
		if component == "refresh" || component == "actions" {
			return true
		}
	}
	return false
}

func walkStepTemplates(value any, stepType string, path []string, visit func(string) error) error {
	if deferredStepTemplateSubtree(stepType, path) {
		return nil
	}
	switch typed := value.(type) {
	case string:
		return visit(typed)
	case []any:
		for index, item := range typed {
			if err := walkStepTemplates(item, stepType, append(path, fmt.Sprint(index)), visit); err != nil {
				return fmt.Errorf("item %d: %w", index, err)
			}
		}
	case map[string]any:
		for key, item := range typed {
			if err := walkStepTemplates(item, stepType, append(path, key), visit); err != nil {
				return fmt.Errorf("field %s: %w", key, err)
			}
		}
	}
	return nil
}

func transformStepTemplates(value any, stepType string, path []string, render func(string) (string, error)) (any, error) {
	if deferredStepTemplateSubtree(stepType, path) {
		return value, nil
	}
	switch typed := value.(type) {
	case string:
		return render(typed)
	case []any:
		result := make([]any, len(typed))
		for index, item := range typed {
			resolved, err := transformStepTemplates(item, stepType, append(path, fmt.Sprint(index)), render)
			if err != nil {
				return nil, fmt.Errorf("item %d: %w", index, err)
			}
			result[index] = resolved
		}
		return result, nil
	case map[string]any:
		result := make(map[string]any, len(typed))
		for key, item := range typed {
			resolved, err := transformStepTemplates(item, stepType, append(path, key), render)
			if err != nil {
				return nil, fmt.Errorf("field %s: %w", key, err)
			}
			result[key] = resolved
		}
		return result, nil
	default:
		return value, nil
	}
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
