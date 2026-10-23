package lua

import (
	"context"
	"fmt"
	"maps"
	"strings"

	"github.com/up2jj/wuko/step"
	glua "github.com/yuin/gopher-lua"
	"github.com/yuin/gopher-lua/parse"
)

// Predicate is a compiled Lua expression that resolves to a function returning a boolean.
// The immutable prototype can be reused; each evaluation owns a fresh Lua state.
type Predicate struct {
	prototype *glua.FunctionProto
}

// CompilePredicate checks syntax without executing code or loading required modules.
// Source may be an anonymous function or an expression such as require("rules").name.
func CompilePredicate(source string) (*Predicate, error) {
	chunk, err := parse.Parse(strings.NewReader("return (\n"+source+"\n)"), "validation.lua")
	if err != nil {
		return nil, fmt.Errorf("compiling Lua validator: %w", err)
	}
	prototype, err := glua.Compile(chunk, "validation.lua")
	if err != nil {
		return nil, fmt.Errorf("compiling Lua validator: %w", err)
	}
	return &Predicate{prototype: prototype}, nil
}

// Evaluate calls the function with value and a copy of the workflow's data roots.
// It shares module lookup with Lua steps, but exposes no workflow mutation API.
func (predicate *Predicate) Evaluate(ctx context.Context, request step.Request, value any) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	state := newState(moduleRoot(request))
	defer state.Close()
	state.SetContext(ctx)
	if err := state.CallByParam(glua.P{Fn: state.NewFunctionFromProto(predicate.prototype), NRet: 1, Protect: true}); err != nil {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		return false, fmt.Errorf("loading Lua validator: %w", err)
	}
	function, ok := state.Get(-1).(*glua.LFunction)
	state.Pop(1)
	if !ok {
		return false, fmt.Errorf("Lua validator must resolve to a function")
	}
	argument, err := toLua(state, value)
	if err != nil {
		return false, fmt.Errorf("converting validator value: %w", err)
	}
	run := request.RunValue()
	roots := maps.Clone(request.Bindings)
	if roots == nil {
		roots = map[string]any{}
	}
	for key, value := range request.Providers.Values {
		roots[key] = value
	}
	for key, value := range map[string]any{
		"vars": request.Vars, "inputs": request.Inputs, "steps": request.Steps, "env": request.Env, "dependencies": request.Dependencies,
		"workflow": map[string]any{"name": request.WorkflowName, "dir": request.WorkflowDir, "timezone": request.WorkflowTimezone},
		"run":      map[string]any{"dir": run.Dir, "environment_loaders": run.EnvironmentLoaders, "os": run.OS, "arch": run.Arch},
	} {
		roots[key] = value
	}
	// The form-local draft shadows a provider with the same name, just as local
	// Expr roots do. It must never resolve to an unrelated workflow/provider value.
	if fields, exists := request.Bindings["fields"]; exists {
		roots["fields"] = fields
	}
	data, err := toLua(state, roots)
	if err != nil {
		return false, fmt.Errorf("converting validator context: %w", err)
	}
	if err := state.CallByParam(glua.P{Fn: function, NRet: 1, Protect: true}, argument, data); err != nil {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		return false, fmt.Errorf("running Lua validator: %w", err)
	}
	result, ok := state.Get(-1).(glua.LBool)
	if !ok {
		return false, fmt.Errorf("Lua validator must return a boolean")
	}
	return bool(result), nil
}
