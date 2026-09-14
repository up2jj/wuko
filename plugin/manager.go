package plugin

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/up2jj/wuko/executor"
	"github.com/up2jj/wuko/helper"
	"github.com/up2jj/wuko/process"
	"github.com/up2jj/wuko/step"
	luastep "github.com/up2jj/wuko/steps/lua"
	"github.com/up2jj/wuko/workflow"
)

type Config struct {
	CWD         func() (string, error)
	HomeDir     func() (string, error)
	ConfigDir   func() (string, error)
	LookPath    func(string) (string, error)
	Stderr      io.Writer
	HostVersion string
	HTTPClient  *http.Client
}

type Manager struct {
	config       Config
	mu           sync.Mutex
	declarations map[string]workflow.PluginSource
	plugins      map[string]*runningPlugin
	failures     map[string]error
	temporary    []string
	closed       bool
}

type initializeResult struct {
	Protocol  string                `json:"protocol"`
	Namespace string                `json:"namespace"`
	Lifecycle bool                  `json:"lifecycle,omitempty"`
	Steps     []stepDeclaration     `json:"steps"`
	Executors []executorDeclaration `json:"executors"`
	Helpers   []helperDeclaration   `json:"helpers,omitempty"`
}
type helperDeclaration struct {
	Name string `json:"name"`
}
type stepDeclaration struct {
	Type          string   `json:"type"`
	Cleanup       bool     `json:"cleanup,omitempty"`
	Service       bool     `json:"service,omitempty"`
	HostCallbacks []string `json:"host_callbacks,omitempty"`
}
type executorDeclaration struct {
	Type               string `json:"type"`
	CancelStopsProcess bool   `json:"cancel_stops_process"`
}

// executorRunResult defines the language-neutral wire format in snake_case.
type executorRunResult struct {
	Stdout          string `json:"stdout"`
	Stderr          string `json:"stderr"`
	ExitCode        int    `json:"exit_code"`
	StdoutTruncated bool   `json:"stdout_truncated"`
	StderrTruncated bool   `json:"stderr_truncated"`
}

func (result executorRunResult) processResult() process.Result {
	return process.Result{
		Stdout:          result.Stdout,
		Stderr:          result.Stderr,
		ExitCode:        result.ExitCode,
		StdoutTruncated: result.StdoutTruncated,
		StderrTruncated: result.StderrTruncated,
	}
}

type runningPlugin struct {
	namespace   string
	protocol    string
	client      *client
	initialized initializeResult
	startWith   map[string]any
	lifecycleMu sync.Mutex
	started     bool
	startErr    error
}

func NewManager(config Config) *Manager {
	if config.CWD == nil {
		config.CWD = os.Getwd
	}
	if config.HomeDir == nil {
		config.HomeDir = os.UserHomeDir
	}
	if config.ConfigDir == nil {
		config.ConfigDir = os.UserConfigDir
	}
	if config.LookPath == nil {
		config.LookPath = exec.LookPath
	}
	if config.Stderr == nil {
		config.Stderr = io.Discard
	}
	return &Manager{config: config, declarations: make(map[string]workflow.PluginSource), plugins: make(map[string]*runningPlugin), failures: make(map[string]error)}
}

func (m *Manager) ResolveStep(ctx context.Context, name string, raw map[string]any) (step.Runner, error) {
	if err := m.configureSources(workflow.PluginsFromContext(ctx)); err != nil {
		return nil, err
	}
	namespace, err := namespaceForType(name)
	if err != nil {
		return nil, fmt.Errorf("unknown step type %q", name)
	}
	plugin, err := m.load(ctx, namespace)
	if err != nil {
		return nil, err
	}
	var declaration *stepDeclaration
	for index := range plugin.initialized.Steps {
		if plugin.initialized.Steps[index].Type == name {
			declaration = &plugin.initialized.Steps[index]
			break
		}
	}
	if declaration == nil {
		return nil, fmt.Errorf("plugin %q does not declare step type %q", namespace, name)
	}
	base := &pluginStep{plugin: plugin, declaration: *declaration, name: name, raw: raw}
	if declaration.Cleanup {
		return &cleaningPluginStep{pluginStep: base}, nil
	}
	return base, nil
}

