package lua

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/expr-lang/expr"
	"github.com/expr-lang/expr/vm"
	wukoexpr "github.com/up2jj/wuko/expression"
	storepkg "github.com/up2jj/wuko/keyvalue"
	"github.com/up2jj/wuko/process"
	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/workflow"
	glua "github.com/yuin/gopher-lua"
)

type Config struct {
	File   string               `yaml:"file,omitempty"`
	Source string               `yaml:"source,omitempty"`
	Args   map[string]any       `yaml:"args,omitempty"`
	Env    workflow.Environment `yaml:"env,omitempty"`
}

type Runner struct {
	config      Config
	argPrograms map[string]*vm.Program
	doHTTP      func(*http.Request, time.Duration) (*http.Response, error)
}

type expressionEnvironment struct {
	Inputs       map[string]any               `expr:"inputs"`
	Vars         map[string]any               `expr:"vars"`
	Env          map[string]string            `expr:"env"`
	Steps        map[string]any               `expr:"steps"`
	Dependencies map[string]map[string]any    `expr:"dependencies"`
	Batch        map[string]any               `expr:"batch"`
	Foreach      map[string]any               `expr:"foreach"`
	Matrix       map[string]any               `expr:"matrix"`
	Observe      map[string]any               `expr:"observe"`
	Finally      map[string]any               `expr:"finally"`
	Error        map[string]any               `expr:"error"`
	Workflow     step.WorkflowValue           `expr:"workflow"`
	Run          step.RunValue                `expr:"run"`
	Secret       func(string) (string, error) `expr:"secret"`
}

type runtime struct {
	request   step.Request
	args      map[string]any
	outputs   map[string]any
	variables map[string]any
	doHTTP    func(*http.Request, time.Duration) (*http.Response, error)
}

func Register(registry *step.Registry) error { return registry.Register("lua", New) }

func New(raw map[string]any) (step.Runner, error) {
	var config Config
	if err := step.DecodeConfig(raw, &config); err != nil {
		return nil, err
	}
	if (config.File == "") == (config.Source == "") {
		return nil, fmt.Errorf("exactly one of file or source is required")
	}
	for key := range config.Env {
		if !workflow.ValidEnvironmentName(key) {
			return nil, fmt.Errorf("invalid environment name %q", key)
		}
	}
	argPrograms, err := compileArgExpressions(config.Args)
	if err != nil {
		return nil, err
	}
	return &Runner{config: config, argPrograms: argPrograms, doHTTP: func(request *http.Request, timeout time.Duration) (*http.Response, error) {
		return (&http.Client{Timeout: timeout}).Do(request)
	}}, nil
}

func compileArgExpressions(args map[string]any) (map[string]*vm.Program, error) {
	programs := make(map[string]*vm.Program)
	for name, value := range args {
		binding, ok := value.(map[string]any)
		if !ok || len(binding) != 1 {
			continue
		}
		source, exists := binding["expr"]
		if !exists {
			continue
		}
		text, ok := source.(string)
		if !ok || strings.TrimSpace(text) == "" {
			return nil, fmt.Errorf("argument %q expr must be a non-empty string", name)
		}
		program, err := wukoexpr.Compile(text, expr.Env(expressionEnvironment{}))
		if err != nil {
			return nil, fmt.Errorf("compiling argument %q expr: %w", name, err)
		}
		programs[name] = program
	}
	return programs, nil
}

func (r *Runner) Validate(_ context.Context, request step.Request) error {
	source, name, err := r.source(request)
	if err != nil {
		return err
	}
	state := newState()
	defer state.Close()
	if _, err := state.Load(bytes.NewReader([]byte(source)), name); err != nil {
		return fmt.Errorf("compiling Lua %s: %w", name, err)
	}
	return nil
}

