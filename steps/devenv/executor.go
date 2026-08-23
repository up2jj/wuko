// Package devenv implements a devenv-backed workflow executor.
package devenv

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/Masterminds/semver/v3"
	"github.com/up2jj/wuko/executor"
	"github.com/up2jj/wuko/process"
	"github.com/up2jj/wuko/step"
)

const (
	minimumVersion        = "2.2.0"
	processCleanupTimeout = 5 * time.Second
)

type SecretsConfig struct {
	Mode     string `yaml:"mode,omitempty"`
	Profile  string `yaml:"profile,omitempty"`
	Provider string `yaml:"provider,omitempty"`
}

type ExecutorConfig struct {
	Directory string         `yaml:"directory,omitempty"`
	Profiles  []string       `yaml:"profiles,omitempty"`
	Processes []string       `yaml:"processes,omitempty"`
	Secrets   *SecretsConfig `yaml:"secrets,omitempty"`
}

type commandRunner func(context.Context, process.Options) (process.Result, error)

type ExecutorProvider struct {
	config       ExecutorConfig
	command      commandRunner
	processState processStateReader
}

func RegisterExecutor(registry *executor.Registry) error {
	return registry.Register("devenv", NewExecutor)
}

func NewExecutor(raw map[string]any) (executor.Provider, error) {
	var config ExecutorConfig
	if err := step.DecodeConfig(raw, &config); err != nil {
		return nil, err
	}
	if err := validateConfig(config); err != nil {
		return nil, err
	}
	return &ExecutorProvider{config: config, command: localCommand, processState: cliProcessStateReader{command: localCommand, profiles: config.Profiles}}, nil
}

func validateConfig(config ExecutorConfig) error {
	if config.Directory != "" && strings.TrimSpace(config.Directory) == "" {
		return fmt.Errorf("directory cannot be blank")
	}
	for i, profile := range config.Profiles {
		if strings.TrimSpace(profile) == "" || strings.HasPrefix(profile, "-") {
			return fmt.Errorf("profile %d must be a non-empty name", i+1)
		}
	}
	seen := make(map[string]struct{}, len(config.Processes))
	for i, name := range config.Processes {
		if strings.TrimSpace(name) == "" || strings.HasPrefix(name, "-") {
			return fmt.Errorf("process %d must be a non-empty name", i+1)
		}
		if _, ok := seen[name]; ok {
			return fmt.Errorf("process %q is configured more than once", name)
		}
		seen[name] = struct{}{}
	}
	if config.Secrets != nil {
		mode := config.Secrets.Mode
		if mode == "" {
			mode = "auto"
		}
		switch mode {
		case "auto", "runtime", "inherit", "disabled":
		default:
			return fmt.Errorf("secrets.mode must be auto, runtime, inherit, or disabled")
		}
	}
	return nil
}

func localCommand(ctx context.Context, options process.Options) (process.Result, error) {
	return process.LocalExecutor{}.Run(ctx, options)
}

type session struct {
	config       ExecutorConfig
	request      executor.Request
	command      commandRunner
	root         string
	active       bool
	secretMode   string
	owned        []string
	closed       bool
	processState processStateReader
}

type activeEnvironment struct {
	root    string
	profile string
	runtime string
	dotfile string
	state   string
}

func (provider *ExecutorProvider) Open(ctx context.Context, request executor.Request) (executor.Session, error) {
	if err := validateConfig(provider.config); err != nil {
		return nil, err
	}
	root, err := resolveRoot(request.RunDir, provider.config.Directory)
	if err != nil {
		return nil, err
	}
	activeEnv, active, err := detectActiveEnvironment(request.Env)
	if err != nil {
		return nil, err
	}
	if active && activeEnv.root != root {
		return nil, fmt.Errorf("active devenv root %q does not match requested directory %q", activeEnv.root, root)
	}
	if err := checkVersion(ctx, provider.command, root, request.Env, provider.config.Profiles); err != nil {
		return nil, err
	}
	if active && len(provider.config.Profiles) > 0 {
		if err := verifyActiveProfile(ctx, provider.command, root, request.Env, provider.config.Profiles); err != nil {
			return nil, err
		}
	}
	secretMode, err := resolveSecretMode(root, request.Env, provider.config.Secrets)
	if err != nil {
		return nil, err
	}
	s := &session{
		config: provider.config, request: request, command: provider.command,
		root: root, active: active, secretMode: secretMode, processState: provider.processState,
	}
	if err := s.ensureProcesses(ctx); err != nil {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), processCleanupTimeout)
		defer cancel()
		return nil, errors.Join(err, s.Close(cleanupCtx))
	}
	return s, nil
}