func (m *Manager) ResolveExecutor(ctx context.Context, name string, raw map[string]any) (executor.Provider, error) {
	if err := m.configureSources(workflow.PluginsFromContext(ctx)); err != nil {
		return nil, err
	}
	namespace, err := namespaceForType(name)
	if err != nil {
		return nil, fmt.Errorf("unknown executor type %q", name)
	}
	plugin, err := m.load(ctx, namespace)
	if err != nil {
		return nil, err
	}
	for _, declaration := range plugin.initialized.Executors {
		if declaration.Type == name {
			return &pluginExecutor{plugin: plugin, name: name, raw: raw, cancelStops: declaration.CancelStopsProcess}, nil
		}
	}
	return nil, fmt.Errorf("plugin %q does not declare executor type %q", namespace, name)
}

var helperNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// LoadHelpers initializes every explicitly declared plugin and returns its immutable helper set.
func (m *Manager) LoadHelpers(ctx context.Context, sources map[string]workflow.PluginSource) (helper.Set, error) {
	if err := m.configureSources(sources); err != nil {
		return nil, err
	}
	result := make(helper.Set)
	for _, namespace := range slices.Sorted(maps.Keys(sources)) {
		loaded, err := m.load(ctx, namespace)
		if err != nil {
			return nil, err
		}
		for _, declaration := range loaded.initialized.Helpers {
			exposed := exposedHelperName(namespace, declaration.Name)
			if !helperNamePattern.MatchString(declaration.Name) {
				return nil, fmt.Errorf("plugin %q has invalid helper declaration %q", namespace, declaration.Name)
			}
			if _, exists := result[exposed]; exists {
				return nil, fmt.Errorf("plugin helper name %q is declared more than once", exposed)
			}
			plugin, localName := loaded, declaration.Name
			result[exposed] = func(callCtx context.Context, args []any) (any, error) {
				if err := plugin.start(callCtx); err != nil {
					return nil, err
				}
				var response struct {
					Value any `json:"value"`
				}
				if err := plugin.client.call(callCtx, "helper.call", map[string]any{"name": localName, "args": args}, &response, nil); err != nil {
					return nil, err
				}
				return response.Value, nil
			}
		}
	}
	return result, nil
}

func exposedHelperName(namespace, name string) string {
	return strings.ReplaceAll(namespace, "-", "_") + "_" + name
}

func (m *Manager) configureSources(sources map[string]workflow.PluginSource) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for namespace, source := range sources {
		canonical, err := source.CanonicalSource()
		if err != nil {
			return err
		}
		source.Source = canonical
		if previous, ok := m.declarations[namespace]; ok {
			if previous.Source != source.Source || previous.SHA256 != source.SHA256 {
				return fmt.Errorf("plugin %q has conflicting workflow declarations: %s (%s) and %s (%s)", namespace, diagnosticPluginSource(previous.Source), shortDigest(previous.SHA256), diagnosticPluginSource(source.Source), shortDigest(source.SHA256))
			}
			if !reflect.DeepEqual(previous.With, source.With) {
				return fmt.Errorf("plugin %q has conflicting workflow configuration for %s (%s)", namespace, diagnosticPluginSource(source.Source), shortDigest(source.SHA256))
			}
			continue
		}
		if m.plugins[namespace] != nil {
			return fmt.Errorf("plugin %q was loaded before its authoritative declaration", namespace)
		}
		m.declarations[namespace] = source
	}
	return nil
}

func diagnosticPluginSource(source string) string {
	if !strings.HasPrefix(source, "https://") {
		return source
	}
	parsed, err := url.Parse(source)
	if err != nil {
		return "HTTPS source"
	}
	parsed.RawQuery = ""
	parsed.ForceQuery = false
	parsed.Fragment = ""
	return parsed.String()
}

func shortDigest(value string) string {
	if len(value) <= 12 {
		return value
	}
	return value[:12]
}

func namespaceForType(name string) (string, error) {
	parts := strings.Split(name, ".")
	if len(parts) != 2 || !validNamespace(parts[0]) || parts[1] == "" {
		return "", fmt.Errorf("not a plugin type")
	}
	return parts[0], nil
}

