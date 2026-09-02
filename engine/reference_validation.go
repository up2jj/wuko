package engine

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/expr-lang/expr/ast"
	"github.com/expr-lang/expr/parser"
	"github.com/up2jj/wuko/workflow"
)

// referenceSchema describes only the map keys Wuko already knows statically.
// open schemas deliberately stop validation: step output shapes and arbitrary
// runtime values are not part of the workflow declaration.
type referenceSchema struct {
	open   bool
	fields map[string]*referenceSchema
}

type referenceScope struct {
	roots map[string]*referenceSchema
}

type deferredReferences struct {
	owner string
	steps []workflow.Step
}

type referenceValidator struct {
	definition      *workflow.Definition
	renderer        *workflow.Renderer
	initial         *referenceScope
	actions         map[*workflow.Action]struct{}
	controls        []BackgroundControl
	templateRoots   map[string]struct{}
	expressionRoots map[string]struct{}
}

var (
	openReference     = &referenceSchema{open: true}
	leafReference     = &referenceSchema{}
	baseTemplateRoots = map[string]struct{}{
		"inputs": {}, "vars": {}, "env": {}, "steps": {}, "dependencies": {},
		"batch": {}, "foreach": {}, "matrix": {}, "finally": {}, "error": {},
		"workflow": {}, "run": {},
	}
	baseExpressionRoots = map[string]struct{}{
		"inputs": {}, "vars": {}, "env": {}, "steps": {}, "dependencies": {},
		"batch": {}, "foreach": {}, "matrix": {}, "finally": {}, "error": {},
		"workflow": {}, "run": {}, "cancel_on": {}, "monitors": {},
		"result": {}, "poll": {}, "current": {}, "path": {}, "index": {},
		"item": {}, "label": {}, "value": {}, "description": {}, "disabled": {}, "reason": {},
	}
)

func (e *Engine) validateDataReferences(definition *workflow.Definition, options Options, state *State) error {
	validator := &referenceValidator{
		definition: definition, renderer: options.renderer,
		actions: make(map[*workflow.Action]struct{}), controls: e.backgroundControls,
		templateRoots: maps.Clone(baseTemplateRoots), expressionRoots: maps.Clone(baseExpressionRoots),
	}
	for _, control := range validator.controls {
		validator.templateRoots[control.BindingRoot()] = struct{}{}
		validator.expressionRoots[control.BindingRoot()] = struct{}{}
	}
	validator.initial = newReferenceScope(state)
	return validator.validateDefinition()
}

func newReferenceScope(state *State) *referenceScope {
	scope := &referenceScope{roots: map[string]*referenceSchema{
		"inputs":       schemaForAnyMap(state.Inputs),
		"vars":         schemaForAnyMap(state.Vars),
		"env":          openReference,
		"steps":        schemaForAnyMap(state.Steps),
		"dependencies": schemaForDependencies(state.Dependencies),
		"workflow":     closedReference("name", "dir", "timezone"),
		"run":          closedReference("dir", "environment_loaders"),
	}}
	for name, value := range state.Bindings {
		scope.addBinding(name, value)
	}
	return scope
}

func schemaForAnyMap(values map[string]any) *referenceSchema {
	result := &referenceSchema{fields: make(map[string]*referenceSchema, len(values))}
	for name := range values {
		result.fields[name] = openReference
	}
	return result
}

func schemaForDependencies(values map[string]map[string]any) *referenceSchema {
	result := &referenceSchema{fields: make(map[string]*referenceSchema, len(values))}
	for alias, outputs := range values {
		result.fields[alias] = schemaForAnyMap(outputs)
	}
	return result
}

// varWriters are step types whose "variable" field names the workflow variable
// they assign; extract names its targets in a "variables" map instead.
// dynamicVarWriters produce variable names that are only known at run time, so
// they stop variable validation for everything that follows.
var (
	varWriters = map[string]struct{}{
		"set": {}, "jsonpath": {}, "semver": {}, "key_value": {}, "tui_choice": {}, "tui_confirm": {},
		"tui_input": {}, "tui_password": {}, "tui_path": {}, "tui_review": {}, "time": {},
	}
	dynamicVarWriters = map[string]struct{}{"lua": {}, "import_vars": {}}
)

func (scope *referenceScope) addWrittenVariables(stepType, stepID string, raw map[string]any) {
	if _, dynamic := dynamicVarWriters[stepType]; dynamic {
		scope.roots["vars"] = openReference
		return
	}
	if _, writes := varWriters[stepType]; writes {
		variable := stringValue(raw, "variable")
		if stepType == "time" && variable == "" {
			variable = stepID
		}
		scope.addVariable(variable)
		return
	}
	if stepType == "extract" {
		targets, _ := raw["variables"].(map[string]any)
		for _, target := range targets {
			name, _ := target.(string)
			scope.addVariable(name)
		}
	}
}

// mergeVariables adopts the variables another scope declared. Concurrent
// branches and try/catch bodies namespace their step IDs away but commit their
// variable writes to the enclosing run.
func (scope *referenceScope) mergeVariables(other *referenceScope) {
	if other == nil {
		return
	}
	source := other.roots["vars"]
	if source == nil {
		return
	}
	target := scope.roots["vars"]
	if target == nil || target.open {
		return
	}
	if source.open {
		scope.roots["vars"] = openReference
		return
	}
	for name, schema := range source.fields {
		if _, declared := target.fields[name]; !declared {
			target.fields[name] = cloneReferenceSchema(schema)
		}
	}
}