func (r *Runner) Run(ctx context.Context, request step.Request) (step.Result, error) {
	source, name, err := r.source(request)
	if err != nil {
		return step.Result{}, err
	}
	request.Env = maps.Clone(request.Env)
	maps.Copy(request.Env, r.config.Env)
	request.Env = step.ApplyAttemptEnvironment(request.Env, request)
	args, err := r.resolveArgs(ctx, request)
	if err != nil {
		return step.Result{}, err
	}
	runtime := &runtime{request: request, args: args, outputs: make(map[string]any), variables: make(map[string]any), doHTTP: r.doHTTP}
	state := newState()
	defer state.Close()
	state.SetContext(ctx)
	wuko, err := runtime.module(state)
	if err != nil {
		return step.Result{}, err
	}
	state.SetGlobal("wuko", wuko)
	function, err := state.Load(bytes.NewReader([]byte(source)), name)
	if err != nil {
		return step.Result{}, fmt.Errorf("compiling Lua %s: %w", name, err)
	}
	state.Push(function)
	if err := state.PCall(0, glua.MultRet, nil); err != nil {
		return step.Result{}, fmt.Errorf("running Lua %s: %w", name, err)
	}
	return step.Result{Outputs: runtime.outputs, Variables: runtime.variables}, nil
}

func (r *Runner) resolveArgs(ctx context.Context, request step.Request) (map[string]any, error) {
	args := maps.Clone(r.config.Args)
	for name, program := range r.argPrograms {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		value, err := expr.Run(program, expressionEnvironment{
			Inputs:       request.Inputs,
			Vars:         request.Vars,
			Env:          request.Env,
			Steps:        request.Steps,
			Dependencies: request.Dependencies,
			Batch:        bindingRoot(request.Bindings, "batch"),
			Foreach:      bindingRoot(request.Bindings, "foreach"),
			Matrix:       bindingRoot(request.Bindings, "matrix"),
			Observe:      bindingRoot(request.Bindings, "observe"),
			Finally:      bindingRoot(request.Bindings, "finally"),
			Error:        bindingRoot(request.Bindings, "error"),
			Workflow:     request.WorkflowValue(),
			Run:          request.RunValue(),
			Secret:       request.ResolveSecret,
		})
		if err != nil {
			return nil, fmt.Errorf("evaluating argument %q expr: %w", name, err)
		}
		args[name] = value
	}
	return args, nil
}

func bindingRoot(bindings map[string]any, name string) map[string]any {
	value, _ := bindings[name].(map[string]any)
	if value == nil {
		return map[string]any{}
	}
	return value
}

