package engine

import (
	"strings"

	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/workflow"
)

// validateConfigBindings checks the templates and expressions of a built-in step
// configuration against the reference scope. It returns the configuration with literals
// unwrapped and expression bindings replaced by step.DeferredValue.
func (validator *referenceValidator) validateConfigBindings(stepType string, raw map[string]any, scope *referenceScope) (map[string]any, error) {
	label := func(path []string) string { return "with." + strings.Join(path, ".") }
	value, err := workflow.TransformStepConfig(stepType, raw, workflow.StepConfigVisitor{
		Owned: func(value any, path []string) (any, error) {
			if binding, ok := workflow.OwnedBinding(stepType, path, value); ok {
				if binding.IsLiteral || binding.Expr == "" {
					return value, nil
				}
				return value, validator.validateExpression(label(path)+" expr", binding.Expr, scope)
			}
			if rawSourceStep(stepType) && len(path) == 1 && path[0] == "source" {
				return value, nil
			}
			if stepType == "dashboard" || stepType == "tui_form" {
				return value, validator.validateDeferredStepTemplateValue(label(path), value, scope, stepType, path)
			}
			return value, validator.validateTemplateValue(label(path), value, scope, false)
		},
		Binding: func(binding workflow.Binding, path []string) (any, error) {
			if binding.IsLiteral {
				return binding.Literal, nil
			}
			// The engine reports a missing or non-string expression.
			if binding.Expr == "" {
				return step.DeferredValue{}, nil
			}
			if err := validator.validateTemplate(label(path)+" expr", binding.Expr, scope); err != nil {
				return nil, err
			}
			return step.DeferredValue{}, validator.validateExpression(label(path)+" expr", binding.Expr, scope)
		},
		Text: func(text string, path []string) (any, error) {
			return text, validator.validateTemplate(label(path), text, scope)
		},
	})
	if err != nil {
		return nil, err
	}
	return value.(map[string]any), nil
}
