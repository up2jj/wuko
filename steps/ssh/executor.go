// Package ssh runs workflow commands and filesystem operations through system OpenSSH.
package ssh

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/up2jj/wuko/executor"
	"github.com/up2jj/wuko/process"
	"github.com/up2jj/wuko/step"
)

type ExecutorConfig struct {
	Host           string            `yaml:"host"`
	Directory      string            `yaml:"directory"`
	User           string            `yaml:"user,omitempty"`
	Port           int               `yaml:"port,omitempty"`
	IdentityFile   string            `yaml:"identity_file,omitempty"`
	ConfigFile     string            `yaml:"config_file,omitempty"`
	ConnectTimeout string            `yaml:"connect_timeout,omitempty"`
	Env            map[string]string `yaml:"env,omitempty"`
	ForwardEnv     []string          `yaml:"forward_env,omitempty"`
}

type commandRunner func(context.Context, process.Options) (process.Result, error)

type ExecutorProvider struct {
	config ExecutorConfig
	run    commandRunner
}

func RegisterExecutor(registry *executor.Registry) error {
	return registry.Register("ssh", NewExecutor)
}

var environmentName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func NewExecutor(raw map[string]any) (executor.Provider, error) {
	var config ExecutorConfig
	if err := step.DecodeConfig(raw, &config); err != nil {
		return nil, err
	}
	if err := validateConfig(config); err != nil {
		return nil, err
	}
	return &ExecutorProvider{config: config, run: process.LocalExecutor{}.Run}, nil
}

// Configuration is built once before templates are rendered and again on entry.
func templated(value string) bool { return strings.Contains(value, "{{") }

func validateConfig(config ExecutorConfig) error {
	if !templated(config.Host) && (strings.TrimSpace(config.Host) == "" || strings.HasPrefix(config.Host, "-") || strings.ContainsAny(config.Host, " \t\r\n\x00")) {
		return fmt.Errorf("host must be a hostname, SSH alias, or user@host")
	}
	if !templated(config.Directory) && (!path.IsAbs(config.Directory) || strings.ContainsRune(config.Directory, 0)) {
		return fmt.Errorf("directory must be an absolute remote path")
	}
	if config.Port < 0 || config.Port > 65535 {
		return fmt.Errorf("port must be between 1 and 65535")
	}
	if strings.ContainsAny(config.User, "\x00\r\n") || strings.HasPrefix(config.User, "-") {
		return fmt.Errorf("invalid SSH user")
	}
	for _, name := range append(slices.Clone(config.ForwardEnv), slices.Collect(maps.Keys(config.Env))...) {
		if !environmentName.MatchString(name) {
			return fmt.Errorf("invalid environment variable name %q", name)
		}
	}
	for _, value := range config.Env {
		if strings.ContainsRune(value, 0) {
			return fmt.Errorf("environment values cannot contain NUL")
		}
	}
	if !templated(config.ConnectTimeout) {
		if _, err := connectionTimeout(config); err != nil {
			return err
		}
	}
	return nil
}

func connectionTimeout(config ExecutorConfig) (time.Duration, error) {
	if config.ConnectTimeout == "" {
		return 10 * time.Second, nil
	}
	duration, err := time.ParseDuration(config.ConnectTimeout)
	if err != nil || duration <= 0 {
		return 0, fmt.Errorf("connect_timeout must be a positive duration")
	}
	return duration, nil
}

func (*ExecutorProvider) SupportsFileSystem() bool { return true }

func (*ExecutorProvider) ValidateStep(kind string, raw map[string]any) error {
	if kind != "shell" && kind != "file" && kind != "edit" {
		return fmt.Errorf("step type %q is not supported by the SSH executor", kind)
	}
	// Expression bindings are unresolved during validation, and nothing re-checks these
	// restrictions at run time, so the restricted fields must be static.
	for _, key := range []string{"tty", "interact", "user"} {
		if !step.ConfigKnown(raw, key) {
			return fmt.Errorf("%s must be static under the SSH executor", key)
		}
	}
	for _, key := range []string{"tty", "interact"} {
		if enabled, _ := raw[key].(bool); enabled {
			return fmt.Errorf("SSH executor does not support %s", key)
		}
	}
	if _, ok := raw["interactions"]; ok {
		return fmt.Errorf("SSH executor does not support interactions")
	}
	if user, _ := raw["user"].(string); user != "" {
		return fmt.Errorf("SSH executor does not support step-level user switching; configure executor user")
	}
	return nil
}