func (scope *referenceScope) mergeSteps(source *referenceSchema) {
	if source == nil {
		return
	}
	target := scope.roots["steps"]
	if target == nil || target.open {
		return
	}
	if source.open {
		scope.roots["steps"] = openReference
		return
	}
	for id, schema := range source.fields {
		target.fields[id] = cloneReferenceSchema(schema)
	}
}

func (scope *referenceScope) addVariable(name string) {
	if name == "" || strings.Contains(name, "{{") {
		return
	}
	vars := scope.roots["vars"]
	if vars == nil || vars.open {
		return
	}
	vars.fields[name] = openReference
}

func closedReference(names ...string) *referenceSchema {
	result := &referenceSchema{fields: make(map[string]*referenceSchema, len(names))}
	for _, name := range names {
		result.fields[name] = leafReference
	}
	return result
}

func (scope *referenceScope) clone() *referenceScope {
	result := &referenceScope{roots: make(map[string]*referenceSchema, len(scope.roots))}
	for name, schema := range scope.roots {
		result.roots[name] = cloneReferenceSchema(schema)
	}
	return result
}

func cloneReferenceSchema(schema *referenceSchema) *referenceSchema {
	if schema == nil || schema.open {
		return schema
	}
	result := &referenceSchema{fields: make(map[string]*referenceSchema, len(schema.fields))}
	for name, child := range schema.fields {
		result.fields[name] = cloneReferenceSchema(child)
	}
	return result
}

func (scope *referenceScope) addStep(id string) {
	if id == "" {
		return
	}
	steps := scope.roots["steps"]
	if steps == nil {
		steps = &referenceSchema{fields: make(map[string]*referenceSchema)}
		scope.roots["steps"] = steps
	}
	steps.fields[id] = openReference
}

func (scope *referenceScope) addBinding(name string, value any) {
	switch name {
	case "batch":
		scope.roots[name] = &referenceSchema{fields: map[string]*referenceSchema{"index": leafReference, "items": openReference}}
	case "foreach":
		scope.roots[name] = &referenceSchema{fields: map[string]*referenceSchema{"index": leafReference, "item": openReference}}
	case "matrix":
		fields := make(map[string]*referenceSchema)
		if values, ok := value.(map[string]any); ok {
			for axis := range values {
				fields[axis] = openReference
			}
		}
		scope.roots[name] = &referenceSchema{fields: fields}
	case "finally":
		scope.roots[name] = &referenceSchema{fields: map[string]*referenceSchema{
			"status": leafReference,
			"errors": openReference,
		}}
	case "error":
		scope.roots[name] = &referenceSchema{fields: map[string]*referenceSchema{
			"status":  leafReference,
			"message": leafReference,
			"step":    leafReference,
			"type":    leafReference,
			"errors":  openReference,
		}}
	}
}

func (scope *referenceScope) validate(path []string, known map[string]struct{}) error {
	if len(path) == 0 {
		return nil
	}
	schema, exists := scope.roots[path[0]]
	if !exists {
		if _, knownRoot := known[path[0]]; knownRoot {
			return fmt.Errorf("data root %q is not available here", path[0])
		}
		return nil
	}
	for index, field := range path[1:] {
		if schema == nil || schema.open {
			return nil
		}
		child, exists := schema.fields[field]
		if !exists {
			return referenceFieldError(path[0], index, field)
		}
		schema = child
	}
	return nil
}

func referenceFieldError(root string, depth int, field string) error {
	if depth == 0 {
		switch root {
		case "vars":
			return fmt.Errorf("variable %q is not declared", field)
		case "inputs":
			return fmt.Errorf("input %q is not declared", field)
		case "env":
			return fmt.Errorf("environment value %q is not available", field)
		case "steps":
			return fmt.Errorf("step %q is not available here", field)
		case "dependencies":
			return fmt.Errorf("dependency alias %q is not declared", field)
		}
	}
	if root == "dependencies" && depth == 1 {
		return fmt.Errorf("dependency output %q is not declared", field)
	}
	return fmt.Errorf("field %q is not available in %s", field, root)
}

func (validator *referenceValidator) validateDefinition() error {
	if err := validator.validateNamedTemplates(); err != nil {
		return err
	}

	mainScope, defers, err := validator.validateSteps(validator.definition.Steps, validator.initial.clone())
	if err != nil {
		return err
	}
	mainScope, err = validator.validateDeferred(defers, mainScope)
	if err != nil {
		return err
	}
	if len(validator.definition.Finally) > 0 {
		mainScope = withFinally(mainScope)
		mainScope, _, err = validator.validateSteps(validator.definition.Finally, mainScope)
		if err != nil {
			return fmt.Errorf("finally: %w", err)
		}
	}
	for _, name := range slices.Sorted(maps.Keys(validator.definition.Outputs)) {
		if err := validator.validateExpression("workflow output "+fmt.Sprintf("%q", name), validator.definition.Outputs[name].Value, mainScope); err != nil {
			return err
		}
	}

	for _, lifecycle := range []struct {
		name  string
		steps []workflow.Step
	}{{"install", validator.definition.Install}, {"uninstall", validator.definition.Uninstall}} {
		if len(lifecycle.steps) == 0 {
			continue
		}
		scope, lifecycleDefers, err := validator.validateSteps(lifecycle.steps, validator.initial.clone())
		if err != nil {
			return fmt.Errorf("%s: %w", lifecycle.name, err)
		}
		if _, err := validator.validateDeferred(lifecycleDefers, scope); err != nil {
			return fmt.Errorf("%s: %w", lifecycle.name, err)
		}
	}
	return nil
}

