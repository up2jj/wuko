package sdk

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
)

var namespacePattern = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

type Plugin struct {
	mu          sync.RWMutex
	namespace   string
	frozen      bool
	lifecycle   *Lifecycle
	actions     map[string]json.RawMessage
	steps       map[string]registeredStep
	executors   map[string]registeredExecutor
	helpers     map[string]HelperFunc
	sessions    map[string]ExecutorSession
	nextSession uint64
}

type registeredStep struct {
	declaration stepDeclaration
	validate    func(context.Context, json.RawMessage, StepContext) error
	run         func(context.Context, json.RawMessage, StepContext, io.Writer, io.Writer, func(Result) error) (Result, error)
	cleanup     func(context.Context, json.RawMessage, Result) error
	service     func(context.Context, json.RawMessage, StepContext) (Service, error)
}

type registeredExecutor struct {
	declaration executorDeclaration
	validate    func(context.Context, json.RawMessage, ExecutorContext) error
	open        func(context.Context, json.RawMessage, ExecutorContext) (ExecutorSession, error)
}

type stepDeclaration struct {
	Type          string           `json:"type"`
	Cleanup       bool             `json:"cleanup,omitempty"`
	Service       bool             `json:"service,omitempty"`
	HostCallbacks []HostCapability `json:"host_callbacks,omitempty"`
	Outputs       *OutputSchema    `json:"outputs,omitempty"`
}

type executorDeclaration struct {
	Type               string `json:"type"`
	CancelStopsProcess bool   `json:"cancel_stops_process"`
}

func New(namespace string) (*Plugin, error) {
	if !namespacePattern.MatchString(namespace) {
		return nil, fmt.Errorf("invalid plugin namespace %q", namespace)
	}
	return &Plugin{
		namespace: namespace,
		actions:   make(map[string]json.RawMessage),
		steps:     make(map[string]registeredStep),
		executors: make(map[string]registeredExecutor),
		helpers:   make(map[string]HelperFunc),
		sessions:  make(map[string]ExecutorSession),
	}, nil
}

func (plugin *Plugin) RegisterAction(name string, action Action) error {
	snapshot, err := snapshotAction(name, action)
	if err != nil {
		return err
	}
	plugin.mu.Lock()
	defer plugin.mu.Unlock()
	if err := plugin.canRegister(name, plugin.actions); err != nil {
		return err
	}
	plugin.actions[name] = snapshot
	return nil
}

func (plugin *Plugin) SetLifecycle(lifecycle Lifecycle) error {
	if lifecycle.Start == nil && lifecycle.Stop == nil {
		return fmt.Errorf("lifecycle must define Start or Stop")
	}
	plugin.mu.Lock()
	defer plugin.mu.Unlock()
	if plugin.frozen {
		return fmt.Errorf("plugin registrations are frozen")
	}
	if plugin.lifecycle != nil {
		return fmt.Errorf("plugin lifecycle is already registered")
	}
	copy := lifecycle
	plugin.lifecycle = &copy
	return nil
}

func (plugin *Plugin) RegisterHelper(name string, helper HelperFunc) error {
	if !actionIdentifier.MatchString(name) {
		return fmt.Errorf("invalid helper name %q", name)
	}
	if helper == nil {
		return fmt.Errorf("helper %q is nil", name)
	}
	plugin.mu.Lock()
	defer plugin.mu.Unlock()
	if err := plugin.canRegister(name, plugin.helpers); err != nil {
		return err
	}
	plugin.helpers[name] = helper
	return nil
}

func (plugin *Plugin) canRegister(name string, registry any) error {
	if plugin.frozen {
		return fmt.Errorf("plugin registrations are frozen")
	}
	duplicate := false
	switch values := registry.(type) {
	case map[string]json.RawMessage:
		_, duplicate = values[name]
	case map[string]registeredStep:
		_, duplicate = values[name]
	case map[string]registeredExecutor:
		_, duplicate = values[name]
	case map[string]HelperFunc:
		_, duplicate = values[name]
	}
	if duplicate {
		return fmt.Errorf("%q is already registered", name)
	}
	return nil
}