func (r *Runner) source(request step.Request) (string, string, error) {
	if r.config.Source != "" {
		return r.config.Source, fmt.Sprintf("%s:%s:inline.lua", request.WorkflowName, request.StepID), nil
	}
	path := r.config.File
	if !filepath.IsAbs(path) {
		path = filepath.Join(request.WorkflowDir, path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", "", fmt.Errorf("reading Lua file %s: %w", path, err)
	}
	return string(data), path, nil
}

func newState() *glua.LState {
	state := glua.NewState(glua.Options{SkipOpenLibs: true})
	glua.OpenBase(state)
	glua.OpenTable(state)
	glua.OpenString(state)
	glua.OpenMath(state)
	glua.OpenPackage(state)
	return state
}

func (r *runtime) module(state *glua.LState) (*glua.LTable, error) {
	module := state.NewTable()
	args, err := toLua(state, r.args)
	if err != nil {
		return nil, fmt.Errorf("converting Lua arguments: %w", err)
	}
	module.RawSetString("args", args)
	for name, value := range map[string]any{
		"inputs":       r.request.Inputs,
		"steps":        r.request.Steps,
		"dependencies": r.request.Dependencies,
		"workflow":     map[string]any{"name": r.request.WorkflowName, "dir": r.request.WorkflowDir, "timezone": r.request.WorkflowTimezone},
		"run":          map[string]any{"dir": r.request.RunDir, "environment_loaders": slices.Clone(r.request.EnvironmentLoaders)},
	} {
		converted, err := toLua(state, value)
		if err != nil {
			return nil, fmt.Errorf("converting %s root: %w", name, err)
		}
		module.RawSetString(name, converted)
	}
	for _, name := range []string{"batch", "foreach", "matrix", "observe", "finally", "error"} {
		binding, exists := r.request.Bindings[name]
		if !exists {
			continue
		}
		value, err := toLua(state, binding)
		if err != nil {
			return nil, fmt.Errorf("converting %s binding: %w", name, err)
		}
		module.RawSetString(name, value)
	}
	state.SetFuncs(module, map[string]glua.LGFunction{
		"var": r.varValue, "set_var": r.setVar, "output": r.output,
	})

	env := state.NewTable()
	state.SetFuncs(env, map[string]glua.LGFunction{"get": r.envGet, "all": r.envAll})
	module.RawSetString("env", env)
	jsonModule := state.NewTable()
	state.SetFuncs(jsonModule, map[string]glua.LGFunction{"encode": r.jsonEncode, "decode": r.jsonDecode})
	module.RawSetString("json", jsonModule)
	helpers := state.NewTable()
	state.SetFuncs(helpers, helperFunctions())
	module.RawSetString("helpers", helpers)
	kvModule := state.NewTable()
	state.SetFuncs(kvModule, map[string]glua.LGFunction{
		"get": r.kvGet, "set": r.kvSet, "delete": r.kvDelete, "list": r.kvList,
	})
	module.RawSetString("kv", kvModule)
	httpModule := state.NewTable()
	state.SetFuncs(httpModule, map[string]glua.LGFunction{"request": r.httpRequest})
	module.RawSetString("http", httpModule)
	fs := state.NewTable()
	state.SetFuncs(fs, map[string]glua.LGFunction{
		"read": r.fsRead, "write": r.fsWrite, "mkdir_all": r.fsMkdirAll,
		"list": r.fsList, "stat": r.fsStat, "rename": r.fsRename, "remove": r.fsRemove,
	})
	module.RawSetString("fs", fs)
	execModule := state.NewTable()
	state.SetFuncs(execModule, map[string]glua.LGFunction{"run": r.execRun})
	module.RawSetString("exec", execModule)
	return module, nil
}

func (r *runtime) varValue(state *glua.LState) int {
	name := state.CheckString(1)
	value, ok := r.variables[name]
	if !ok {
		value, ok = r.request.Vars[name]
	}
	if !ok {
		state.Push(glua.LNil)
		return 1
	}
	luaValue, err := toLua(state, value)
	if err != nil {
		state.RaiseError("reading variable %s: %v", name, err)
		return 0
	}
	state.Push(luaValue)
	return 1
}

func (r *runtime) setVar(state *glua.LState) int {
	name := state.CheckString(1)
	value, err := fromLua(state.Get(2), make(map[*glua.LTable]bool))
	if err != nil {
		state.RaiseError("setting variable %s: %v", name, err)
		return 0
	}
	r.variables[name] = value
	return 0
}

func (r *runtime) output(state *glua.LState) int {
	name := state.CheckString(1)
	value, err := fromLua(state.Get(2), make(map[*glua.LTable]bool))
	if err != nil {
		state.RaiseError("setting output %s: %v", name, err)
		return 0
	}
	r.outputs[name] = value
	return 0
}

func (r *runtime) envGet(state *glua.LState) int {
	value, ok := r.request.Env[state.CheckString(1)]
	if !ok {
		state.Push(glua.LNil)
	} else {
		state.Push(glua.LString(value))
	}
	return 1
}

func (r *runtime) envAll(state *glua.LState) int {
	table := state.NewTable()
	keys := make([]string, 0, len(r.request.Env))
	for key := range r.request.Env {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		table.RawSetString(key, glua.LString(r.request.Env[key]))
	}
	state.Push(table)
	return 1
}

func (r *runtime) jsonEncode(state *glua.LState) int {
	value, err := fromLua(state.Get(1), make(map[*glua.LTable]bool))
	if err != nil {
		state.RaiseError("encoding JSON: %v", err)
		return 0
	}
	data, err := json.Marshal(value)
	if err != nil {
		state.RaiseError("encoding JSON: %v", err)
		return 0
	}
	state.Push(glua.LString(data))
	return 1
}

func (r *runtime) jsonDecode(state *glua.LState) int {
	decoder := json.NewDecoder(strings.NewReader(state.CheckString(1)))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		state.RaiseError("decoding JSON: %v", err)
		return 0
	}
	luaValue, err := toLua(state, value)
	if err != nil {
		state.RaiseError("decoding JSON: %v", err)
		return 0
	}
	state.Push(luaValue)
	return 1
}