func (validator *referenceValidator) validateNamedTemplates() error {
	global := validator.initial.clone()
	for _, steps := range [][]workflow.Step{
		validator.definition.Steps, validator.definition.Finally,
		validator.definition.Install, validator.definition.Uninstall,
	} {
		addDeclaredStepIDs(global, steps)
	}
	addGlobalTemplateBindings(global, validator.definition)
	for _, control := range validator.controls {
		// Named templates are validated once for every call site, so a control binding
		// has to be in scope here the same way validateBackgroundControl puts it in
		// scope for the body; addBinding only knows the built-in binding shapes.
		global.roots[control.BindingRoot()] = openReference
	}
	return validator.renderer.WalkNamedDataReferences(func(name string, path []string) error {
		if err := global.validate(path, validator.templateRoots); err != nil {
			return fmt.Errorf("template %q: %w", name, err)
		}
		return nil
	})
}

func addDeclaredStepIDs(scope *referenceScope, steps []workflow.Step) {
	for _, step := range steps {
		scope.addStep(step.ID)
		if step.CancelOn != nil {
			for _, monitor := range step.CancelOn.Monitors {
				scope.addStep(monitor.ID)
			}
		}
		for _, child := range step.ChildSequences() {
			addDeclaredStepIDs(scope, child.Steps)
		}
	}
}

func addGlobalTemplateBindings(scope *referenceScope, definition *workflow.Definition) {
	scope.addBinding("batch", nil)
	scope.addBinding("foreach", nil)
	scope.addBinding("finally", nil)
	scope.addBinding("error", nil)
	matrix := make(map[string]any)
	var collectAxes func([]workflow.Step)
	collectAxes = func(steps []workflow.Step) {
		for _, step := range steps {
			if step.Matrix != nil {
				for _, axis := range step.Matrix.Axes {
					matrix[axis.Name] = nil
				}
			}
			for _, child := range step.ChildSequences() {
				collectAxes(child.Steps)
			}
		}
	}
	collectAxes(definition.Steps)
	collectAxes(definition.Finally)
	collectAxes(definition.Install)
	collectAxes(definition.Uninstall)
	scope.addBinding("matrix", matrix)
}

func (validator *referenceValidator) validateSteps(steps []workflow.Step, scope *referenceScope) (*referenceScope, []deferredReferences, error) {
	var defers []deferredReferences
	for _, step := range steps {
		var nested []deferredReferences
		var err error
		scope, nested, err = validator.validateStep(step, scope)
		if err != nil {
			if step.ID != "" {
				return nil, nil, fmt.Errorf("step %q: %w", step.ID, err)
			}
			return nil, nil, err
		}
		defers = append(defers, nested...)
	}
	return scope, defers, nil
}

func (validator *referenceValidator) validateStep(step workflow.Step, scope *referenceScope) (*referenceScope, []deferredReferences, error) {
	switch {
	case step.IsTryCatch():
		return validator.validateTryCatch(step, scope)
	case step.IsCancelOn():
		return validator.validateCancelOn(step, scope)
	case step.IsBackgroundControl():
		control := validator.backgroundControl(step)
		if control == nil {
			return nil, nil, fmt.Errorf("background control %q is not registered", step.BackgroundControlKind())
		}
		return validator.validateBackgroundControl(step, scope, control)
	case step.IsOnce():
		return validator.validateOnce(step, scope)
	case step.IsExecutorBlock():
		return validator.validateExecutor(step, scope)
	case step.IsEnvironmentBlock():
		for _, name := range slices.Sorted(maps.Keys(step.Env)) {
			if err := validator.validateTemplate("env "+fmt.Sprintf("%q", name), step.Env[name], scope); err != nil {
				return nil, nil, err
			}
		}
		return validator.validateSteps(step.Steps, scope)
	case step.IsWorkingDirectoryBlock():
		if err := validator.validateTemplate("working_directory", step.WorkingDirectory, scope); err != nil {
			return nil, nil, err
		}
		return validator.validateSteps(step.Steps, scope)
	case step.IsWorktreeBlock():
		return validator.validateWorktree(step, scope)
	case step.IsConditionalBlock():
		if err := validator.validateExpression("if", string(step.If), scope); err != nil {
			return nil, nil, err
		}
		return validator.validateSteps(step.Steps, scope)
	case step.Concurrent != nil:
		return validator.validateConcurrent(step, scope)
	case step.Batch != nil || step.Foreach != nil || step.Matrix != nil:
		return validator.validateFanout(step, scope)
	case step.Loop != nil:
		return validator.validateLoop(step, scope)
	case step.Return != nil:
		if err := validator.validateExpression("if", string(step.If), scope); err != nil {
			return nil, nil, err
		}
		for _, name := range slices.Sorted(maps.Keys(step.Return.Outputs)) {
			if err := validator.validateExpression("return output "+fmt.Sprintf("%q", name), step.Return.Outputs[name], scope); err != nil {
				return nil, nil, err
			}
		}
		return scope, nil, nil
	default:
		return validator.validateOrdinaryStep(step, scope)
	}
}