func (m *Manager) load(ctx context.Context, namespace string) (result *runningPlugin, resultErr error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	defer func() {
		// Context errors are transient: a run canceled mid-download must not poison the
		// namespace for the cleanup scope, which runs with cancellation stripped.
		if resultErr != nil && !m.closed && !errors.Is(resultErr, context.Canceled) && !errors.Is(resultErr, context.DeadlineExceeded) {
			m.failures[namespace] = resultErr
		}
	}()
	if m.closed {
		return nil, fmt.Errorf("plugin manager is closed")
	}
	if existing := m.plugins[namespace]; existing != nil {
		return existing, nil
	}
	if failure := m.failures[namespace]; failure != nil {
		return nil, failure
	}
	var path string
	var protocol string
	var startWith map[string]any
	if declaration, ok := m.declarations[namespace]; ok {
		if !strings.HasPrefix(declaration.Source, "https://") && !strings.HasPrefix(declaration.Source, "github:") {
			return nil, fmt.Errorf("workflow-scoped plugin %q must use an HTTPS or GitHub manifest", namespace)
		}
		release, err := FetchRelease(ctx, declaration.Source, declaration.SHA256, m.config.HTTPClient)
		if err != nil {
			return nil, fmt.Errorf("fetching plugin %q: %w", namespace, err)
		}
		if release.Manifest.Namespace != namespace {
			return nil, fmt.Errorf("plugin declaration namespace %q conflicts with manifest namespace %q", namespace, release.Manifest.Namespace)
		}
		protocol = release.Manifest.Protocol
		directory, err := os.MkdirTemp("", "wuko-plugin-"+namespace+"-")
		if err != nil {
			return nil, err
		}
		path, err = Extract(release, directory)
		if err != nil {
			_ = os.RemoveAll(directory)
			return nil, err
		}
		m.temporary = append(m.temporary, directory)
		if startWith, err = cloneWith(declaration.With); err != nil {
			return nil, err
		}
	} else {
		var err error
		path, err = m.findLocal(namespace)
		if err != nil {
			return nil, err
		}
		protocol, err = localPluginProtocol(path, namespace)
		if err != nil {
			return nil, err
		}
	}
	c, initialized, protocol, err := launchInitialized(ctx, path, namespace, protocol, m.config.HostVersion, m.config.Stderr)
	if err != nil {
		return nil, err
	}
	if err := validateInitializeDeclarations(namespace, protocol, initialized); err != nil {
		closeClient(c)
		return nil, err
	}
	plugin := &runningPlugin{namespace: namespace, protocol: protocol, client: c, initialized: initialized, startWith: startWith}
	m.plugins[namespace] = plugin
	return plugin, nil
}

func launchInitialized(ctx context.Context, path, namespace, protocol, hostVersion string, stderr io.Writer) (*client, initializeResult, string, error) {
	protocols := []string{protocol}
	// An installed release pins its protocol; only a bare development or PATH executable leaves
	// Wuko to negotiate, and only then may the plugin answer with a protocol other than the one
	// offered.
	negotiating := protocol == ""
	if negotiating {
		protocols = []string{ProtocolV2, ProtocolV1}
	}
	var attempts []error
	for _, candidate := range protocols {
		client, err := launch(ctx, path, stderr)
		if err != nil {
			attempts = append(attempts, fmt.Errorf("%s launch: %w", candidate, err))
			continue
		}
		var initialized initializeResult
		err = client.call(ctx, "initialize", map[string]any{"protocol": candidate, "host_version": hostVersion}, &initialized, nil)
		if err == nil && initialized.Namespace == namespace {
			if initialized.Protocol == candidate {
				return client, initialized, candidate, nil
			}
			// The plugin named a protocol Wuko was going to offer anyway, so take it at its
			// word rather than shutting the process down and relaunching it to be told the
			// same thing. Declarations are validated against the accepted protocol next.
			if negotiating && slices.Contains(protocols, initialized.Protocol) {
				return client, initialized, initialized.Protocol, nil
			}
		}
		if err == nil {
			err = fmt.Errorf("handshake namespace or protocol mismatch")
		}
		attempts = append(attempts, fmt.Errorf("%s: %w", candidate, err))
		closeClient(client)
		if ctx.Err() != nil {
			break
		}
	}
	return nil, initializeResult{}, "", fmt.Errorf("initializing plugin %q: %w", namespace, errors.Join(attempts...))
}