func (r *runtime) kvGet(state *glua.LState) int {
	store, options, ok := r.kvStore(state, "get", true)
	if !ok {
		return 0
	}
	value, found, err := store.Get(state.Context(), options.key)
	if err != nil {
		state.RaiseError("getting key-value entry: %v", err)
		return 0
	}
	luaValue, err := toLua(state, value)
	if err != nil {
		state.RaiseError("converting key-value entry: %v", err)
		return 0
	}
	state.Push(luaValue)
	state.Push(glua.LBool(found))
	return 2
}

func (r *runtime) kvSet(state *glua.LState) int {
	store, options, ok := r.kvStore(state, "set", true)
	if !ok {
		return 0
	}
	value, err := fromLua(options.table.RawGetString("value"), make(map[*glua.LTable]bool))
	if err != nil {
		state.RaiseError("setting key-value entry: %v", err)
		return 0
	}
	value, err = store.Set(state.Context(), options.key, value)
	if err != nil {
		state.RaiseError("setting key-value entry: %v", err)
		return 0
	}
	luaValue, err := toLua(state, value)
	if err != nil {
		state.RaiseError("converting key-value entry: %v", err)
		return 0
	}
	state.Push(luaValue)
	return 1
}

func (r *runtime) kvDelete(state *glua.LState) int {
	store, options, ok := r.kvStore(state, "delete", true)
	if !ok {
		return 0
	}
	value, deleted, err := store.Delete(state.Context(), options.key)
	if err != nil {
		state.RaiseError("deleting key-value entry: %v", err)
		return 0
	}
	luaValue, err := toLua(state, value)
	if err != nil {
		state.RaiseError("converting key-value entry: %v", err)
		return 0
	}
	state.Push(luaValue)
	state.Push(glua.LBool(deleted))
	return 2
}

func (r *runtime) kvList(state *glua.LState) int {
	store, _, ok := r.kvStore(state, "list", false)
	if !ok {
		return 0
	}
	entries, err := store.List(state.Context())
	if err != nil {
		state.RaiseError("listing key-value entries: %v", err)
		return 0
	}
	result := state.NewTable()
	for _, entry := range entries {
		item := state.NewTable()
		item.RawSetString("key", glua.LString(entry.Key))
		value, err := toLua(state, entry.Value)
		if err != nil {
			state.RaiseError("converting key-value entry: %v", err)
			return 0
		}
		item.RawSetString("value", value)
		result.Append(item)
	}
	state.Push(result)
	return 1
}

type kvOptions struct {
	table *glua.LTable
	key   string
}