func (validator *referenceValidator) validateOnce(step workflow.Step, scope *referenceScope) (*referenceScope, []deferredReferences, error) {
	if err := validator.validateExpression("if", string(step.If), scope); err != nil {
		return nil, nil, err
	}
	if err := validator.validateTemplate("once key", step.Once.Key, scope); err != nil {
		return nil, nil, err
	}
	private, _, err := validator.validateSteps(step.Once.Steps, scope.clone())
	if err != nil {
		return nil, nil, fmt.Errorf("once body: %w", err)
	}
	result := scope.clone()
	result.mergeVariables(private)
	result.addStep(step.ID)
	return result, nil, nil
}

func (validator *referenceValidator) validateOrdinaryStep(step workflow.Step, scope *referenceScope) (*referenceScope, []deferredReferences, error) {
	if err := validator.validateExpression("if", string(step.If), scope); err != nil {
		return nil, nil, err
	}
	if step.Retry != nil {
		if err := validator.validateTemplate("retry operation_id", step.Retry.OperationID, scope); err != nil {
			return nil, nil, err
		}
		retryScope := scope.clone()
		retryScope.roots["error"] = openReference
		if err := validator.validateExpression("retry when", string(step.Retry.When), retryScope); err != nil {
			return nil, nil, err
		}
	}
	if err := validator.validateActionSource(step, scope); err != nil {
		return nil, nil, err
	}
	if err := validator.validateStepConfiguration(step.ID, step.Type, step.With, scope); err != nil {
		return nil, nil, err
	}
	scope.addWrittenVariables(step.Type, step.ID, step.With)
	if step.Action != nil {
		if err := validator.validateTypedBindings("input", step.With, scope); err != nil {
			return nil, nil, err
		}
		if err := validator.validateAction(step.Action, scope); err != nil {
			return nil, nil, err
		}
	}
	scope.addStep(step.ID)
	var defers []deferredReferences
	if len(step.Defer) > 0 {
		defers = append(defers, deferredReferences{owner: step.ID, steps: step.Defer})
	}
	return scope, defers, nil
}

func (validator *referenceValidator) validateActionSource(step workflow.Step, scope *referenceScope) error {
	if err := validator.validateTemplate("uses", step.Uses.URL, scope); err != nil {
		return err
	}
	if err := validator.validateTemplate("uses command", step.Uses.Command, scope); err != nil {
		return err
	}
	for index, argument := range step.Uses.Args {
		if err := validator.validateTemplate(fmt.Sprintf("uses argument %d", index+1), argument, scope); err != nil {
			return err
		}
	}
	return nil
}

func (validator *referenceValidator) validateConcurrent(step workflow.Step, scope *referenceScope) (*referenceScope, []deferredReferences, error) {
	graph := newConcurrentGraph(step.Concurrent.Steps)
	remaining := make([]int, len(step.Concurrent.Steps))
	ready := make([]int, 0, len(step.Concurrent.Steps))
	branchScopes := make([]*referenceScope, len(step.Concurrent.Steps))
	for index, dependencies := range graph.dependencies {
		remaining[index] = len(dependencies)
		if len(dependencies) == 0 {
			ready = append(ready, index)
		}
	}
	for len(ready) > 0 {
		index := ready[0]
		ready = ready[1:]
		branchInput := scope.clone()
		for _, ancestor := range graph.ancestors[index] {
			branchInput.mergeSteps(selectedStepSchema([]workflow.Step{step.Concurrent.Steps[ancestor]}))
			branchInput.mergeVariables(branchScopes[ancestor])
		}
		branchScope, _, err := validator.validateSteps([]workflow.Step{step.Concurrent.Steps[index]}, branchInput)
		if err != nil {
			return nil, nil, fmt.Errorf("concurrent group: %w", err)
		}
		branchScopes[index] = branchScope
		for _, dependent := range graph.dependents[index] {
			remaining[dependent]--
			if remaining[dependent] == 0 {
				ready = append(ready, dependent)
			}
		}
		slices.Sort(ready)
	}

	result := scope.clone()
	for index, branch := range step.Concurrent.Steps {
		for id := range selectedStepSchema([]workflow.Step{branch}).fields {
			result.addStep(id)
		}
		result.mergeVariables(branchScopes[index])
	}
	return result, nil, nil
}