func validateInitializeDeclarations(namespace, protocol string, initialized initializeResult) error {
	seen := make(map[string]bool)
	for _, item := range initialized.Steps {
		if seen[item.Type] || !strings.HasPrefix(item.Type, namespace+".") {
			return fmt.Errorf("plugin %q has invalid or duplicate declaration %q", namespace, item.Type)
		}
		if protocol == ProtocolV1 && (item.Service || len(item.HostCallbacks) != 0) {
			return fmt.Errorf("plugin %q declares v2 step capabilities with protocol v1", namespace)
		}
		seenCallbacks := make(map[string]bool)
		for _, callback := range item.HostCallbacks {
			if seenCallbacks[callback] || (callback != "host.template.validate" && callback != "host.template.render" && callback != "host.function.call") {
				return fmt.Errorf("plugin %q has invalid or duplicate host callback %q", namespace, callback)
			}
			seenCallbacks[callback] = true
		}
		seen[item.Type] = true
	}
	for _, item := range initialized.Executors {
		if seen[item.Type] || !strings.HasPrefix(item.Type, namespace+".") {
			return fmt.Errorf("plugin %q has invalid or duplicate declaration %q", namespace, item.Type)
		}
		seen[item.Type] = true
	}
	seenHelpers := make(map[string]bool)
	reservedHelpers := luastep.BuiltinHelperNames()
	for _, item := range initialized.Helpers {
		if seenHelpers[item.Name] || !helperNamePattern.MatchString(item.Name) {
			return fmt.Errorf("plugin %q has invalid or duplicate helper declaration %q", namespace, item.Name)
		}
		exposed := exposedHelperName(namespace, item.Name)
		if _, reserved := reservedHelpers[exposed]; reserved {
			return fmt.Errorf("plugin %q helper %q conflicts with built-in helper %q", namespace, item.Name, exposed)
		}
		seenHelpers[item.Name] = true
	}
	return nil
}

func localPluginProtocol(path, namespace string) (string, error) {
	directory := filepath.Dir(path)
	if _, err := os.Stat(filepath.Join(directory, MarkerName)); os.IsNotExist(err) {
		return "", nil
	} else if err != nil {
		return "", err
	}
	marker, err := ValidateInstallation(directory, namespace)
	if err != nil {
		return "", err
	}
	return marker.Protocol, nil
}

func (m *Manager) findLocal(namespace string) (string, error) {
	name := "wuko-plugin-" + namespace
	cwd, err := m.config.CWD()
	if err != nil {
		return "", err
	}
	for directory := cwd; ; directory = filepath.Dir(directory) {
		candidateDir := filepath.Join(directory, ".wuko", "plugins", namespace)
		if path, found, err := pluginCandidate(candidateDir, name); found || err != nil {
			return path, err
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			break
		}
	}
	home, err := m.config.HomeDir()
	if err != nil {
		return "", err
	}
	if path, found, err := pluginCandidate(filepath.Join(home, ".wuko", "plugins", namespace), name); found || err != nil {
		return path, err
	}
	config, err := m.config.ConfigDir()
	if err != nil {
		return "", err
	}
	if path, found, err := pluginCandidate(filepath.Join(config, "wuko", "plugins", namespace), name); found || err != nil {
		return path, err
	}
	path, err := m.config.LookPath(name)
	if err != nil {
		return "", fmt.Errorf("plugin %q is not installed: %w", namespace, err)
	}
	return path, nil
}

func pluginCandidate(directory, name string) (string, bool, error) {
	info, err := os.Stat(directory)
	if os.IsNotExist(err) {
		return "", false, nil
	}
	if err != nil {
		return "", true, err
	}
	if !info.IsDir() {
		return "", true, fmt.Errorf("plugin location %s is not a directory", directory)
	}
	path := filepath.Join(directory, name)
	info, err = os.Stat(path)
	if err != nil {
		return "", true, fmt.Errorf("invalid plugin installation %s: %w", directory, err)
	}
	if !info.Mode().IsRegular() || info.Mode()&0111 == 0 {
		return "", true, fmt.Errorf("plugin executable %s is not a regular executable", path)
	}
	return path, true, nil
}

func (plugin *runningPlugin) start(ctx context.Context) error {
	plugin.lifecycleMu.Lock()
	defer plugin.lifecycleMu.Unlock()
	if plugin.started {
		return nil
	}
	if plugin.startErr != nil {
		return plugin.startErr
	}
	if !plugin.initialized.Lifecycle {
		plugin.started = true
		return nil
	}
	err := plugin.client.call(ctx, "plugin.start", map[string]any{"with": plugin.startWith}, &struct{}{}, nil)
	if err == nil {
		plugin.started = true
		return nil
	}
	// Context errors are transient, exactly as in Manager.load: a run canceled while the plugin
	// was starting must not poison it for the cleanup scope, which runs with cancellation
	// stripped and still has to stop what the run left behind.
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	plugin.startErr = err
	return err
}