func (r *runtime) kvStore(state *glua.LState, operation string, needsKey bool) (*storepkg.Store, kvOptions, bool) {
	table := state.CheckTable(1)
	allowed := map[string]bool{"scope": true, "store": true}
	if needsKey {
		allowed["key"] = true
	}
	if operation == "set" {
		allowed["value"] = true
	}
	var optionErr string
	table.ForEach(func(key, _ glua.LValue) {
		if optionErr != "" {
			return
		}
		if key.Type() != glua.LTString || !allowed[key.String()] {
			optionErr = fmt.Sprintf("unknown option %q", key.String())
		}
	})
	if optionErr != "" {
		state.RaiseError("key-value %s: %s", operation, optionErr)
		return nil, kvOptions{}, false
	}
	scope, ok := requiredTableString(state, table, "scope", "key-value "+operation)
	if !ok {
		return nil, kvOptions{}, false
	}
	name, ok := requiredTableString(state, table, "store", "key-value "+operation)
	if !ok {
		return nil, kvOptions{}, false
	}
	key := ""
	if needsKey {
		key, ok = requiredTableString(state, table, "key", "key-value "+operation)
		if !ok {
			return nil, kvOptions{}, false
		}
	}
	store, err := storepkg.OpenWorkflowScoped(r.request.LocalValueDir, r.request.GlobalValueDir, scope, name)
	if err != nil {
		state.RaiseError("key-value %s: %v", operation, err)
		return nil, kvOptions{}, false
	}
	return store, kvOptions{table: table, key: key}, true
}

func requiredTableString(state *glua.LState, table *glua.LTable, key, operation string) (string, bool) {
	value := table.RawGetString(key)
	text, ok := value.(glua.LString)
	if !ok || text == "" {
		state.RaiseError("%s: %s must be a non-empty string", operation, key)
		return "", false
	}
	return string(text), true
}

func (r *runtime) httpRequest(state *glua.LState) int {
	options := state.CheckTable(1)
	method := tableString(options, "method", http.MethodGet)
	url := tableString(options, "url", "")
	if url == "" {
		state.RaiseError("http.request url is required")
		return 0
	}
	body := tableString(options, "body", "")
	timeoutSeconds := tableNumber(options, "timeout", 30)
	request, err := http.NewRequestWithContext(state.Context(), method, url, strings.NewReader(body))
	if err != nil {
		state.RaiseError("creating HTTP request: %v", err)
		return 0
	}
	if headers, ok := options.RawGetString("headers").(*glua.LTable); ok {
		headers.ForEach(func(key, value glua.LValue) { request.Header.Set(key.String(), value.String()) })
	}
	response, err := r.doHTTP(request, time.Duration(timeoutSeconds*float64(time.Second)))
	if err != nil {
		state.RaiseError("performing HTTP request: %v", err)
		return 0
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		state.RaiseError("reading HTTP response: %v", err)
		return 0
	}
	result := state.NewTable()
	result.RawSetString("status", glua.LNumber(response.StatusCode))
	result.RawSetString("body", glua.LString(data))
	headers := state.NewTable()
	for key, values := range response.Header {
		headers.RawSetString(key, glua.LString(strings.Join(values, ", ")))
	}
	result.RawSetString("headers", headers)
	state.Push(result)
	return 1
}

func (r *runtime) fsRead(state *glua.LState) int {
	data, err := os.ReadFile(r.resolvePath(state.CheckString(1)))
	if err != nil {
		state.RaiseError("reading file: %v", err)
		return 0
	}
	state.Push(glua.LString(data))
	return 1
}

func (r *runtime) fsWrite(state *glua.LState) int {
	if err := os.WriteFile(r.resolvePath(state.CheckString(1)), []byte(state.CheckString(2)), 0o644); err != nil {
		state.RaiseError("writing file: %v", err)
	}
	return 0
}

func (r *runtime) fsMkdirAll(state *glua.LState) int {
	if err := os.MkdirAll(r.resolvePath(state.CheckString(1)), 0o755); err != nil {
		state.RaiseError("creating directory: %v", err)
	}
	return 0
}