func (validator *referenceValidator) validateFanout(step workflow.Step, scope *referenceScope) (*referenceScope, []deferredReferences, error) {
	if err := validator.validateExpression("if", string(step.If), scope); err != nil {
		return nil, nil, err
	}
	var kind string
	var children []workflow.Step
	var collect string
	var bindings map[string]any
	switch {
	case step.Batch != nil:
		kind, children, collect = "batch", step.Batch.Steps, step.Batch.Collect
		bindings = map[string]any{"batch": map[string]any{"index": 0, "items": []any{}}}
		if err := validator.validateExpression("batch items", step.Batch.Items, scope); err != nil {
			return nil, nil, err
		}
		if err := validator.validateExpression("batch size", step.Batch.Size.Expression, scope); err != nil {
			return nil, nil, err
		}
	case step.Matrix != nil:
		kind, children, collect = "matrix", step.Matrix.Steps, step.Matrix.Collect
		matrix := make(map[string]any, len(step.Matrix.Axes))
		for _, axis := range step.Matrix.Axes {
			if err := validator.validateExpression("matrix axis "+fmt.Sprintf("%q", axis.Name), axis.Expression, scope); err != nil {
				return nil, nil, err
			}
			matrix[axis.Name] = nil
		}
		bindings = map[string]any{"matrix": matrix}
	case step.Foreach != nil:
		kind, children, collect = "foreach", step.Foreach.Steps, step.Foreach.Collect
		bindings = map[string]any{"foreach": map[string]any{"index": 0, "item": nil}}
		if err := validator.validateExpression("foreach items", step.Foreach.Items, scope); err != nil {
			return nil, nil, err
		}
	}
	childScope := scope.clone()
	for name, value := range bindings {
		childScope.addBinding(name, value)
	}
	childScope, _, err := validator.validateSteps(children, childScope)
	if err != nil {
		return nil, nil, fmt.Errorf("%s body: %w", kind, err)
	}
	if err := validator.validateExpression(kind+" collect", collect, childScope); err != nil {
		return nil, nil, err
	}
	result := scope.clone()
	result.addStep(step.ID)
	return result, nil, nil
}

func (validator *referenceValidator) validateLoop(step workflow.Step, scope *referenceScope) (*referenceScope, []deferredReferences, error) {
	bodyScope, defers, err := validator.validateSteps(step.Loop.Steps, scope)
	if err != nil {
		return nil, nil, fmt.Errorf("loop body: %w", err)
	}
	if err := validator.validateExpression("loop until", string(step.Loop.Until), bodyScope); err != nil {
		return nil, nil, err
	}
	if err := validator.validateExpression("loop delay", step.Loop.Delay.Expression, bodyScope); err != nil {
		return nil, nil, err
	}
	bodyScope.addStep(step.ID)
	return bodyScope, defers, nil
}

func (validator *referenceValidator) validateTryCatch(step workflow.Step, scope *referenceScope) (*referenceScope, []deferredReferences, error) {
	if err := validator.validateExpression("if", string(step.If), scope); err != nil {
		return nil, nil, err
	}
	private, tryDefers, err := validator.validateSteps(step.Try.Steps, scope.clone())
	if err != nil {
		return nil, nil, fmt.Errorf("try: %w", err)
	}
	private.addBinding("error", nil)
	private, catchDefers, err := validator.validateSteps(step.Catch.Steps, private)
	if err != nil {
		return nil, nil, fmt.Errorf("catch: %w", err)
	}
	delete(private.roots, "error")
	private, err = validator.validateDeferred(append(tryDefers, catchDefers...), private)
	if err != nil {
		return nil, nil, err
	}
	result := scope.clone()
	result.addStep(step.ID)
	result.mergeVariables(private)
	return result, nil, nil
}

func (validator *referenceValidator) validateCancelOn(step workflow.Step, scope *referenceScope) (*referenceScope, []deferredReferences, error) {
	if err := validator.validateExpression("if", string(step.If), scope); err != nil {
		return nil, nil, err
	}
	for index := range step.CancelOn.Monitors {
		declaration := step.CancelOn.MonitorDeclaration(index)
		if _, _, err := validator.validateSteps([]workflow.Step{declaration}, scope.clone()); err != nil {
			return nil, nil, fmt.Errorf("cancel_on monitor %q: %w", step.CancelOn.Monitors[index].ID, err)
		}
	}
	if _, _, err := validator.validateSteps(step.CancelOn.Steps, scope.clone()); err != nil {
		return nil, nil, fmt.Errorf("cancel_on body: %w", err)
	}
	collectScope := scope.clone()
	collectScope.roots["steps"] = selectedStepSchema(step.CancelOn.Steps)
	collectScope.roots["vars"] = cloneReferenceSchema(scope.roots["vars"])
	collectScope.roots["monitors"] = monitorSchema(step.CancelOn.Monitors)
	collectScope.roots["cancel_on"] = &referenceSchema{fields: map[string]*referenceSchema{
		"ok": leafReference, "triggered": leafReference, "status": leafReference, "error": leafReference,
		"winner": {fields: map[string]*referenceSchema{"monitor": leafReference, "kind": leafReference}},
	}}
	if err := validator.validateExpression("cancel_on collect", step.CancelOn.Collect, collectScope); err != nil {
		return nil, nil, err
	}
	result := scope.clone()
	result.addStep(step.ID)
	return result, nil, nil
}

func (validator *referenceValidator) backgroundControl(step workflow.Step) BackgroundControl {
	for _, control := range validator.controls {
		if control.Matches(step) {
			return control
		}
	}
	return nil
}

func (validator *referenceValidator) validateBackgroundControl(step workflow.Step, scope *referenceScope, control BackgroundControl) (*referenceScope, []deferredReferences, error) {
	kind := control.Kind()
	if err := validator.validateTemplateValue(kind+" configuration", control.Configuration(step), scope, false); err != nil {
		return nil, nil, err
	}
	private := scope.clone()
	private.roots[control.BindingRoot()] = openReference
	if _, _, err := validator.validateSteps(control.Body(step), private); err != nil {
		return nil, nil, fmt.Errorf("%s body: %w", kind, err)
	}
	result := scope.clone()
	result.addStep(step.ID)
	return result, nil, nil
}