func detectActiveEnvironment(environment map[string]string) (activeEnvironment, bool, error) {
	active := activeEnvironment{
		root:    environment["DEVENV_ROOT"],
		profile: environment["DEVENV_PROFILE"],
		runtime: environment["DEVENV_RUNTIME"],
		dotfile: environment["DEVENV_DOTFILE"],
		state:   environment["DEVENV_STATE"],
	}
	if active.root == "" && active.profile == "" && active.runtime == "" && active.dotfile == "" && active.state == "" {
		return active, false, nil
	}
	if active.root == "" {
		return active, false, fmt.Errorf("active devenv environment is incomplete: DEVENV_ROOT is missing")
	}
	root, err := canonicalPath(active.root)
	if err != nil {
		return active, false, fmt.Errorf("resolving active DEVENV_ROOT: %w", err)
	}
	active.root = root
	if active.dotfile != "" {
		resolved, err := canonicalPath(active.dotfile)
		if err != nil {
			return active, false, fmt.Errorf("resolving active DEVENV_DOTFILE: %w", err)
		}
		if !pathWithin(root, resolved) {
			return active, false, fmt.Errorf("active devenv dotfile %q does not belong to root %q", resolved, root)
		}
		active.dotfile = resolved
	}
	if active.state != "" {
		resolved, err := canonicalPath(active.state)
		if err != nil {
			return active, false, fmt.Errorf("resolving active DEVENV_STATE: %w", err)
		}
		expected := filepath.Join(active.dotfile, "state")
		if active.dotfile != "" && resolved != expected {
			return active, false, fmt.Errorf("active devenv state %q does not belong to dotfile %q", resolved, active.dotfile)
		}
		active.state = resolved
	}
	return active, true, nil
}

func pathWithin(root, value string) bool {
	relative, err := filepath.Rel(root, value)
	if err != nil {
		return false
	}
	return relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)))
}

func resolveRoot(runDir, configured string) (string, error) {
	root := configured
	if root == "" {
		root = runDir
	}
	if !filepath.IsAbs(root) {
		root = filepath.Join(runDir, root)
	}
	return canonicalPath(root)
}

func canonicalPath(value string) (string, error) {
	abs, err := filepath.Abs(value)
	if err != nil {
		return "", err
	}
	parts := make([]string, 0, 4)
	current := abs
	for {
		if resolved, err := filepath.EvalSymlinks(current); err == nil {
			for i := len(parts) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, parts[i])
			}
			return filepath.Clean(resolved), nil
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		parts = append(parts, filepath.Base(current))
		current = parent
	}
	return filepath.Clean(abs), nil
}

func (s *session) Run(ctx context.Context, options process.Options) (process.Result, error) {
	if s.closed {
		return process.Result{}, fmt.Errorf("devenv executor session is closed")
	}
	if options.Command == "" {
		return process.Result{}, fmt.Errorf("command is required")
	}
	options = s.wrapOptions(options)
	return s.command(ctx, options)
}

func (s *session) RunTask(ctx context.Context, request executor.TaskRequest) (process.Result, error) {
	if strings.TrimSpace(request.Name) == "" {
		return process.Result{}, fmt.Errorf("task name is required")
	}
	mode := request.Mode
	if mode == "" {
		mode = "before"
	}
	if mode != "single" && mode != "before" && mode != "after" && mode != "all" {
		return process.Result{}, fmt.Errorf("task mode %q is invalid", mode)
	}
	args := []string{"tasks", "run", request.Name, "--mode", mode}
	if len(request.Inputs) > 0 {
		encoded, err := json.Marshal(request.Inputs)
		if err != nil {
			return process.Result{}, fmt.Errorf("encoding task inputs: %w", err)
		}
		args = append(args, "--input-json", string(encoded))
	}
	return s.Run(ctx, process.Options{
		Command: "devenv", Args: args, Dir: s.root, Env: s.request.Env,
		Stdin: request.Stdin, Stdout: request.Stdout, Stderr: request.Stderr,
		CaptureLimit: request.CaptureLimit,
	})
}

