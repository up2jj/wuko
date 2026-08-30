package engine

import (
	"fmt"

	"github.com/expr-lang/expr"
	"github.com/expr-lang/expr/vm"
	wukoexpr "github.com/up2jj/wuko/expression"
	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/workflow"
)

type conditionEnvironment = map[string]any

type conditionRun struct {
	Dir string `expr:"dir"`
}

func (e *Engine) compileCondition(condition workflow.Condition) (*vm.Program, error) {
	program, err := wukoexpr.Compile(
		string(condition),
		expr.Env(e.conditionEnvironmentShape()),
		expr.AsBool(),
	)
	if err != nil {
		return nil, err
	}
	return program, nil
}

func (e *Engine) evaluateCondition(condition workflow.Condition, environment conditionEnvironment) (bool, error) {
	if condition == "" {
		return true, nil
	}
	program, err := e.compileCondition(condition)
	if err != nil {
		return false, err
	}
	return evaluateConditionProgram(program, environment)
}

func (e *Engine) conditionEnvironmentShape() conditionEnvironment {
	shape := conditionEnvironment{
		"inputs": map[string]any{}, "vars": map[string]any{}, "env": map[string]string{},
		"steps": map[string]any{}, "dependencies": map[string]map[string]any{},
		"batch": map[string]any{}, "foreach": map[string]any{}, "matrix": map[string]any{},
		"finally": map[string]any{}, "error": map[string]any{},
		"workflow": step.WorkflowValue{}, "run": conditionRun{},
	}
	for _, control := range e.backgroundControls {
		shape[control.BindingRoot()] = map[string]any{}
	}
	return shape
}

func evaluateConditionProgram(program *vm.Program, environment conditionEnvironment) (bool, error) {
	value, err := expr.Run(program, environment)
	if err != nil {
		return false, err
	}
	result, ok := value.(bool)
	if !ok {
		return false, fmt.Errorf("expression returned %T, want bool", value)
	}
	return result, nil
}

func makeConditionEnvironment(definition *workflow.Definition, runDir string, state *State) conditionEnvironment {
	environment := conditionEnvironment{
		"inputs":       state.Inputs,
		"vars":         state.Vars,
		"env":          state.Env,
		"steps":        state.Steps,
		"dependencies": state.Dependencies,
		"workflow": step.WorkflowValue{
			Name: definition.Name, Dir: definition.Dir, Timezone: definition.Timezone,
		},
		"run":   conditionRun{Dir: runDir},
		"batch": bindingRoot(state.Bindings, "batch"), "foreach": bindingRoot(state.Bindings, "foreach"),
		"matrix": bindingRoot(state.Bindings, "matrix"), "finally": bindingRoot(state.Bindings, "finally"),
		"error": bindingRoot(state.Bindings, "error"),
	}
	for name, value := range state.Bindings {
		environment[name] = value
	}
	return environment
}