func (plugin *runningPlugin) lifecycleStarted() bool {
	plugin.lifecycleMu.Lock()
	defer plugin.lifecycleMu.Unlock()
	return plugin.started && plugin.initialized.Lifecycle
}

// closeClient tears down a client that never became a managed plugin. The bounded
// context stops a plugin that ignores shutdown from hanging the manager lock.
func closeClient(c *client) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = c.close(ctx)
}

// Close stops every started plugin, shuts down processes, and joins teardown errors.
func (m *Manager) Close(ctx context.Context, reason string) error {
	return m.teardown(ctx, reason, true)
}

// Reset stops every started plugin and drops the declarations, loaded plugins, and extracted
// installations left by the run that just finished, leaving the manager usable. A host that runs
// several workflows in one process - the picker reopened by a return destination - owes each run
// its own plugin declarations and lifecycle bracket, exactly as a separate invocation gets.
func (m *Manager) Reset(ctx context.Context, reason string) error {
	return m.teardown(ctx, reason, false)
}

func (m *Manager) teardown(ctx context.Context, reason string, permanent bool) error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = permanent
	plugins := make([]*runningPlugin, 0, len(m.plugins))
	for _, p := range m.plugins {
		plugins = append(plugins, p)
	}
	temporary := append([]string(nil), m.temporary...)
	if !permanent {
		clear(m.declarations)
		clear(m.plugins)
		clear(m.failures)
		m.temporary = nil
	}
	m.mu.Unlock()
	var result error
	for _, p := range plugins {
		// ctx already has cancellation stripped by the caller; deriving from it keeps the
		// whole teardown inside the caller's budget instead of five seconds per plugin.
		if p.lifecycleStarted() {
			stopCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			err := p.client.call(stopCtx, "plugin.stop", map[string]any{"reason": reason}, &struct{}{}, nil)
			cancel()
			result = errors.Join(result, err)
		}
		closeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		result = errors.Join(result, p.client.close(closeCtx))
		cancel()
	}
	for _, directory := range temporary {
		result = errors.Join(result, os.RemoveAll(directory))
	}
	return result
}

type pluginStep struct {
	plugin      *runningPlugin
	declaration stepDeclaration
	name        string
	raw         map[string]any
}

func (s *pluginStep) Validate(ctx context.Context, request step.Request) error {
	params := map[string]any{"type": s.name, "with": s.raw, "context": stepContext(request, s.plugin.protocol)}
	if s.plugin.protocol == ProtocolV2 {
		return s.plugin.client.callWithOptions(ctx, "step.validate", params, &struct{}{}, callOptions{host: s.hostCallbacks(request)})
	}
	return s.plugin.client.call(ctx, "step.validate", params, &struct{}{}, nil)
}
func (s *pluginStep) Run(ctx context.Context, request step.Request) (step.Result, error) {
	if err := s.plugin.start(ctx); err != nil {
		return step.Result{}, err
	}
	if s.plugin.protocol == ProtocolV2 && s.declaration.Service {
		return s.runService(ctx, request)
	}
	var result step.Result
	params := map[string]any{"type": s.name, "with": s.raw, "context": stepContext(request, s.plugin.protocol)}
	if s.plugin.protocol == ProtocolV2 {
		err := s.plugin.client.callWithOptions(ctx, "step.run", params, &result, callOptions{event: streamEvents(request.Stdout, request.Stderr, nil), host: s.hostCallbacks(request)})
		return result, err
	}
	err := s.plugin.client.call(ctx, "step.run", params, &result, streamEvents(request.Stdout, request.Stderr, nil))
	return result, err
}

type serviceOptionsResult struct {
	Kind      string `json:"kind"`
	KeepAlive bool   `json:"keep_alive"`
	FailFast  bool   `json:"fail_fast"`
	ExitOnEnd bool   `json:"exit_on_end"`
}

type serviceStartup struct {
	result step.Result
	err    error
}