type session struct {
	mu         sync.Mutex
	config     ExecutorConfig
	request    executor.Request
	run        commandRunner
	ssh        string
	localDir   string
	socket     string
	remoteDir  string
	args       []string
	masterDone chan struct{}
	masterErr  error // published by closing masterDone
	stopMaster context.CancelFunc
	closed     bool
}

func (provider *ExecutorProvider) Open(ctx context.Context, request executor.Request) (_ executor.Session, err error) {
	if err := validateConfig(provider.config); err != nil {
		return nil, err
	}
	timeout, err := connectionTimeout(provider.config)
	if err != nil {
		return nil, err
	}
	binary, err := exec.LookPath("ssh")
	if err != nil {
		return nil, fmt.Errorf("finding system OpenSSH: %w", err)
	}
	// Unix control sockets have a small path limit, particularly on macOS.
	directory, err := os.MkdirTemp("/tmp", "wuko-ssh-")
	if err != nil {
		return nil, fmt.Errorf("creating SSH socket directory: %w", err)
	}
	s := &session{config: provider.config, request: request, run: provider.run, ssh: binary,
		localDir: directory, socket: filepath.Join(directory, "s"), masterDone: make(chan struct{})}
	defer func() {
		if err != nil {
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			err = errors.Join(err, s.Close(cleanup))
		}
	}()
	s.args = s.connectionArgs(timeout)
	masterCtx, stop := context.WithCancel(context.WithoutCancel(ctx))
	s.stopMaster = stop
	go func() {
		result, runErr := s.local(masterCtx, append(slices.Clone(s.args), "-M", "-N", "-o", "ControlMaster=yes", "--", s.config.Host), process.Options{
			StdoutPolicy: process.OutputDiscard, StderrPolicy: process.OutputTee, Stderr: request.Stderr, CaptureLimit: 64 << 10,
		})
		if runErr != nil {
			s.masterErr = fmt.Errorf("%s: %w", strings.TrimSpace(result.Stderr), runErr)
		}
		close(s.masterDone)
	}()
	opening, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
ready:
	for {
		// The master creates its socket only once it is listening, so a stat (not an
		// ssh process per tick) waits out a slow connection.
		if _, statErr := os.Stat(s.socket); statErr == nil {
			if _, checkErr := s.control(opening, "check"); checkErr == nil {
				break ready
			}
		}
		select {
		case <-opening.Done():
			return nil, fmt.Errorf("opening SSH connection: %w", opening.Err())
		case <-s.masterDone:
			if s.masterErr != nil {
				return nil, fmt.Errorf("SSH master exited before connecting: %w", s.masterErr)
			}
			return nil, fmt.Errorf("SSH master exited before connecting")
		case <-ticker.C:
		}
	}
	result, err := s.internal(opening, initializeScript, s.config.Directory)
	if err != nil {
		return nil, fmt.Errorf("initializing SSH target: %s: %w", strings.TrimSpace(result.Stderr), err)
	}
	s.remoteDir = strings.TrimSuffix(result.Stdout, "\n")
	if !strings.HasPrefix(s.remoteDir, "/tmp/wuko-ssh.") || strings.ContainsAny(s.remoteDir, "\r\n\x00") || path.Base(s.remoteDir) == "." || path.Clean(s.remoteDir) != s.remoteDir {
		s.remoteDir = ""
		return nil, fmt.Errorf("unexpected SSH initialization response; remote shell startup must not print to stdout")
	}
	return s, nil
}

