package engine

import (
	"fmt"

	"github.com/expr-lang/expr"
	"github.com/expr-lang/expr/vm"
	wukoexpr "github.com/up2jj/wuko/expression"
	"github.com/up2jj/wuko/workflow"
)

type conditionEnvironment struct {
	Inputs   map[string]any    `expr:"inputs"`
	Vars     map[string]any    `expr:"vars"`
	Env      map[string]string `expr:"env"`
	Steps    map[string]any    `expr:"steps"`
	Batch    map[string]any    `expr:"batch"`
	Foreach  map[string]any    `expr:"foreach"`
	Matrix   map[string]any    `expr:"matrix"`
	Finally  map[string]any    `expr:"finally"`
	Workflow conditionWorkflow `expr:"workflow"`
	Run      conditionRun      `expr:"run"`
}

type conditionWorkflow struct {
	Name string `expr:"name"`
	Dir  string `expr:"dir"`
}

type conditionRun struct {
	Dir string `expr:"dir"`
}

func compileCondition(condition workflow.Condition) (*vm.Program, error) {
	program, err := wukoexpr.Compile(
		string(condition),
		expr.Env(conditionEnvironment{}),
		expr.AsBool(),
	)
	if err != nil {
		return nil, err
	}
	return program, nil
}

func evaluateCondition(condition workflow.Condition, environment conditionEnvironment) (bool, error) {
	if condition == "" {
		return true, nil
	}
	program, err := compileCondition(condition)
	if err != nil {
		return false, err
	}
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
	return conditionEnvironment{
		Inputs:  state.Inputs,
		Vars:    state.Vars,
		Env:     state.Env,
		Steps:   state.Steps,
		Batch:   bindingRoot(state.Bindings, "batch"),
		Foreach: bindingRoot(state.Bindings, "foreach"),
		Matrix:  bindingRoot(state.Bindings, "matrix"),
		Finally: bindingRoot(state.Bindings, "finally"),
		Workflow: conditionWorkflow{
			Name: definition.Name,
			Dir:  definition.Dir,
		},
		Run: conditionRun{Dir: runDir},
	}
}
