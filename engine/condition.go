package engine

import (
	"fmt"

	"github.com/expr-lang/expr"
	"github.com/expr-lang/expr/vm"
	wukoexpr "github.com/up2jj/wuko/expression"
	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/workflow"
)

type conditionEnvironment struct {
	Inputs       map[string]any            `expr:"inputs"`
	Vars         map[string]any            `expr:"vars"`
	Env          map[string]string         `expr:"env"`
	Steps        map[string]any            `expr:"steps"`
	Dependencies map[string]map[string]any `expr:"dependencies"`
	Batch        map[string]any            `expr:"batch"`
	Foreach      map[string]any            `expr:"foreach"`
	Matrix       map[string]any            `expr:"matrix"`
	Finally      map[string]any            `expr:"finally"`
	Error        map[string]any            `expr:"error"`
	Workflow     step.WorkflowValue        `expr:"workflow"`
	Run          conditionRun              `expr:"run"`
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
	return evaluateConditionProgram(program, environment)
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
	return conditionEnvironment{
		Inputs:       state.Inputs,
		Vars:         state.Vars,
		Env:          state.Env,
		Steps:        state.Steps,
		Dependencies: state.Dependencies,
		Batch:        bindingRoot(state.Bindings, "batch"),
		Foreach:      bindingRoot(state.Bindings, "foreach"),
		Matrix:       bindingRoot(state.Bindings, "matrix"),
		Finally:      bindingRoot(state.Bindings, "finally"),
		Error:        bindingRoot(state.Bindings, "error"),
		Workflow: step.WorkflowValue{
			Name: definition.Name, Dir: definition.Dir, Timezone: definition.Timezone,
		},
		Run: conditionRun{Dir: runDir},
	}
}