func (s *session) connectionArgs(timeout time.Duration) []string {
	seconds := int64(timeout / time.Second)
	if timeout%time.Second != 0 {
		seconds++
	}
	args := []string{"-T", "-S", s.socket, "-o", "BatchMode=yes", "-o", "ControlPersist=no", "-o", "ConnectTimeout=" + strconv.FormatInt(seconds, 10),
		"-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=2", "-o", "RemoteCommand=none", "-o", "ClearAllForwardings=yes",
		"-o", "ForkAfterAuthentication=no", "-o", "StdinNull=no"}
	if s.config.User != "" {
		args = append(args, "-l", s.config.User)
	}
	if s.config.Port != 0 {
		args = append(args, "-p", strconv.Itoa(s.config.Port))
	}
	for _, file := range []struct{ flag, value string }{{"-i", s.config.IdentityFile}, {"-F", s.config.ConfigFile}} {
		if file.value != "" {
			value := file.value
			if !filepath.IsAbs(value) {
				value = filepath.Join(s.request.RunDir, value)
			}
			args = append(args, file.flag, value)
		}
	}
	return args
}

func (s *session) local(ctx context.Context, args []string, options process.Options) (process.Result, error) {
	options.Command, options.Args = s.ssh, args
	options.Dir, options.Env, options.User = s.request.RunDir, s.request.Env, ""
	return s.run(ctx, options)
}

func (s *session) control(ctx context.Context, operation string) (process.Result, error) {
	return s.local(ctx, append(slices.Clone(s.args), "-O", operation, "--", s.config.Host), process.Options{
		StdoutPolicy: process.OutputCapture, StderrPolicy: process.OutputCapture, CaptureLimit: 64 << 10,
	})
}

// quote protects arguments from the remote login shell as well as the helper shell.
func quote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'" }

func (s *session) script(ctx context.Context, script string, args []string, options process.Options) (process.Result, error) {
	command := "exec sh -c " + quote(script) + " wuko"
	for _, arg := range args {
		if strings.ContainsRune(arg, 0) {
			return process.Result{}, fmt.Errorf("SSH command arguments cannot contain NUL")
		}
		command += " " + quote(arg)
	}
	// A slave must fail if its owned master is gone, rather than silently reconnecting.
	arguments := append(slices.Clone(s.args), "-o", "SessionType=default", "-o", "ControlMaster=no", "-o", "ProxyJump=none", "-o", "ProxyCommand=false", "--", s.config.Host, command)
	result, err := s.local(ctx, arguments, options)
	if err != nil {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		// Do not expose the command string (which may contain explicit environment secrets).
		return result, fmt.Errorf("SSH transport or remote helper failed (status %d)", result.ExitCode)
	}
	return result, nil
}

func (s *session) internal(ctx context.Context, script string, args ...string) (process.Result, error) {
	return s.script(ctx, script, args, process.Options{StdoutPolicy: process.OutputCapture, StderrPolicy: process.OutputCapture, CaptureLimit: 64 << 10})
}

func (s *session) mapPath(value string) string {
	if value == "" {
		return path.Clean(s.config.Directory)
	}
	if !filepath.IsAbs(value) {
		return path.Join(s.config.Directory, filepath.ToSlash(value))
	}
	relative, err := filepath.Rel(s.request.RunDir, filepath.Clean(value))
	if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return path.Join(s.config.Directory, filepath.ToSlash(relative))
	}
	return filepath.ToSlash(filepath.Clean(value))
}

// environment renders the command's overrides as a script sourced on the remote side.
// It travels over stdin so values never appear in a local or remote process's argv.
func (s *session) environment(values map[string]string) (string, error) {
	if values == nil {
		values = s.request.Env
	}
	env := make(map[string]string)
	for _, name := range s.config.ForwardEnv {
		if value, exists := values[name]; exists {
			env[name] = value
		}
	}
	maps.Copy(env, s.config.Env)
	var script strings.Builder
	for _, name := range slices.Sorted(maps.Keys(env)) {
		if strings.ContainsRune(env[name], 0) {
			return "", fmt.Errorf("environment variable %s cannot contain NUL", name)
		}
		script.WriteString("export " + name + "=" + quote(env[name]) + "\n")
	}
	return script.String(), nil
}

func (s *session) checkOpen() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return fmt.Errorf("SSH executor is closed")
	}
	return nil
}