func RegisterStep[Config any](plugin *Plugin, name string, handler StepHandler[Config], options ...StepOption) error {
	if plugin == nil {
		return fmt.Errorf("plugin is nil")
	}
	if !actionIdentifier.MatchString(name) {
		return fmt.Errorf("invalid step name %q", name)
	}
	if handler.Run == nil {
		return fmt.Errorf("step %q requires a Run handler", name)
	}
	settings := stepOptions{}
	for _, option := range options {
		if option == nil {
			return fmt.Errorf("step %q has a nil option", name)
		}
		if err := option(&settings); err != nil {
			return fmt.Errorf("step %q: %w", name, err)
		}
	}
	qualified := plugin.namespace + "." + name
	registered := registeredStep{declaration: stepDeclaration{Type: qualified, Cleanup: handler.Cleanup != nil, Service: handler.Service != nil, HostCallbacks: settings.hostCallbacks, Outputs: settings.outputs}}
	registered.validate = func(ctx context.Context, raw json.RawMessage, stepContext StepContext) error {
		if handler.Validate == nil {
			_, err := decodeConfig[Config](raw)
			return err
		}
		config, err := decodeConfig[Config](raw)
		if err != nil {
			return err
		}
		return handler.Validate(ctx, StepRequest[Config]{Config: config, Context: stepContext, Host: hostClient{}})
	}
	registered.run = func(ctx context.Context, raw json.RawMessage, stepContext StepContext, stdout, stderr io.Writer, ready func(Result) error) (Result, error) {
		config, err := decodeConfig[Config](raw)
		if err != nil {
			return Result{}, err
		}
		return handler.Run(ctx, StepRequest[Config]{Config: config, Context: stepContext, Host: hostClient{}, Stdout: stdout, Stderr: stderr, Ready: ready})
	}
	if handler.Cleanup != nil {
		registered.cleanup = func(ctx context.Context, raw json.RawMessage, result Result) error {
			config, err := decodeConfig[Config](raw)
			if err != nil {
				return err
			}
			return handler.Cleanup(ctx, CleanupRequest[Config]{Config: config, Result: result})
		}
	}
	if handler.Service != nil {
		registered.service = func(ctx context.Context, raw json.RawMessage, stepContext StepContext) (Service, error) {
			config, err := decodeConfig[Config](raw)
			if err != nil {
				return Service{}, err
			}
			return handler.Service(ctx, StepRequest[Config]{Config: config, Context: stepContext, Host: hostClient{}})
		}
	}
	plugin.mu.Lock()
	defer plugin.mu.Unlock()
	if err := plugin.canRegister(name, plugin.steps); err != nil {
		return err
	}
	plugin.steps[name] = registered
	return nil
}

func RegisterExecutor[Config any](plugin *Plugin, name string, handler ExecutorHandler[Config]) error {
	if plugin == nil {
		return fmt.Errorf("plugin is nil")
	}
	if !actionIdentifier.MatchString(name) {
		return fmt.Errorf("invalid executor name %q", name)
	}
	if handler.Open == nil {
		return fmt.Errorf("executor %q requires an Open handler", name)
	}
	qualified := plugin.namespace + "." + name
	registered := registeredExecutor{declaration: executorDeclaration{Type: qualified, CancelStopsProcess: handler.CancelStopsProcess}}
	registered.validate = func(ctx context.Context, raw json.RawMessage, executorContext ExecutorContext) error {
		config, err := decodeConfig[Config](raw)
		if err != nil {
			return err
		}
		if handler.Validate == nil {
			return nil
		}
		return handler.Validate(ctx, ExecutorRequest[Config]{Config: config, Context: executorContext})
	}
	registered.open = func(ctx context.Context, raw json.RawMessage, executorContext ExecutorContext) (ExecutorSession, error) {
		config, err := decodeConfig[Config](raw)
		if err != nil {
			return nil, err
		}
		return handler.Open(ctx, ExecutorRequest[Config]{Config: config, Context: executorContext})
	}
	plugin.mu.Lock()
	defer plugin.mu.Unlock()
	if err := plugin.canRegister(name, plugin.executors); err != nil {
		return err
	}
	plugin.executors[name] = registered
	return nil
}

// Serve freezes registrations and serves the JSONL protocol until input closes,
// ctx is canceled, or shutdown is requested.
func (plugin *Plugin) Serve(ctx context.Context, input io.Reader, output io.Writer) error {
	if plugin == nil {
		return fmt.Errorf("plugin is nil")
	}
	if ctx == nil || input == nil || output == nil {
		return fmt.Errorf("context, input, and output are required")
	}
	plugin.mu.Lock()
	if plugin.frozen {
		plugin.mu.Unlock()
		return fmt.Errorf("plugin is already serving")
	}
	plugin.frozen = true
	plugin.mu.Unlock()
	err := serve(ctx, input, output, plugin.dispatch)
	plugin.closeSessions()
	return err
}

