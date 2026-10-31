package engine

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/expr-lang/expr"
	"github.com/expr-lang/expr/vm"
	wukoexpr "github.com/up2jj/wuko/expression"
	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/workflow"
)

// prepareStepConfig resolves only the bindings a registration explicitly entrusts to the
// host. Plugins and other registrations retain the original template-only configuration path.
// While validating, expression bindings become step.DeferredValue.
func (e *Engine) prepareStepConfig(definition *workflow.Definition, declaration workflow.Step, options Options, state *State, validate bool) (map[string]any, error) {
	shape := e.registry.ConfigShape(declaration.Type)
	if shape == nil || !workflow.HasStepBindings(declaration.Type, declaration.With) {
		if validate {
			return declaration.With, validateStepTemplates(options.renderer, declaration.With, declaration.Type)
		}
		value, err := renderStepValue(options.renderer, declaration.With, templateData(definition, options.RunDir, options.Interactive, state), declaration.Type)
		if err != nil {
			return nil, err
		}
		return value.(map[string]any), nil
	}
	resolver := &bindingResolver{engine: e, stepType: declaration.Type, shape: shape, renderer: options.renderer, validate: validate}
	if !validate {
		resolver.data = templateData(definition, options.RunDir, options.Interactive, state)
		resolver.environment = makeConditionEnvironment(definition, options.RunDir, options.Interactive, state)
	}
	return resolver.resolve(declaration.With)
}

// bindingResolver walks one step configuration, rendering templates and evaluating
// host-owned bindings, or only validating them when validate is set.
type bindingResolver struct {
	engine   *Engine
	stepType string
	shape    any
	renderer *workflow.Renderer
	validate bool
	// data and environment are set only when resolving at run time.
	data        map[string]any
	environment conditionEnvironment
}

func (r *bindingResolver) resolve(config map[string]any) (map[string]any, error) {
	value, err := workflow.TransformStepConfig(r.stepType, config, workflow.StepConfigVisitor{
		Owned:   r.resolveOwned,
		Binding: r.resolveBinding,
		Text:    func(text string, _ []string) (any, error) { return r.template(text) },
	})
	if err != nil {
		return nil, err
	}
	return value.(map[string]any), nil
}

// resolveOwned keeps a step's own grammar and rendering timing: only workflow templates
// outside deferred subtrees are rendered now.
func (r *bindingResolver) resolveOwned(value any, path []string) (any, error) {
	if binding, ok := workflow.OwnedBinding(r.stepType, path, value); ok {
		if r.validate && !binding.IsLiteral {
			return value, r.renderer.Validate(binding.Expr)
		}
		return value, nil
	}
	if len(path) == 1 && path[0] == runtimeRawField(r.stepType) && (!r.validate || rawSourceStep(r.stepType)) {
		return value, nil
	}
	if r.validate {
		return value, walkStepTemplates(value, r.stepType, path, r.renderer.Validate)
	}
	return transformStepTemplates(value, r.stepType, path, r.template)
}

func (r *bindingResolver) resolveBinding(binding workflow.Binding, path []string) (any, error) {
	if binding.IsLiteral {
		return binding.Literal, step.CheckBindingType(r.shape, path, binding.Literal)
	}
	if workflow.StaticStepBindingField(r.stepType, path) {
		return nil, fmt.Errorf("%s must be static", strings.Join(path, "."))
	}
	if strings.TrimSpace(binding.Expr) == "" {
		return nil, fmt.Errorf("expr must be a non-empty string")
	}
	if r.validate {
		if err := r.renderer.Validate(binding.Expr); err != nil {
			return nil, err
		}
		// A templated expression is known only after rendering.
		if !strings.Contains(binding.Expr, "{{") {
			program, err := r.engine.compileBinding(binding.Expr)
			if err != nil {
				return nil, fmt.Errorf("compiling expr: %w", err)
			}
			if resultType := program.Node().Type(); resultType != nil && resultType.Kind() != reflect.Interface {
				if err := step.CheckBindingType(r.shape, path, reflect.Zero(resultType).Interface()); err != nil {
					return nil, err
				}
			}
		}
		return step.DeferredValue{}, nil
	}
	source, err := r.template(binding.Expr)
	if err != nil {
		return nil, err
	}
	program, err := r.engine.compileBinding(source)
	if err != nil {
		return nil, fmt.Errorf("compiling expr: %w", err)
	}
	resolved, err := expr.Run(program, r.environment)
	if err != nil {
		return nil, fmt.Errorf("evaluating expr: %w", err)
	}
	return resolved, step.CheckBindingType(r.shape, path, resolved)
}

// template validates or renders one workflow template string.
func (r *bindingResolver) template(text string) (string, error) {
	if r.validate {
		return text, r.renderer.Validate(text)
	}
	if !strings.Contains(text, "{{") {
		return text, nil
	}
	return r.renderer.Render(text, r.data)
}

// compileBinding compiles an expression binding against the same scope as step conditions,
// so validation and execution agree on the available roots.
func (e *Engine) compileBinding(source string) (*vm.Program, error) {
	return wukoexpr.Compile(source, expr.Env(e.conditionEnvironmentShape()), expr.AllowUndefinedVariables())
}