// Run holds no lock while the command runs, so Close can stop it and filesystem
// operations proceed over their own multiplexed sessions.
func (s *session) Run(ctx context.Context, options process.Options) (process.Result, error) {
	if err := s.checkOpen(); err != nil {
		return process.Result{}, err
	}
	if options.TTY || options.Interactions != nil || options.Interact || options.RemoteTerminal != nil || options.User != "" || options.StdinOutlivesProcess {
		return process.Result{}, fmt.Errorf("SSH executor supports unattended commands without user switching or persistent stdin")
	}
	if options.Command == "" {
		return process.Result{}, fmt.Errorf("command is required")
	}
	if err := ctx.Err(); err != nil {
		return process.Result{}, err
	}
	env, err := s.environment(options.Env)
	if err != nil {
		return process.Result{}, err
	}
	runDir := path.Join(s.remoteDir, rand.Text())
	if _, err := s.script(ctx, `set -e; umask 077; mkdir -- "$1"; cat > "$1/env"`, []string{runDir}, process.Options{
		Stdin: strings.NewReader(env), StdoutPolicy: process.OutputDiscard, StderrPolicy: process.OutputCapture, CaptureLimit: 64 << 10,
	}); err != nil {
		return process.Result{}, err
	}
	args := []string{runDir, s.mapPath(options.Dir), groupScript, stopScript}
	args = append(args, options.Command)
	args = append(args, options.Args...)
	type outcome struct {
		result process.Result
		err    error
	}
	// Keep the transport alive long enough to stop the remote group on cancellation.
	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	defer cancel()
	done := make(chan outcome, 1)
	go func() {
		result, err := s.script(runCtx, runScript, args, options)
		done <- outcome{result, err}
	}()
	var completed outcome
	select {
	case completed = <-done:
	case <-ctx.Done():
		cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		_, stopErr := s.internal(cleanup, stopScript, runDir, stopGrace)
		stop()
		cancel()
		completed = <-done
		if stopErr != nil {
			return completed.result, errors.Join(ctx.Err(), fmt.Errorf("remote termination could not be confirmed: %w", stopErr))
		}
		return completed.result, ctx.Err()
	}
	cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer stop()
	if completed.err != nil {
		_, stopErr := s.internal(cleanup, stopScript, runDir, stopGrace)
		if stopErr != nil {
			return completed.result, errors.Join(completed.err, fmt.Errorf("remote termination could not be confirmed: %w", stopErr))
		}
		return completed.result, completed.err
	}
	// One round trip both reads the completion status and releases the run's resources.
	status, err := s.internal(cleanup, `set -e; cat -- "$1/status"; rm -rf -- "$1"`, runDir)
	if err != nil {
		return completed.result, fmt.Errorf("reading remote command completion: %w", err)
	}
	code, err := strconv.Atoi(strings.TrimSpace(status.Stdout))
	if err != nil || code < 0 || code > 255 {
		return completed.result, fmt.Errorf("invalid remote command completion status")
	}
	completed.result.ExitCode = code
	if code != 0 {
		return completed.result, &process.ExitError{Command: options.Command, Code: code}
	}
	return completed.result, nil
}

func (s *session) Close(ctx context.Context) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()
	var errs []error
	if s.remoteDir != "" {
		if _, err := s.internal(ctx, closeScript, s.remoteDir, stopScript); err != nil {
			errs = append(errs, fmt.Errorf("cleaning SSH target resources; remote termination could not be confirmed: %w", err))
		}
	}
	if s.stopMaster != nil {
		// The master is our foreground child, never the user's shared SSH connection.
		select {
		case <-s.masterDone:
		default:
			_, _ = s.control(ctx, "exit")
		}
		s.stopMaster()
		<-s.masterDone
	}
	if err := os.RemoveAll(s.localDir); err != nil {
		errs = append(errs, fmt.Errorf("removing SSH socket directory: %w", err))
	}
	return errors.Join(errs...)
}

var _ executor.StepValidator = (*ExecutorProvider)(nil)
var _ executor.Session = (*session)(nil)