// closeSessions releases executor sessions the host never closed explicitly. A host that
// disconnects, is canceled, or shuts down without an executor.close would otherwise strand
// whatever a session owns -- a container, a remote connection, a scratch directory. serve has
// already canceled and awaited every in-flight request, so no handler still holds one.
func (plugin *Plugin) closeSessions() {
	plugin.mu.Lock()
	sessions := plugin.sessions
	plugin.sessions = make(map[string]ExecutorSession)
	plugin.mu.Unlock()
	if len(sessions) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, name := range sortedKeys(sessions) {
		_ = sessions[name].Close(ctx)
	}
}

func (plugin *Plugin) dispatch(ctx context.Context, req wireRequest, peer *peer) (any, error) {
	switch req.Method {
	case "initialize":
		var params struct {
			Protocol string `json:"protocol"`
		}
		if err := decodeParams(req.Params, &params); err != nil {
			return nil, err
		}
		if params.Protocol != Protocol {
			return nil, fmt.Errorf("unsupported protocol %q", params.Protocol)
		}
		plugin.mu.RLock()
		steps := make([]stepDeclaration, 0, len(plugin.steps))
		for _, name := range sortedKeys(plugin.steps) {
			steps = append(steps, plugin.steps[name].declaration)
		}
		executors := make([]executorDeclaration, 0, len(plugin.executors))
		for _, name := range sortedKeys(plugin.executors) {
			executors = append(executors, plugin.executors[name].declaration)
		}
		helpers := make([]map[string]string, 0, len(plugin.helpers))
		for _, name := range sortedKeys(plugin.helpers) {
			helpers = append(helpers, map[string]string{"name": name})
		}
		actions := sortedKeys(plugin.actions)
		lifecycle := plugin.lifecycle != nil
		plugin.mu.RUnlock()
		return map[string]any{"protocol": Protocol, "namespace": plugin.namespace, "lifecycle": lifecycle, "steps": steps, "executors": executors, "helpers": helpers, "actions": actions}, nil
	case "action.get":
		var params struct {
			Name string `json:"name"`
		}
		if err := decodeParams(req.Params, &params); err != nil {
			return nil, err
		}
		plugin.mu.RLock()
		action := slices.Clone(plugin.actions[params.Name])
		plugin.mu.RUnlock()
		if action == nil {
			return nil, Coded("not_found", fmt.Errorf("unknown action %q", params.Name))
		}
		return struct {
			Action json.RawMessage `json:"action"`
		}{Action: action}, nil
	case "plugin.start":
		var params struct {
			With map[string]any `json:"with"`
		}
		if err := decodeParams(req.Params, &params); err != nil {
			return nil, err
		}
		plugin.mu.RLock()
		lifecycle := plugin.lifecycle
		plugin.mu.RUnlock()
		if lifecycle != nil && lifecycle.Start != nil {
			return map[string]any{}, lifecycle.Start(ctx, params.With)
		}
		return map[string]any{}, nil
	case "plugin.stop":
		var params struct {
			Reason StopReason `json:"reason"`
		}
		if err := decodeParams(req.Params, &params); err != nil {
			return nil, err
		}
		plugin.mu.RLock()
		lifecycle := plugin.lifecycle
		plugin.mu.RUnlock()
		if lifecycle != nil && lifecycle.Stop != nil {
			return map[string]any{}, lifecycle.Stop(ctx, params.Reason)
		}
		return map[string]any{}, nil
	case "helper.call":
		return plugin.callHelper(ctx, req.Params)
	case "step.validate", "step.run", "step.cleanup", "step.service":
		return plugin.callStep(ctx, req, peer)
	case "executor.validate", "executor.open", "executor.run", "executor.close":
		return plugin.callExecutor(ctx, req, peer)
	default:
		return nil, Coded("method_not_found", fmt.Errorf("unknown method %q", req.Method))
	}
}

func (plugin *Plugin) callHelper(ctx context.Context, raw json.RawMessage) (any, error) {
	var params struct {
		Name string `json:"name"`
		Args []any  `json:"args"`
	}
	if err := decodeParams(raw, &params); err != nil {
		return nil, err
	}
	plugin.mu.RLock()
	helper := plugin.helpers[params.Name]
	plugin.mu.RUnlock()
	if helper == nil {
		return nil, fmt.Errorf("unknown helper %q", params.Name)
	}
	value, err := helper(ctx, params.Args)
	return map[string]any{"value": value}, err
}

