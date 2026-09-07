package plugin

import (
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
	Type    string `json:"type"`
	Cleanup bool   `json:"cleanup,omitempty"`
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
	base := &pluginStep{plugin: plugin, name: name, raw: raw}
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
	}
	c, err := launch(ctx, path, m.config.Stderr)
	if err != nil {
		return nil, fmt.Errorf("plugin %q: %w", namespace, err)
	}
	var initialized initializeResult
	if err := c.call(ctx, "initialize", map[string]any{"protocol": Protocol, "host_version": m.config.HostVersion}, &initialized, nil); err != nil {
		closeClient(c)
		return nil, fmt.Errorf("initializing plugin %q: %w", namespace, err)
	}
	if initialized.Protocol != Protocol || initialized.Namespace != namespace {
		closeClient(c)
		return nil, fmt.Errorf("plugin %q handshake namespace or protocol mismatch", namespace)
	}
	if err := validateInitializeDeclarations(namespace, initialized); err != nil {
		closeClient(c)
		return nil, err
	}
	plugin := &runningPlugin{namespace: namespace, client: c, initialized: initialized, startWith: startWith}
	m.plugins[namespace] = plugin
	return plugin, nil
}

func validateInitializeDeclarations(namespace string, initialized initializeResult) error {
	seen := make(map[string]bool)
	for _, item := range initialized.Steps {
		if seen[item.Type] || !strings.HasPrefix(item.Type, namespace+".") {
			return fmt.Errorf("plugin %q has invalid or duplicate declaration %q", namespace, item.Type)
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
	if plugin.started || plugin.startErr != nil {
		return plugin.startErr
	}
	if plugin.initialized.Lifecycle {
		plugin.startErr = plugin.client.call(ctx, "plugin.start", map[string]any{"with": plugin.startWith}, &struct{}{}, nil)
	}
	if plugin.startErr == nil {
		plugin.started = true
	}
	return plugin.startErr
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
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	plugins := make([]*runningPlugin, 0, len(m.plugins))
	for _, p := range m.plugins {
		plugins = append(plugins, p)
	}
	temporary := append([]string(nil), m.temporary...)
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
	plugin *runningPlugin
	name   string
	raw    map[string]any
}

func (s *pluginStep) Validate(ctx context.Context, request step.Request) error {
	return s.plugin.client.call(ctx, "step.validate", map[string]any{"type": s.name, "with": s.raw, "context": stepContext(request)}, &struct{}{}, nil)
}
func (s *pluginStep) Run(ctx context.Context, request step.Request) (step.Result, error) {
	if err := s.plugin.start(ctx); err != nil {
		return step.Result{}, err
	}
	var result step.Result
	err := s.plugin.client.call(ctx, "step.run", map[string]any{"type": s.name, "with": s.raw, "context": stepContext(request)}, &result, streamEvents(request.Stdout, request.Stderr, nil))
	return result, err
}

type cleaningPluginStep struct{ *pluginStep }

func (s *cleaningPluginStep) Cleanup(ctx context.Context, result step.Result) error {
	wireResult := map[string]any{"outputs": result.Outputs, "variables": result.Variables}
	return s.plugin.client.call(ctx, "step.cleanup", map[string]any{"type": s.name, "with": s.raw, "result": wireResult}, &struct{}{}, nil)
}

func stepContext(r step.Request) map[string]any {
	return map[string]any{"step_id": r.StepID, "workflow_name": r.WorkflowName, "workflow_dir": r.WorkflowDir, "run_dir": r.RunDir, "vars": r.Vars, "inputs": r.Inputs, "env": r.Env, "steps": r.Steps, "dependencies": r.Dependencies, "attempt": r.Attempt, "max_attempts": r.MaxAttempts, "operation_id": r.OperationID}
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
