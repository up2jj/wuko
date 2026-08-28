// Package keyvalue implements JSON-backed persistent workflow values.
package keyvalue

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/expr-lang/expr"
	"github.com/expr-lang/expr/vm"
	wukoexpr "github.com/up2jj/wuko/expression"
	storepkg "github.com/up2jj/wuko/keyvalue"
	"github.com/up2jj/wuko/step"
)

const (
	operationGet    = "get"
	operationSet    = "set"
	operationDelete = "delete"
	operationList   = "list"
	operationUpdate = "update"
	operationClear  = "clear"
)

// alwaysAllowed are the fields every operation accepts; operationFields adds the optional
// inputs each one understands. A field outside both sets is rejected for that operation
// rather than silently ignored.
var (
	alwaysAllowed   = fields("operation", "scope", "store", "variable")
	operationFields = map[string]map[string]struct{}{
		operationGet:    fields("key", "default"),
		operationSet:    fields("key", "value", "expr"),
		operationUpdate: fields("key", "expr"),
		operationDelete: fields("key"),
		operationList:   fields("prefix"),
		operationClear:  fields(),
	}
)

func fields(names ...string) map[string]struct{} {
	set := make(map[string]struct{}, len(names))
	for _, name := range names {
		set[name] = struct{}{}
	}
	return set
}

// Config describes one persistent key-value operation.
type Config struct {
	Operation string `yaml:"operation"`
	Scope     string `yaml:"scope"`
	Store     string `yaml:"store"`
	Key       string `yaml:"key,omitempty"`
	Value     any    `yaml:"value,omitempty"`
	Expr      string `yaml:"expr,omitempty"`
	Variable  string `yaml:"variable,omitempty"`
	Default   any    `yaml:"default,omitempty"`
	Prefix    string `yaml:"prefix,omitempty"`
}

type expressionEnvironment struct {
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
	Workflow     workflowValue             `expr:"workflow"`
	Run          runValue                  `expr:"run"`
	// Current and Found describe the stored value an update replaces. They are nil and
	// false for every other operation.
	Current any  `expr:"current"`
	Found   bool `expr:"found"`
}

type workflowValue struct {
	Name string `expr:"name"`
	Dir  string `expr:"dir"`
}

type runValue struct {
	Dir string `expr:"dir"`
}

// Runner executes a key-value operation.
type Runner struct {
	config   Config
	present  map[string]struct{}
	hasValue bool
	hasExpr  bool
	program  *vm.Program
}

// Register adds the key_value step to a registry.
func Register(registry *step.Registry) error { return registry.Register("key_value", New) }

// New decodes and validates a key_value step configuration.
func New(raw map[string]any) (step.Runner, error) {
	var config Config
	if err := step.DecodeConfig(raw, &config); err != nil {
		return nil, err
	}
	present := make(map[string]struct{}, len(raw))
	for field := range raw {
		present[field] = struct{}{}
	}
	_, hasValue := present["value"]
	_, hasExpr := present["expr"]
	runner := &Runner{config: config, present: present, hasValue: hasValue, hasExpr: hasExpr}
	if err := runner.validateConfig(); err != nil {
		return nil, err
	}
	if err := runner.compileValueExpression(); err != nil {
		return nil, err
	}
	return runner, nil
}

// compileValueExpression prepares expr. A configuration that still holds templates is
// compiled by the build that follows rendering, which is the one that runs.
func (r *Runner) compileValueExpression() error {
	if !r.hasExpr || templated(r.config.Expr) {
		return nil
	}
	program, err := wukoexpr.Compile(r.config.Expr, expr.Env(expressionEnvironment{}))
	if err != nil {
		return fmt.Errorf("compiling expr: %w", err)
	}
	r.program = program
	return nil
}

// Validate checks the selected storage root without touching the filesystem.
func (r *Runner) Validate(_ context.Context, request step.Request) error {
	if templated(r.config.Scope) {
		return nil
	}
	name := r.config.Store
	if templated(name) {
		name = "rendered-store"
	}
	_, err := storepkg.OpenWorkflowScoped(request.LocalValueDir, request.GlobalValueDir, r.config.Scope, name)
	return err
}