func (s *pluginStep) runService(ctx context.Context, request step.Request) (step.Result, error) {
	if request.Services == nil {
		return step.Result{}, fmt.Errorf("plugin service %q requires the workflow service supervisor", s.name)
	}
	var service serviceOptionsResult
	params := map[string]any{"type": s.name, "with": s.raw, "context": stepContext(request, ProtocolV2)}
	if err := s.plugin.client.call(ctx, "step.service", params, &service, nil); err != nil {
		return step.Result{}, err
	}
	if strings.TrimSpace(service.Kind) == "" {
		return step.Result{}, fmt.Errorf("plugin service %q returned an empty kind", s.name)
	}
	frozen := freezeStepRequest(request)
	params["context"] = stepContext(frozen, ProtocolV2)
	startup := make(chan serviceStartup, 1)
	if err := request.Services.StartService(request.StepID, service.Kind, step.ServiceOptions{KeepAlive: service.KeepAlive, FailFast: service.FailFast, ExitOnEnd: service.ExitOnEnd}, func(serviceCtx context.Context) error {
		callCtx, cancel := context.WithCancel(serviceCtx)
		defer cancel()
		var ready atomic.Bool
		var readyEvents atomic.Uint32
		protocolFailure := make(chan error, 1)
		var startupOnce sync.Once
		reportStartup := func(value serviceStartup) { startupOnce.Do(func() { startup <- value }) }
		events := streamEvents(request.Stdout, request.Stderr, nil)
		event := func(frame eventFrame) {
			if frame.Event != "ready" {
				events(frame)
				return
			}
			if readyEvents.Add(1) != 1 {
				failure := fmt.Errorf("plugin service %q sent more than one readiness event", s.name)
				select {
				case protocolFailure <- failure:
				default:
				}
				cancel()
				return
			}
			var result step.Result
			if len(frame.Result) == 0 || decodeNumber(frame.Result, &result) != nil {
				reportStartup(serviceStartup{err: fmt.Errorf("plugin service %q sent malformed readiness result", s.name)})
				cancel()
				return
			}
			ready.Store(true)
			reportStartup(serviceStartup{result: result})
		}
		var final step.Result
		err := s.plugin.client.callWithOptions(callCtx, "step.run", params, &final, callOptions{event: event, host: s.hostCallbacks(frozen), drainCancel: 10 * time.Second})
		select {
		case failure := <-protocolFailure:
			err = errors.Join(failure, err)
		default:
		}
		if !ready.Load() {
			if err == nil {
				err = fmt.Errorf("plugin service %q exited before readiness", s.name)
			}
			reportStartup(serviceStartup{err: err})
			return errors.Join(step.ErrServiceAborted, err)
		}
		return err
	}); err != nil {
		return step.Result{}, err
	}
	select {
	case started := <-startup:
		return started.result, started.err
	default:
	}
	select {
	case started := <-startup:
		return started.result, started.err
	case <-ctx.Done():
		select {
		case started := <-startup:
			return started.result, started.err
		default:
			return step.Result{}, ctx.Err()
		}
	}
}

func decodeNumber(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	return decoder.Decode(target)
}

type cleaningPluginStep struct{ *pluginStep }

func (s *cleaningPluginStep) Cleanup(ctx context.Context, result step.Result) error {
	wireResult := map[string]any{"outputs": result.Outputs, "variables": result.Variables}
	return s.plugin.client.call(ctx, "step.cleanup", map[string]any{"type": s.name, "with": s.raw, "result": wireResult}, &struct{}{}, nil)
}

func stepContext(r step.Request, protocol string) map[string]any {
	result := map[string]any{"step_id": r.StepID, "workflow_name": r.WorkflowName, "workflow_dir": r.WorkflowDir, "run_dir": r.RunDir, "vars": r.Vars, "inputs": r.Inputs, "env": r.Env, "steps": r.Steps, "dependencies": r.Dependencies, "attempt": r.Attempt, "max_attempts": r.MaxAttempts, "operation_id": r.OperationID}
	if protocol == ProtocolV2 {
		result["workflow_source"] = r.WorkflowSource
		result["workflow_dir_borrowed"] = r.WorkflowDirBorrowed
		result["workflow_timezone"] = r.WorkflowTimezone
		result["environment_loaders"] = r.EnvironmentLoaders
		result["local_value_dir"] = r.LocalValueDir
		result["global_value_dir"] = r.GlobalValueDir
		result["preset_vars"] = r.PresetVars
		result["bindings"] = r.Bindings
		result["providers"] = r.Providers.Values
		result["helpers"] = r.Helpers.Names()
		if r.PreviousAttempt != nil {
			result["previous_attempt"] = map[string]any{"outputs": r.PreviousAttempt.Outputs, "variables": r.PreviousAttempt.Variables}
		}
	}
	return result
}