func selectedStepSchema(steps []workflow.Step) *referenceSchema {
	result := &referenceSchema{fields: make(map[string]*referenceSchema)}
	for _, step := range steps {
		if step.IsExecutorBlock() || step.IsEnvironmentBlock() || step.IsWorkingDirectoryBlock() || step.IsConditionalBlock() || step.Concurrent != nil {
			for _, child := range step.ChildSequences() {
				maps.Copy(result.fields, selectedStepSchema(child.Steps).fields)
			}
			continue
		}
		if step.ID != "" {
			result.fields[step.ID] = openReference
		}
	}
	return result
}

func monitorSchema(monitors []workflow.Step) *referenceSchema {
	result := &referenceSchema{fields: make(map[string]*referenceSchema, len(monitors))}
	for _, monitor := range monitors {
		result.fields[monitor.ID] = openReference
	}
	return result
}

func (validator *referenceValidator) validateExecutor(step workflow.Step, scope *referenceScope) (*referenceScope, []deferredReferences, error) {
	if err := validator.validateTemplateValue("executor with", step.Executor.With, scope, false); err != nil {
		return nil, nil, err
	}
	body, defers, err := validator.validateSteps(step.Steps, scope)
	if err != nil {
		return nil, nil, fmt.Errorf("executor steps: %w", err)
	}
	body, err = validator.validateDeferred(defers, body)
	if err != nil {
		return nil, nil, err
	}
	if len(step.Finally) > 0 {
		body = withFinally(body)
		body, _, err = validator.validateSteps(step.Finally, body)
		if err != nil {
			return nil, nil, fmt.Errorf("executor finally: %w", err)
		}
	}
	return body, nil, nil
}

func (validator *referenceValidator) validateWorktree(step workflow.Step, scope *referenceScope) (*referenceScope, []deferredReferences, error) {
	for label, value := range map[string]string{"worktree revision": step.Worktree.Revision, "worktree path": step.Worktree.Path} {
		if err := validator.validateTemplate(label, value, scope); err != nil {
			return nil, nil, err
		}
	}
	body, defers, err := validator.validateSteps(step.Worktree.Steps, scope)
	if err != nil {
		return nil, nil, fmt.Errorf("worktree body: %w", err)
	}
	body, err = validator.validateDeferred(defers, body)
	if err != nil {
		return nil, nil, err
	}
	if step.Worktree.Publish != nil {
		if err := validator.validateTemplate("worktree publish branch", step.Worktree.Publish.Branch, body); err != nil {
			return nil, nil, err
		}
	}
	body.addStep(step.ID)
	return body, nil, nil
}

func (validator *referenceValidator) validateDeferred(groups []deferredReferences, scope *referenceScope) (*referenceScope, error) {
	if len(groups) == 0 {
		return scope, nil
	}
	outer, bound := scope.roots["finally"]
	scope = withFinally(scope)
	for index := len(groups) - 1; index >= 0; index-- {
		var err error
		scope, _, err = validator.validateSteps(groups[index].steps, scope)
		if err != nil {
			return nil, fmt.Errorf("step %q defer: %w", groups[index].owner, err)
		}
	}
	// The binding is gone once the defers have run; the steps they declared stay.
	if bound {
		scope.roots["finally"] = cloneReferenceSchema(outer)
	} else {
		delete(scope.roots, "finally")
	}
	return scope, nil
}

func withFinally(scope *referenceScope) *referenceScope {
	result := scope.clone()
	result.addBinding("finally", nil)
	return result
}

func (validator *referenceValidator) validateAction(action *workflow.Action, caller *referenceScope) error {
	if _, seen := validator.actions[action]; seen {
		return nil
	}
	validator.actions[action] = struct{}{}
	renderer, err := workflow.NewRenderer(action.Templates)
	if err != nil {
		return err
	}
	inputs := &referenceSchema{fields: make(map[string]*referenceSchema, len(action.Inputs))}
	for name := range action.Inputs {
		inputs.fields[name] = openReference
	}
	scope := caller.clone()
	scope.roots["inputs"] = inputs
	scope.roots["vars"] = &referenceSchema{fields: make(map[string]*referenceSchema)}
	scope.roots["steps"] = &referenceSchema{fields: make(map[string]*referenceSchema)}
	scope.roots["dependencies"] = &referenceSchema{fields: make(map[string]*referenceSchema)}
	for _, binding := range []string{"batch", "foreach", "matrix", "finally", "error"} {
		delete(scope.roots, binding)
	}
	for _, control := range validator.controls {
		delete(scope.roots, control.BindingRoot())
	}
	inner := &workflow.Definition{Version: 1, Name: action.Name, Templates: action.Templates, Dir: action.Dir, Steps: action.Steps, Finally: action.Finally, Vars: map[string]any{}, Env: workflow.Environment{}, Location: action.Location}
	inner.InheritSecretSession(validator.definition)
	child := &referenceValidator{
		definition: inner, renderer: renderer, initial: scope, actions: validator.actions,
		controls: validator.controls, templateRoots: validator.templateRoots, expressionRoots: validator.expressionRoots,
	}
	if err := child.validateNamedTemplates(); err != nil {
		return fmt.Errorf("action %q: %w", action.Name, err)
	}
	result, defers, err := child.validateSteps(action.Steps, scope.clone())
	if err != nil {
		return fmt.Errorf("action %q: %w", action.Name, err)
	}
	result, err = child.validateDeferred(defers, result)
	if err != nil {
		return fmt.Errorf("action %q: %w", action.Name, err)
	}
	if len(action.Finally) > 0 {
		result = withFinally(result)
		result, _, err = child.validateSteps(action.Finally, result)
		if err != nil {
			return fmt.Errorf("action %q finally: %w", action.Name, err)
		}
	}
	for _, name := range slices.Sorted(maps.Keys(action.Outputs)) {
		if err := child.validateExpression("action output "+fmt.Sprintf("%q", name), action.Outputs[name].Value, result); err != nil {
			return fmt.Errorf("action %q: %w", action.Name, err)
		}
	}
	return nil
}