func (r *runtime) fsList(state *glua.LState) int {
	entries, err := os.ReadDir(r.resolvePath(state.CheckString(1)))
	if err != nil {
		state.RaiseError("listing directory: %v", err)
		return 0
	}
	table := state.NewTable()
	for _, entry := range entries {
		item := state.NewTable()
		item.RawSetString("name", glua.LString(entry.Name()))
		item.RawSetString("is_dir", glua.LBool(entry.IsDir()))
		table.Append(item)
	}
	state.Push(table)
	return 1
}

func (r *runtime) fsStat(state *glua.LState) int {
	info, err := os.Stat(r.resolvePath(state.CheckString(1)))
	if err != nil {
		state.RaiseError("stating path: %v", err)
		return 0
	}
	table := state.NewTable()
	table.RawSetString("name", glua.LString(info.Name()))
	table.RawSetString("size", glua.LNumber(info.Size()))
	table.RawSetString("is_dir", glua.LBool(info.IsDir()))
	table.RawSetString("mode", glua.LString(info.Mode().String()))
	table.RawSetString("modified_unix", glua.LNumber(info.ModTime().Unix()))
	state.Push(table)
	return 1
}

func (r *runtime) fsRename(state *glua.LState) int {
	if err := os.Rename(r.resolvePath(state.CheckString(1)), r.resolvePath(state.CheckString(2))); err != nil {
		state.RaiseError("renaming path: %v", err)
	}
	return 0
}

func (r *runtime) fsRemove(state *glua.LState) int {
	if err := os.Remove(r.resolvePath(state.CheckString(1))); err != nil {
		state.RaiseError("removing path: %v", err)
	}
	return 0
}

func (r *runtime) execRun(state *glua.LState) int {
	options := state.CheckTable(1)
	command := tableString(options, "command", "")
	if command == "" {
		state.RaiseError("exec.run command is required")
		return 0
	}
	args, err := tableStrings(options.RawGetString("args"))
	if err != nil {
		state.RaiseError("exec.run args: %v", err)
		return 0
	}
	environment := maps.Clone(r.request.Env)
	if envTable, ok := options.RawGetString("env").(*glua.LTable); ok {
		envTable.ForEach(func(key, value glua.LValue) { environment[key.String()] = value.String() })
	}
	environment = step.ApplyAttemptEnvironment(environment, r.request)
	dir := tableString(options, "working_directory", r.request.RunDir)
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(r.request.RunDir, dir)
	}
	stdoutPolicy, err := process.ParseOutputPolicy(tableString(options, "stdout", ""))
	if err != nil {
		state.RaiseError("exec.run stdout %v", err)
		return 0
	}
	stderrPolicy, err := process.ParseOutputPolicy(tableString(options, "stderr", ""))
	if err != nil {
		state.RaiseError("exec.run stderr %v", err)
		return 0
	}
	captureLimit, err := process.ParseCaptureLimit(tableString(options, "capture_limit", ""))
	if err != nil {
		state.RaiseError("exec.run capture_limit %v", err)
		return 0
	}
	result, runErr := process.Run(state.Context(), process.Options{
		Command: command, Args: args, Dir: dir, Env: environment,
		Stdin: strings.NewReader(tableString(options, "stdin", "")), Stdout: r.request.Stdout, Stderr: r.request.Stderr,
		CaptureLimit: captureLimit, StdoutPolicy: stdoutPolicy, StderrPolicy: stderrPolicy,
	})
	table := state.NewTable()
	table.RawSetString("stdout", glua.LString(result.Stdout))
	table.RawSetString("stderr", glua.LString(result.Stderr))
	table.RawSetString("exit_code", glua.LNumber(result.ExitCode))
	table.RawSetString("stdout_truncated", glua.LBool(result.StdoutTruncated))
	table.RawSetString("stderr_truncated", glua.LBool(result.StderrTruncated))
	if runErr != nil {
		table.RawSetString("error", glua.LString(runErr.Error()))
	} else {
		table.RawSetString("error", glua.LNil)
	}
	state.Push(table)
	return 1
}

func (r *runtime) resolvePath(path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(r.request.RunDir, path)
}