func (plugin *Plugin) callStep(ctx context.Context, req wireRequest, peer *peer) (any, error) {
	var params struct {
		Type    string          `json:"type"`
		With    json.RawMessage `json:"with"`
		Context StepContext     `json:"context"`
		Result  Result          `json:"result"`
	}
	if err := decodeParams(req.Params, &params); err != nil {
		return nil, err
	}
	name := strings.TrimPrefix(params.Type, plugin.namespace+".")
	plugin.mu.RLock()
	step, ok := plugin.steps[name]
	plugin.mu.RUnlock()
	if !ok || step.declaration.Type != params.Type {
		return nil, fmt.Errorf("unknown step type %q", params.Type)
	}
	switch req.Method {
	case "step.validate":
		return map[string]any{}, step.validate(ctx, params.With, params.Context)
	case "step.cleanup":
		if step.cleanup == nil {
			return nil, fmt.Errorf("step %q has no cleanup handler", params.Type)
		}
		return map[string]any{}, step.cleanup(ctx, params.With, params.Result)
	case "step.service":
		if step.service == nil {
			return nil, fmt.Errorf("step %q is not a service", params.Type)
		}
		return step.service(ctx, params.With, params.Context)
	case "step.run":
		stdout := eventWriter{peer: peer, id: req.ID, event: "stdout"}
		stderr := eventWriter{peer: peer, id: req.ID, event: "stderr"}
		var ready func(Result) error
		if step.declaration.Service {
			ready = func(result Result) error { return peer.eventResult(req.ID, "ready", result) }
		}
		return step.run(ctx, params.With, params.Context, stdout, stderr, ready)
	default:
		panic("unreachable")
	}
}

func (plugin *Plugin) callExecutor(ctx context.Context, req wireRequest, peer *peer) (any, error) {
	if req.Method == "executor.run" || req.Method == "executor.close" {
		var params struct {
			Session      string            `json:"session"`
			Command      string            `json:"command"`
			Args         []string          `json:"args"`
			Dir          string            `json:"dir"`
			Env          map[string]string `json:"env"`
			Stdin        string            `json:"stdin"`
			CaptureLimit int64             `json:"capture_limit"`
			StdoutPolicy int               `json:"stdout_policy"`
			StderrPolicy int               `json:"stderr_policy"`
		}
		if err := decodeParams(req.Params, &params); err != nil {
			return nil, err
		}
		plugin.mu.RLock()
		session := plugin.sessions[params.Session]
		plugin.mu.RUnlock()
		if session == nil {
			return nil, fmt.Errorf("unknown executor session %q", params.Session)
		}
		if req.Method == "executor.close" {
			err := session.Close(ctx)
			plugin.mu.Lock()
			delete(plugin.sessions, params.Session)
			plugin.mu.Unlock()
			return map[string]any{}, err
		}
		stdin, err := base64.StdEncoding.DecodeString(params.Stdin)
		if err != nil {
			return nil, fmt.Errorf("decoding stdin: %w", err)
		}
		command := Command{Command: params.Command, Args: params.Args, Dir: params.Dir, Env: params.Env, Stdin: stdin, CaptureLimit: params.CaptureLimit, StdoutPolicy: params.StdoutPolicy, StderrPolicy: params.StderrPolicy, Stdout: eventWriter{peer: peer, id: req.ID, event: "stdout"}, Stderr: eventWriter{peer: peer, id: req.ID, event: "stderr"}, Started: func() { _ = peer.event(req.ID, "started", nil) }}
		return session.Run(ctx, command)
	}
	var params struct {
		Type    string          `json:"type"`
		With    json.RawMessage `json:"with"`
		Context ExecutorContext `json:"context"`
	}
	if err := decodeParams(req.Params, &params); err != nil {
		return nil, err
	}
	name := strings.TrimPrefix(params.Type, plugin.namespace+".")
	plugin.mu.RLock()
	executor, ok := plugin.executors[name]
	plugin.mu.RUnlock()
	if !ok || executor.declaration.Type != params.Type {
		return nil, fmt.Errorf("unknown executor type %q", params.Type)
	}
	if req.Method == "executor.validate" {
		return map[string]any{}, executor.validate(ctx, params.With, params.Context)
	}
	session, err := executor.open(ctx, params.With, params.Context)
	if err != nil {
		return nil, err
	}
	if session == nil {
		return nil, fmt.Errorf("executor %q returned a nil session", params.Type)
	}
	plugin.mu.Lock()
	plugin.nextSession++
	id := fmt.Sprintf("session-%d", plugin.nextSession)
	plugin.sessions[id] = session
	plugin.mu.Unlock()
	return map[string]any{"session": id}, nil
}

func decodeParams(raw json.RawMessage, target any) error {
	if len(raw) == 0 {
		raw = []byte("{}")
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return fmt.Errorf("decoding request parameters: %w", err)
	}
	return nil
}

func sortedKeys[Value any](values map[string]Value) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}