// Run performs the configured operation.
func (r *Runner) Run(ctx context.Context, request step.Request) (step.Result, error) {
	if err := r.validateResolvedConfig(); err != nil {
		return step.Result{}, err
	}
	store, err := storepkg.OpenWorkflowScoped(request.LocalValueDir, request.GlobalValueDir, r.config.Scope, r.config.Store)
	if err != nil {
		return step.Result{}, err
	}
	result, err := r.perform(ctx, store, request)
	if err != nil {
		return result, err
	}
	return r.withVariable(result), nil
}

// withVariable copies the operation's result into the configured workflow variable: the
// value it read or wrote, or the entries a list returned.
func (r *Runner) withVariable(result step.Result) step.Result {
	if r.config.Variable == "" {
		return result
	}
	value, ok := result.Outputs["value"]
	if !ok {
		value, ok = result.Outputs["entries"]
	}
	if !ok {
		value = result.Outputs["cleared"]
	}
	result.Variables = map[string]any{r.config.Variable: value}
	return result
}

func (r *Runner) perform(ctx context.Context, store *storepkg.Store, request step.Request) (step.Result, error) {
	switch r.config.Operation {
	case operationGet:
		value, found, err := store.Get(ctx, r.config.Key)
		if err == nil && !found {
			value = r.config.Default
		}
		return step.Result{Outputs: map[string]any{"value": runtimeValue(value), "found": found}}, err
	case operationSet:
		value, err := r.storedValue(request)
		if err != nil {
			return step.Result{}, err
		}
		value, err = store.Set(ctx, r.config.Key, value)
		return step.Result{Outputs: map[string]any{"value": runtimeValue(value)}}, err
	case operationUpdate:
		existed := false
		value, changed, err := store.Update(ctx, r.config.Key, func(current any, found bool) (any, error) {
			existed = found
			return r.updatedValue(request, current, found)
		})
		if err != nil {
			return step.Result{}, err
		}
		return step.Result{Outputs: map[string]any{"value": runtimeValue(value), "found": existed, "changed": changed}}, nil
	case operationDelete:
		value, deleted, err := store.Delete(ctx, r.config.Key)
		return step.Result{Outputs: map[string]any{"value": runtimeValue(value), "deleted": deleted}}, err
	case operationList:
		entries, err := store.List(ctx)
		outputs := make([]any, 0, len(entries))
		for _, entry := range entries {
			if !strings.HasPrefix(entry.Key, r.config.Prefix) {
				continue
			}
			outputs = append(outputs, map[string]any{"key": entry.Key, "value": runtimeValue(entry.Value)})
		}
		return step.Result{Outputs: map[string]any{"entries": outputs}}, err
	case operationClear:
		cleared, err := store.Clear(ctx)
		return step.Result{Outputs: map[string]any{"cleared": int64(cleared)}}, err
	default:
		panic("validated key-value operation")
	}
}

func (r *Runner) validateConfig() error {
	if r.config.Scope == "" {
		return fmt.Errorf("scope is required")
	}
	if r.config.Store == "" {
		return fmt.Errorf("store is required")
	}
	if _, declared := r.present["variable"]; declared && strings.TrimSpace(r.config.Variable) == "" {
		return fmt.Errorf("variable must not be empty")
	}
	// OpenWorkflowScoped validates resolved store names and scopes without accessing the filesystem.
	if !templated(r.config.Store) {
		if _, err := storepkg.OpenWorkflowScoped("root", "root", storepkg.Local, r.config.Store); err != nil {
			return err
		}
	}
	if !templated(r.config.Scope) {
		if _, err := storepkg.OpenWorkflowScoped("local", "global", r.config.Scope, "store"); err != nil {
			return err
		}
	}
	if templated(r.config.Operation) {
		return nil
	}
	return r.validateOperation()
}

func (r *Runner) validateResolvedConfig() error {
	if templated(r.config.Operation) || templated(r.config.Scope) || templated(r.config.Store) {
		return fmt.Errorf("key-value configuration contains an unresolved template")
	}
	if _, err := storepkg.OpenWorkflowScoped("local", "global", r.config.Scope, r.config.Store); err != nil {
		return err
	}
	return r.validateOperation()
}

