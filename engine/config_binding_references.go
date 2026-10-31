package engine

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/workflow"
)

func (validator *referenceValidator) validateConfigBindings(stepType string, value any, scope *referenceScope, path []string) (any, error) {
	label := "with." + strings.Join(path, ".")
	if workflow.StepOwnsBindings(stepType, path) {
		if binding, ok := workflow.OwnedBinding(stepType, path, value); ok {
			if binding.IsLiteral || binding.Expr == "" {
				return value, nil
			}
			return value, validator.validateExpression(label+" expr", binding.Expr, scope)
		}
		if rawSourceStep(stepType) && len(path) == 1 && path[0] == "source" {
			return value, nil
		}
		if stepType == "dashboard" || stepType == "tui_form" {
			return value, validator.validateDeferredStepTemplateValue(label, value, scope, stepType, path)
		}
		return value, validator.validateTemplateValue(label, value, scope, false)
	}
	if binding, ok := workflow.ParseBinding(value); ok && len(path) > 0 {
		if binding.IsLiteral {
			return binding.Literal, nil
		}
		// The engine reports a missing or non-string expression.
		if binding.Expr == "" {
			return step.DeferredValue{}, nil
		}
		if err := validator.validateTemplate(label+" expr", binding.Expr, scope); err != nil {
			return nil, err
		}
		return step.DeferredValue{}, validator.validateExpression(label+" expr", binding.Expr, scope)
	}
	switch typed := value.(type) {
	case string:
		return value, validator.validateTemplate(label, typed, scope)
	case map[string]any:
		result := make(map[string]any, len(typed))
		for _, name := range slices.Sorted(maps.Keys(typed)) {
			resolved, err := validator.validateConfigBindings(stepType, typed[name], scope, appendPath(path, name))
			if err != nil {
				return nil, fmt.Errorf("field %s: %w", name, err)
			}
			result[name] = resolved
		}
		return result, nil
	case []any:
		result := make([]any, len(typed))
		for i, child := range typed {
			resolved, err := validator.validateConfigBindings(stepType, child, scope, appendPath(path, fmt.Sprint(i)))
			if err != nil {
				return nil, fmt.Errorf("item %d: %w", i, err)
			}
			result[i] = resolved
		}
		return result, nil
	default:
		return value, nil
	}
}