func (validator *referenceValidator) validateStepConfiguration(stepID, stepType string, raw map[string]any, scope *referenceScope) error {
	if stepType == "wait" {
		for _, key := range slices.Sorted(maps.Keys(raw)) {
			if key == "step" || key == "until" {
				continue
			}
			if err := validator.validateTemplateValue("with field "+key, raw[key], scope, false); err != nil {
				return err
			}
		}
		if nested := nestedMap(raw, "step"); nested != nil {
			nestedType, _ := nested["type"].(string)
			if err := validator.validateStepConfiguration(stepID, nestedType, nestedMap(nested, "with"), scope); err != nil {
				return fmt.Errorf("nested step: %w", err)
			}
			scope.addWrittenVariables(nestedType, stepID, nestedMap(nested, "with"))
			waitScope := scope.clone()
			waitScope.roots["result"] = openReference
			waitScope.roots["error"] = openReference
			waitScope.roots["poll"] = leafReference
			return validator.validateRawExpression("until", raw, "until", waitScope)
		}
		return nil
	}
	if err := validator.validateTemplateValue("with", raw, scope, stepType == "lua"); err != nil {
		return err
	}
	switch stepType {
	case "assert", "set":
		return validator.validateRawExpression("expr", raw, "expr", scope)
	case "edit":
		if err := validator.validateRawExpression("from.expr", nestedMap(raw, "from"), "expr", scope); err != nil {
			return err
		}
		if source := nestedMap(raw, "from"); source != nil {
			if name, _ := source["var"].(string); name != "" && !strings.Contains(name, "{{") {
				if err := scope.validate([]string{"vars", name}, validator.expressionRoots); err != nil {
					return fmt.Errorf("from.var: %w", err)
				}
			}
		}
		return validator.validateRawExpression("expr", raw, "expr", withEditLocals(scope))
	case "shell":
		if err := validator.validateRawExpression("argv expr", nestedMap(raw, "argv"), "expr", scope); err != nil {
			return err
		}
		return validator.validateRawExpression("interactions expr", nestedMap(raw, "interactions"), "expr", scope)
	case "process":
		return validator.validateRawExpression("argv expr", nestedMap(raw, "argv"), "expr", scope)
	case "process_call":
		if err := validator.validateRawExpression("pool", raw, "pool", scope); err != nil {
			return err
		}
		return validator.validateRawExpression("payload_expr", raw, "payload_expr", scope)
	case "lua":
		return validator.validateTypedBindings("argument", nestedMap(raw, "args"), scope)
	case "tui_choice":
		if err := validator.validateLookup("from", stringValue(raw, "from"), scope); err != nil {
			return err
		}
		locals := scope.clone()
		for _, field := range []string{"item", "label", "value", "description", "disabled", "reason"} {
			locals.roots[field] = openReference
		}
		for _, field := range []string{"label_expr", "value_expr", "description_expr", "disabled_expr", "reason_expr", "default_expr"} {
			if err := validator.validateRawExpression(field, raw, field, locals); err != nil {
				return err
			}
		}
	case "extract", "decode", "jsonpath", "table":
		return validator.validateLookup("from", stringValue(raw, "from"), scope)
	}
	return nil
}

func withEditLocals(scope *referenceScope) *referenceScope {
	result := scope.clone()
	result.roots["current"] = openReference
	result.roots["path"] = leafReference
	result.roots["index"] = leafReference
	return result
}

func (validator *referenceValidator) validateTypedBindings(kind string, bindings map[string]any, scope *referenceScope) error {
	for _, name := range slices.Sorted(maps.Keys(bindings)) {
		mapping, ok := bindings[name].(map[string]any)
		if !ok || len(mapping) != 1 {
			continue
		}
		source, ok := mapping["expr"].(string)
		if !ok {
			continue
		}
		if err := validator.validateExpression(kind+" "+fmt.Sprintf("%q", name)+" expr", source, scope); err != nil {
			return err
		}
	}
	return nil
}

func (validator *referenceValidator) validateRawExpression(label string, raw map[string]any, field string, scope *referenceScope) error {
	if raw == nil {
		return nil
	}
	source, _ := raw[field].(string)
	return validator.validateExpression(label, source, scope)
}

func (validator *referenceValidator) validateExpression(label, source string, scope *referenceScope) error {
	if strings.TrimSpace(source) == "" || strings.Contains(source, "{{") {
		return nil
	}
	tree, err := parser.Parse(source)
	if err != nil {
		return nil // Existing expression compilation owns syntax diagnostics.
	}
	metadata := expressionReferenceMetadata{locals: make(map[string]struct{}), callees: make(map[*ast.IdentifierNode]struct{})}
	ast.Walk(&tree.Node, &metadata)
	visitor := expressionReferenceVisitor{scope: scope, roots: validator.expressionRoots, locals: metadata.locals, callees: metadata.callees}
	ast.Walk(&tree.Node, &visitor)
	if visitor.err != nil {
		return fmt.Errorf("%s: %w", label, visitor.err)
	}
	return nil
}