func (r *Runner) validateOperation() error {
	if r.config.Operation == "" {
		return fmt.Errorf("operation is required")
	}
	allowed, known := operationFields[r.config.Operation]
	if !known {
		return fmt.Errorf("operation must be get, set, update, delete, list, or clear")
	}
	for _, field := range slices.Sorted(maps.Keys(r.present)) {
		if _, common := alwaysAllowed[field]; common {
			continue
		}
		if _, ok := allowed[field]; !ok {
			return fmt.Errorf("%s is not allowed for %s", field, r.config.Operation)
		}
	}
	if _, needsKey := allowed["key"]; needsKey && r.config.Key == "" {
		return fmt.Errorf("key is required for %s", r.config.Operation)
	}
	switch r.config.Operation {
	case operationSet:
		if r.hasValue == r.hasExpr {
			return fmt.Errorf("exactly one of value or expr is required for set")
		}
	case operationUpdate:
		if !r.hasExpr {
			return fmt.Errorf("expr is required for update")
		}
	}
	if r.hasExpr && strings.TrimSpace(r.config.Expr) == "" {
		return fmt.Errorf("expr must not be empty")
	}
	return r.normalizeLiterals()
}

// normalizeLiterals converts the literal inputs to the shapes the store round-trips, which
// also rejects anything that is not JSON-compatible.
func (r *Runner) normalizeLiterals() error {
	for name, literal := range map[string]*any{"value": &r.config.Value, "default": &r.config.Default} {
		if _, declared := r.present[name]; !declared {
			continue
		}
		normalized, err := storepkg.Normalize(*literal)
		if err != nil {
			return fmt.Errorf("%s is not JSON-compatible: %w", name, err)
		}
		*literal = normalized
	}
	return nil
}

// storedValue resolves what set writes. expr keeps the JSON type of its result, which a
// templated value cannot: rendering turns every value into a string.
func (r *Runner) storedValue(request step.Request) (any, error) {
	if r.hasValue {
		return r.config.Value, nil
	}
	if r.program == nil {
		return nil, fmt.Errorf("expr contains an unresolved template")
	}
	value, err := expr.Run(r.program, environment(request))
	if err != nil {
		return nil, fmt.Errorf("evaluating expr: %w", err)
	}
	normalized, err := storepkg.Normalize(value)
	if err != nil {
		return nil, fmt.Errorf("expr result is not JSON-compatible: %w", err)
	}
	return normalized, nil
}

// updatedValue evaluates expr against the stored value an update replaces. It runs while
// the store lock is held, so the value it reads is the value it writes back.
func (r *Runner) updatedValue(request step.Request, current any, found bool) (any, error) {
	if r.program == nil {
		return nil, fmt.Errorf("expr contains an unresolved template")
	}
	value := environment(request)
	value.Current = runtimeValue(current)
	value.Found = found
	updated, err := expr.Run(r.program, value)
	if err != nil {
		return nil, fmt.Errorf("evaluating expr: %w", err)
	}
	normalized, err := storepkg.Normalize(updated)
	if err != nil {
		return nil, fmt.Errorf("expr result is not JSON-compatible: %w", err)
	}
	return normalized, nil
}

func environment(request step.Request) expressionEnvironment {
	return expressionEnvironment{
		Inputs: request.Inputs, Vars: request.Vars, Env: request.Env, Steps: request.Steps,
		Dependencies: request.Dependencies, Batch: binding(request.Bindings, "batch"),
		Foreach: binding(request.Bindings, "foreach"), Matrix: binding(request.Bindings, "matrix"),
		Finally: binding(request.Bindings, "finally"), Error: binding(request.Bindings, "error"),
		Workflow: workflowValue{Name: request.WorkflowName, Dir: request.WorkflowDir},
		Run:      runValue{Dir: request.RunDir},
	}
}

func binding(bindings map[string]any, name string) map[string]any {
	value, _ := bindings[name].(map[string]any)
	if value == nil {
		return map[string]any{}
	}
	return value
}

func templated(value string) bool { return strings.Contains(value, "{{") }

func runtimeValue(value any) any {
	switch typed := value.(type) {
	case json.Number:
		if integer, err := typed.Int64(); err == nil {
			return integer
		}
		if unsigned, err := strconv.ParseUint(string(typed), 10, 64); err == nil {
			return unsigned
		}
		number, err := typed.Float64()
		if err != nil {
			return typed
		}
		return number
	case []any:
		result := make([]any, len(typed))
		for i, item := range typed {
			result[i] = runtimeValue(item)
		}
		return result
	case map[string]any:
		result := make(map[string]any, len(typed))
		for key, item := range typed {
			result[key] = runtimeValue(item)
		}
		return result
	default:
		return value
	}
}