func tableString(table *glua.LTable, key, fallback string) string {
	value := table.RawGetString(key)
	if value == glua.LNil {
		return fallback
	}
	return value.String()
}

func tableNumber(table *glua.LTable, key string, fallback float64) float64 {
	value := table.RawGetString(key)
	if number, ok := value.(glua.LNumber); ok {
		return float64(number)
	}
	return fallback
}

func tableStrings(value glua.LValue) ([]string, error) {
	if value == glua.LNil {
		return nil, nil
	}
	table, ok := value.(*glua.LTable)
	if !ok {
		return nil, fmt.Errorf("must be a list")
	}
	result := make([]string, 0, table.Len())
	for i := 1; i <= table.Len(); i++ {
		item := table.RawGetInt(i)
		if item.Type() != glua.LTString {
			return nil, fmt.Errorf("item %d must be a string", i)
		}
		result = append(result, item.String())
	}
	return result, nil
}

func toLua(state *glua.LState, value any) (glua.LValue, error) {
	switch typed := value.(type) {
	case nil:
		return glua.LNil, nil
	case string:
		return glua.LString(typed), nil
	case bool:
		return glua.LBool(typed), nil
	case int:
		return glua.LNumber(typed), nil
	case int64:
		return glua.LNumber(typed), nil
	case float64:
		return glua.LNumber(typed), nil
	case json.Number:
		number, err := typed.Float64()
		if err != nil {
			return nil, err
		}
		return glua.LNumber(number), nil
	case []any:
		table := state.NewTable()
		for _, item := range typed {
			converted, err := toLua(state, item)
			if err != nil {
				return nil, err
			}
			table.Append(converted)
		}
		return table, nil
	case map[string]any:
		table := state.NewTable()
		for key, item := range typed {
			converted, err := toLua(state, item)
			if err != nil {
				return nil, err
			}
			table.RawSetString(key, converted)
		}
		return table, nil
	default:
		data, err := json.Marshal(value)
		if err != nil {
			return nil, fmt.Errorf("unsupported value %T", value)
		}
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.UseNumber()
		var normalized any
		if err := decoder.Decode(&normalized); err != nil {
			return nil, err
		}
		return toLua(state, normalized)
	}
}

func fromLua(value glua.LValue, visiting map[*glua.LTable]bool) (any, error) {
	switch typed := value.(type) {
	case *glua.LNilType:
		return nil, nil
	case glua.LString:
		return string(typed), nil
	case glua.LBool:
		return bool(typed), nil
	case glua.LNumber:
		return float64(typed), nil
	case *glua.LTable:
		if visiting[typed] {
			return nil, fmt.Errorf("cyclic Lua table")
		}
		visiting[typed] = true
		defer delete(visiting, typed)
		array := make([]any, typed.Len())
		isArray := true
		count := 0
		object := make(map[string]any)
		var conversionErr error
		typed.ForEach(func(key, item glua.LValue) {
			if conversionErr != nil {
				return
			}
			converted, err := fromLua(item, visiting)
			if err != nil {
				conversionErr = err
				return
			}
			count++
			if number, ok := key.(glua.LNumber); ok && float64(number) == float64(int(number)) && int(number) >= 1 && int(number) <= typed.Len() {
				array[int(number)-1] = converted
				return
			}
			isArray = false
			if key.Type() != glua.LTString {
				conversionErr = fmt.Errorf("Lua object keys must be strings")
				return
			}
			object[key.String()] = converted
		})
		if conversionErr != nil {
			return nil, conversionErr
		}
		if typed.Len() > 0 && isArray && count == typed.Len() {
			return array, nil
		}
		if typed.Len() > 0 && count != len(object) {
			return nil, fmt.Errorf("mixed Lua table cannot be converted")
		}
		return object, nil
	default:
		return nil, fmt.Errorf("unsupported Lua value %s", value.Type().String())
	}
}