type expressionReferenceVisitor struct {
	scope   *referenceScope
	roots   map[string]struct{}
	locals  map[string]struct{}
	callees map[*ast.IdentifierNode]struct{}
	err     error
}

func (visitor *expressionReferenceVisitor) Visit(node *ast.Node) {
	if visitor.err != nil {
		return
	}
	if path := expressionStaticPath(*node); len(path) > 0 {
		if identifier, ok := (*node).(*ast.IdentifierNode); ok {
			if _, callee := visitor.callees[identifier]; callee {
				return
			}
		}
		if _, local := visitor.locals[path[0]]; local {
			return
		}
		if _, exists := visitor.scope.roots[path[0]]; !exists {
			if _, known := visitor.roots[path[0]]; !known {
				visitor.err = fmt.Errorf("data root %q is not available here", path[0])
				return
			}
		}
		visitor.err = visitor.scope.validate(path, visitor.roots)
		if visitor.err != nil {
			return
		}
	}
	switch typed := (*node).(type) {
	case *ast.CallNode:
		identifier, ok := typed.Callee.(*ast.IdentifierNode)
		if ok && (identifier.Value == "get" || identifier.Value == "hasKey") {
			visitor.validateConstantKeyCall(identifier.Value, typed.Arguments)
		}
	case *ast.BuiltinNode:
		if typed.Name == "get" || typed.Name == "hasKey" {
			visitor.validateConstantKeyCall(typed.Name, typed.Arguments)
		}
	}
}

type expressionReferenceMetadata struct {
	locals  map[string]struct{}
	callees map[*ast.IdentifierNode]struct{}
}

func (metadata *expressionReferenceMetadata) Visit(node *ast.Node) {
	switch typed := (*node).(type) {
	case *ast.VariableDeclaratorNode:
		metadata.locals[typed.Name] = struct{}{}
	case *ast.CallNode:
		if identifier, ok := typed.Callee.(*ast.IdentifierNode); ok {
			metadata.callees[identifier] = struct{}{}
		}
	}
}

func (visitor *expressionReferenceVisitor) validateConstantKeyCall(name string, arguments []ast.Node) {
	if len(arguments) != 2 {
		return
	}
	path := expressionStaticPath(arguments[0])
	if len(path) == 0 {
		return
	}
	if name == "hasKey" {
		// A presence test must not require the key it asks about.
		visitor.err = visitor.scope.validate(path, visitor.roots)
		return
	}
	key, ok := arguments[1].(*ast.StringNode)
	if !ok {
		return
	}
	visitor.err = visitor.scope.validate(append(path, key.Value), visitor.roots)
}

func expressionStaticPath(node ast.Node) []string {
	switch typed := node.(type) {
	case *ast.IdentifierNode:
		return []string{typed.Value}
	case *ast.MemberNode:
		path := expressionStaticPath(typed.Node)
		if len(path) == 0 {
			return nil
		}
		property, ok := typed.Property.(*ast.StringNode)
		if !ok {
			return path
		}
		return append(path, property.Value)
	case *ast.ChainNode:
		return expressionStaticPath(typed.Node)
	}
	return nil
}

func (validator *referenceValidator) validateTemplate(label, value string, scope *referenceScope) error {
	if value == "" || !strings.Contains(value, "{{") {
		return nil
	}
	err := validator.renderer.WalkDataReferences(value, func(path []string) error {
		return scope.validate(path, validator.templateRoots)
	})
	if err != nil {
		return fmt.Errorf("%s template: %w", label, err)
	}
	return nil
}

func (validator *referenceValidator) validateTemplateValue(label string, value any, scope *referenceScope, skipSource bool) error {
	switch typed := value.(type) {
	case string:
		return validator.validateTemplate(label, typed, scope)
	case []any:
		for index, item := range typed {
			if err := validator.validateTemplateValue(fmt.Sprintf("%s item %d", label, index), item, scope, false); err != nil {
				return err
			}
		}
	case map[string]any:
		if _, expression := typed["expr"].(string); expression && len(typed) == 1 {
			return nil
		}
		for _, key := range slices.Sorted(maps.Keys(typed)) {
			if skipSource && key == "source" {
				continue
			}
			if err := validator.validateTemplateValue(label+" field "+key, typed[key], scope, false); err != nil {
				return err
			}
		}
	}
	return nil
}

func (validator *referenceValidator) validateLookup(label, value string, scope *referenceScope) error {
	if value == "" {
		return nil
	}
	if strings.Contains(value, "{{") {
		return nil // The rendered lookup is only known at run time.
	}
	path := strings.Split(value, ".")
	if len(path) < 2 || (path[0] != "vars" && path[0] != "steps") {
		return nil // The owning runner reports malformed lookup syntax.
	}
	if err := scope.validate(path, validator.expressionRoots); err != nil {
		return fmt.Errorf("%s: %w", label, err)
	}
	return nil
}

func nestedMap(values map[string]any, name string) map[string]any {
	result, _ := values[name].(map[string]any)
	return result
}

func stringValue(values map[string]any, name string) string {
	result, _ := values[name].(string)
	return result
}