func (s *session) wrapOptions(options process.Options) process.Options {
	environment := maps.Clone(s.request.Env)
	if environment == nil {
		environment = make(map[string]string)
	}
	maps.Copy(environment, options.Env)
	options.Env = environment
	if options.Dir == "" {
		options.Dir = s.root
	}
	if s.secretMode != "disabled" {
		applySecretOverrides(options.Env, s.config.Secrets)
	}
	secretArgs := []string(nil)
	if s.secretMode == "runtime" {
		secretArgs = []string{"secretspec", "run", "--"}
	}
	commandArgs := append(secretArgs, options.Command)
	commandArgs = append(commandArgs, options.Args...)
	if s.active {
		options.Command = commandArgs[0]
		options.Args = commandArgs[1:]
		return options
	}
	devenvArgs := make([]string, 0, len(s.config.Profiles)*2+3+len(commandArgs))
	for _, profile := range s.config.Profiles {
		devenvArgs = append(devenvArgs, "--profile", profile)
	}
	devenvArgs = append(devenvArgs, "shell", "--")
	devenvArgs = append(devenvArgs, commandArgs...)
	options.Command = "devenv"
	options.Args = devenvArgs
	options.Dir = s.root
	return options
}

func applySecretOverrides(environment map[string]string, config *SecretsConfig) {
	if config == nil {
		return
	}
	if config.Profile != "" {
		environment["SECRETSPEC_PROFILE"] = config.Profile
	}
	if config.Provider != "" {
		environment["SECRETSPEC_PROVIDER"] = config.Provider
	}
}

func resolveSecretMode(root string, environment map[string]string, config *SecretsConfig) (string, error) {
	mode := "auto"
	if config != nil && config.Mode != "" {
		mode = config.Mode
	}
	if mode == "auto" {
		if _, err := os.Stat(filepath.Join(root, "secretspec.toml")); err == nil || environment["SECRETSPEC_PROFILE"] != "" || environment["SECRETSPEC_PROVIDER"] != "" {
			return "runtime", nil
		}
		return "disabled", nil
	}
	if mode == "runtime" {
		if _, err := os.Stat(filepath.Join(root, "secretspec.toml")); err != nil && environment["SECRETSPEC_PROFILE"] == "" && environment["SECRETSPEC_PROVIDER"] == "" {
			return "", fmt.Errorf("runtime SecretSpec mode requires secretspec.toml or an active SecretSpec configuration")
		}
	}
	return mode, nil
}

func (s *session) Close(ctx context.Context) error {
	if s.closed {
		return nil
	}
	s.closed = true
	var errs []error
	for _, name := range slices.Backward(s.owned) {
		options := s.processOptions([]string{"processes", "stop", name})
		if _, err := s.command(ctx, options); err != nil {
			if state, statusErr := s.processState.Status(ctx, s.root, []string{name}, s.request.Env); statusErr == nil && state[name] == processStopped {
				continue
			}
			errs = append(errs, fmt.Errorf("stopping devenv process %q: %w", name, err))
		}
	}
	return errors.Join(errs...)
}

func (s *session) processOptions(args []string) process.Options {
	options := process.Options{Command: "devenv", Args: args, Dir: s.root, Env: maps.Clone(s.request.Env), CaptureLimit: 64 * 1024}
	if !s.active {
		prefixed := make([]string, 0, len(s.config.Profiles)*2+len(args))
		for _, profile := range s.config.Profiles {
			prefixed = append(prefixed, "--profile", profile)
		}
		prefixed = append(prefixed, args...)
		options.Args = prefixed
	}
	return options
}

func (s *session) ensureProcesses(ctx context.Context) error {
	if len(s.config.Processes) == 0 {
		return nil
	}
	if s.processState == nil {
		return fmt.Errorf("devenv process state is unavailable; refusing to manage processes")
	}
	state, err := s.processState.Status(ctx, s.root, s.config.Processes, s.request.Env)
	if err != nil {
		return err
	}
	for _, name := range s.config.Processes {
		switch state[name] {
		case processRunning:
			continue
		case processStopped:
			if _, err := s.command(ctx, s.processOptions([]string{"processes", "start", name})); err != nil {
				return fmt.Errorf("starting devenv process %q: %w", name, err)
			}
			s.owned = append(s.owned, name)
		default:
			return fmt.Errorf("cannot establish ownership for devenv process %q", name)
		}
	}
	if _, err := s.command(ctx, s.processOptions([]string{"processes", "wait"})); err != nil {
		return fmt.Errorf("waiting for devenv processes: %w", err)
	}
	return nil
}