func freezeStepRequest(request step.Request) step.Request {
	request.EnvironmentLoaders = slices.Clone(request.EnvironmentLoaders)
	request.Vars = workflow.CloneMap(request.Vars)
	request.PresetVars = workflow.CloneMap(request.PresetVars)
	request.Inputs = workflow.CloneMap(request.Inputs)
	request.Env = maps.Clone(request.Env)
	request.Steps = workflow.CloneMap(request.Steps)
	request.Dependencies = workflow.CloneDependencies(request.Dependencies)
	request.Bindings = workflow.CloneMap(request.Bindings)
	request.Providers = request.Providers.Clone()
	request.Helpers = request.Helpers.Clone()
	if request.PreviousAttempt != nil {
		request.PreviousAttempt = &step.Result{Outputs: workflow.CloneMap(request.PreviousAttempt.Outputs), Variables: workflow.CloneMap(request.PreviousAttempt.Variables)}
	}
	if renderer, ok := request.TemplateRenderer.(step.DataTemplateRenderer); ok {
		request.TemplateRenderer = renderer.Snapshot()
	}
	return request
}

func (s *pluginStep) hostCallbacks(request step.Request) hostCall {
	allowed := make(map[string]bool, len(s.declaration.HostCallbacks))
	for _, method := range s.declaration.HostCallbacks {
		allowed[method] = true
	}
	// Rendering runs the workflow function map, so host.template.render would otherwise reach
	// secret as well and make the declared-callback list meaningless as a boundary: a plugin
	// that asked only to render templates could read every secret the run can resolve. A step
	// that declared host.function.call may already resolve secrets directly and keeps the
	// unrestricted renderer; every other step renders through one whose secret helper fails.
	// The variant is built once per step run because it clones the compiled template set.
	renderer := sync.OnceValue(func() step.DataTemplateRenderer {
		data, ok := request.TemplateRenderer.(step.DataTemplateRenderer)
		if !ok {
			return nil
		}
		if allowed["host.function.call"] {
			return data
		}
		return data.WithoutSecrets()
	})
	return func(ctx context.Context, method string, raw json.RawMessage) (any, error) {
		if !allowed[method] {
			return nil, fmt.Errorf("plugin step %q did not declare host callback %q", s.name, method)
		}
		switch method {
		case "host.template.validate":
			var params struct {
				ParentID string `json:"parent_id"`
				Content  string `json:"content"`
			}
			if err := decodeFrame(raw, &params); err != nil {
				return nil, fmt.Errorf("decoding template validation request: %w", err)
			}
			if request.TemplateRenderer == nil {
				return nil, fmt.Errorf("template renderer is unavailable")
			}
			if err := request.TemplateRenderer.ValidateContent(params.Content); err != nil {
				return nil, err
			}
			return struct{}{}, nil
		case "host.template.render":
			var params struct {
				ParentID string         `json:"parent_id"`
				Content  string         `json:"content"`
				Extra    map[string]any `json:"extra,omitempty"`
			}
			if err := decodeFrame(raw, &params); err != nil {
				return nil, fmt.Errorf("decoding template render request: %w", err)
			}
			data := renderer()
			if data == nil {
				return nil, fmt.Errorf("data template renderer is unavailable")
			}
			value, err := data.RenderContentWith(params.Content, params.Extra)
			if err != nil {
				return nil, err
			}
			return map[string]any{"value": value}, nil
		case "host.function.call":
			var params struct {
				ParentID string `json:"parent_id"`
				Name     string `json:"name"`
				Args     []any  `json:"args"`
			}
			if err := decodeFrame(raw, &params); err != nil {
				return nil, fmt.Errorf("decoding function call: %w", err)
			}
			if params.Name == "secret" {
				if len(params.Args) != 1 {
					return nil, fmt.Errorf("secret requires one string argument")
				}
				reference, ok := params.Args[0].(string)
				if !ok {
					return nil, fmt.Errorf("secret requires one string argument")
				}
				value, err := request.ResolveSecret(reference)
				if err != nil {
					return nil, err
				}
				return map[string]any{"value": value}, nil
			}
			call := request.Helpers[params.Name]
			if call == nil {
				return nil, fmt.Errorf("workflow helper %q is unavailable", params.Name)
			}
			value, err := call(ctx, params.Args)
			if err != nil {
				return nil, err
			}
			return map[string]any{"value": value}, nil
		default:
			return nil, fmt.Errorf("unsupported host callback %q", method)
		}
	}
}
func streamEvents(stdout, stderr io.Writer, started func()) func(eventFrame) {
	return func(event eventFrame) {
		switch event.Event {
		case "stdout", "stderr":
			data, err := base64.StdEncoding.DecodeString(event.Data)
			if err != nil {
				return
			}
			if event.Event == "stdout" && stdout != nil {
				_, _ = stdout.Write(data)
			} else if stderr != nil {
				_, _ = stderr.Write(data)
			}
		case "started":
			if started != nil {
				started()
			}
		}
	}
}