func checkVersion(ctx context.Context, run commandRunner, root string, environment map[string]string, profiles []string) error {
	options := process.Options{Command: "devenv", Args: []string{"--version"}, Dir: root, Env: maps.Clone(environment), CaptureLimit: 64 * 1024}
	if len(profiles) > 0 {
		args := make([]string, 0, len(profiles)*2+1)
		for _, profile := range profiles {
			args = append(args, "--profile", profile)
		}
		args = append(args, "--version")
		options.Args = args
	}
	result, err := run(ctx, options)
	if err != nil {
		return fmt.Errorf("checking devenv version: %w", err)
	}
	value := strings.TrimSpace(result.Stdout + " " + result.Stderr)
	value = strings.TrimPrefix(value, "devenv ")
	fields := strings.Fields(value)
	if len(fields) == 0 {
		return fmt.Errorf("checking devenv version: no version reported")
	}
	version, err := semver.NewVersion(strings.TrimPrefix(fields[0], "v"))
	if err != nil {
		return fmt.Errorf("parsing devenv version %q: %w", fields[0], err)
	}
	minimum, _ := semver.NewVersion(minimumVersion)
	if version.LessThan(minimum) {
		return fmt.Errorf("devenv %s is too old; version %s or newer is required", version, minimum)
	}
	return nil
}

func verifyActiveProfile(ctx context.Context, run commandRunner, root string, environment map[string]string, profiles []string) error {
	want := strings.TrimSpace(environment["DEVENV_PROFILE"])
	if want == "" {
		return fmt.Errorf("cannot verify explicit profiles in active devenv: DEVENV_PROFILE is missing")
	}
	args := make([]string, 0, len(profiles)*2+1)
	for _, profile := range profiles {
		args = append(args, "--profile", profile)
	}
	args = append(args, "info")
	result, err := run(ctx, process.Options{Command: "devenv", Args: args, Dir: root, Env: maps.Clone(environment), CaptureLimit: 64 * 1024})
	if err != nil {
		return fmt.Errorf("verifying active devenv profiles: %w", err)
	}
	got := environmentValue(result.Stdout+"\n"+result.Stderr, "DEVENV_PROFILE")
	if got != want {
		return fmt.Errorf("requested devenv profiles do not match the active profile set")
	}
	return nil
}

func environmentValue(output, name string) string {
	prefix := strings.ToLower(name) + ":"
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		line = strings.TrimLeft(line, "-| ")
		if !strings.HasPrefix(strings.ToLower(line), prefix) {
			continue
		}
		return strings.TrimSpace(line[len(prefix):])
	}
	return ""
}

type processState string

const (
	processRunning processState = "running"
	processStopped processState = "stopped"
)

type processStateReader interface {
	Status(context.Context, string, []string, map[string]string) (map[string]processState, error)
}

type processStateReaderFunc func(context.Context, string, []string, map[string]string) (map[string]processState, error)

func (f processStateReaderFunc) Status(ctx context.Context, root string, names []string, environment map[string]string) (map[string]processState, error) {
	return f(ctx, root, names, environment)
}

type cliProcessStateReader struct {
	command  commandRunner
	profiles []string
}

func (reader cliProcessStateReader) Status(ctx context.Context, root string, names []string, environment map[string]string) (map[string]processState, error) {
	state := make(map[string]processState, len(names))
	for _, name := range names {
		args := make([]string, 0, len(reader.profiles)*2+3)
		for _, profile := range reader.profiles {
			args = append(args, "--profile", profile)
		}
		args = append(args, "processes", "status", name)
		result, err := reader.command(ctx, process.Options{Command: "devenv", Args: args, Dir: root, Env: maps.Clone(environment), CaptureLimit: 128 * 1024})
		output := strings.ToLower(result.Stdout + "\n" + result.Stderr)
		if err != nil {
			if strings.Contains(output, "no process manager") || strings.Contains(output, "not running") || strings.Contains(output, "no daemon") {
				state[name] = processStopped
				continue
			}
			return nil, fmt.Errorf("reading devenv process status for %q: %w", name, err)
		}
		// `processes status <name>` is already scoped to one process. The
		// output may be a multi-line record with the state on a separate line.
		line := output
		switch {
		case strings.Contains(line, "stopped"), strings.Contains(line, "exited"), strings.Contains(line, "failed"), strings.Contains(line, "gave-up"), strings.Contains(line, "down"):
			state[name] = processStopped
		case strings.Contains(line, "running"), strings.Contains(line, "ready"), strings.Contains(line, "started"), strings.Contains(line, "starting"), strings.Contains(line, "waiting"), strings.Contains(line, "healthy"), strings.Contains(line, "active"), strings.Contains(line, " up"):
			state[name] = processRunning
		default:
			return nil, fmt.Errorf("devenv process status for %q is ambiguous", name)
		}
	}
	return state, nil
}

var _ executor.Provider = (*ExecutorProvider)(nil)
var _ executor.Session = (*session)(nil)
var _ executor.TaskRunner = (*session)(nil)