type pluginExecutor struct {
	plugin      *runningPlugin
	name        string
	raw         map[string]any
	cancelStops bool
}

func (p *pluginExecutor) Validate(ctx context.Context, r executor.Request) error {
	return p.plugin.client.call(ctx, "executor.validate", map[string]any{"type": p.name, "with": p.raw, "context": executorContext(r)}, &struct{}{}, nil)
}
func (p *pluginExecutor) Open(ctx context.Context, r executor.Request) (executor.Session, error) {
	if err := p.plugin.start(ctx); err != nil {
		return nil, err
	}
	var result struct {
		Session string `json:"session"`
	}
	if err := p.plugin.client.call(ctx, "executor.open", map[string]any{"type": p.name, "with": p.raw, "context": executorContext(r)}, &result, nil); err != nil {
		return nil, err
	}
	if result.Session == "" {
		return nil, fmt.Errorf("plugin returned an empty executor session")
	}
	return &pluginSession{plugin: p.plugin, id: result.Session, cancelStops: p.cancelStops}, nil
}
func executorContext(r executor.Request) map[string]any {
	return map[string]any{"workflow_name": r.WorkflowName, "run_dir": r.RunDir, "env": r.Env}
}

type pluginSession struct {
	plugin      *runningPlugin
	id          string
	cancelStops bool
}

func (s *pluginSession) CancelStopsProcess() bool { return s.cancelStops }
func (s *pluginSession) Run(ctx context.Context, o process.Options) (process.Result, error) {
	if o.TTY || o.Interactions != nil || o.Interact || o.StdinOutlivesProcess {
		return process.Result{}, fmt.Errorf("plugin executors do not support TTY, interactions, or streaming stdin")
	}
	if !o.StdoutPolicy.Valid() || !o.StderrPolicy.Valid() {
		return process.Result{}, fmt.Errorf("invalid output policy")
	}
	var stdin []byte
	var err error
	if o.Stdin != nil {
		stdin, err = io.ReadAll(o.Stdin)
		if err != nil {
			return process.Result{}, err
		}
	}
	params := map[string]any{"session": s.id, "command": o.Command, "args": o.Args, "dir": o.Dir, "env": o.Env, "stdin": base64.StdEncoding.EncodeToString(stdin), "capture_limit": o.CaptureLimit, "stdout_policy": o.StdoutPolicy, "stderr_policy": o.StderrPolicy}
	var wireResult executorRunResult
	stdout, stderr := o.Stdout, o.Stderr
	if !o.StdoutPolicy.Streams() {
		stdout = nil
	}
	if !o.StderrPolicy.Streams() {
		stderr = nil
	}
	err = s.plugin.client.call(ctx, "executor.run", params, &wireResult, streamEvents(stdout, stderr, o.Started))
	result := wireResult.processResult()
	if !o.StdoutPolicy.Captures() {
		result.Stdout = ""
	}
	if !o.StderrPolicy.Captures() {
		result.Stderr = ""
	}
	if o.CaptureLimit > 0 {
		if int64(len(result.Stdout)) > o.CaptureLimit {
			result.Stdout = result.Stdout[:o.CaptureLimit]
			result.StdoutTruncated = true
		}
		if int64(len(result.Stderr)) > o.CaptureLimit {
			result.Stderr = result.Stderr[:o.CaptureLimit]
			result.StderrTruncated = true
		}
	}
	if err == nil && result.ExitCode != 0 {
		err = &process.ExitError{Command: o.Command, Code: result.ExitCode}
	}
	return result, err
}
func (s *pluginSession) Close(ctx context.Context) error {
	return s.plugin.client.call(ctx, "executor.close", map[string]any{"session": s.id}, &struct{}{}, nil)
}

var _ executor.Provider = (*pluginExecutor)(nil)
var _ process.CancelPolicy = (*pluginSession)(nil)

func cloneWith(value map[string]any) (map[string]any, error) {
	if value == nil {
		return nil, nil
	}
	data, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("encoding plugin start parameters: %w", err)
	}
	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("decoding plugin start parameters: %w", err)
	}
	return result, nil
}
