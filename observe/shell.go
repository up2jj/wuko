package observe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/up2jj/wuko/process"
)

const (
	defaultShellEvery   = 5 * time.Second
	defaultShellTimeout = 30 * time.Second
	maxShellOutput      = 1 << 20
)

// ShellBuilder creates polling shell sources. Executor may be supplied by tests or embedders;
// observation always runs on the host, like the filesystem source, never inside an executor block.
type ShellBuilder struct {
	Executor process.Executor
}

func (ShellBuilder) Type() string { return "shell" }

type shellConfig struct {
	Command          string            `yaml:"command"`
	Args             []string          `yaml:"args,omitempty"`
	WorkingDirectory string            `yaml:"working_directory,omitempty"`
	Env              map[string]string `yaml:"env,omitempty"`
	Every            string            `yaml:"every,omitempty"`
	Timeout          string            `yaml:"timeout,omitempty"`
	Trigger          string            `yaml:"trigger,omitempty"`
	Output           string            `yaml:"output,omitempty"`
}

type normalizedShellConfig struct {
	command   string
	args      []string
	directory string
	env       map[string]string
	every     time.Duration
	timeout   time.Duration
	trigger   string
	output    string
}

func (builder ShellBuilder) Validate(raw map[string]any) error {
	_, err := builder.normalize(raw, false)
	return err
}

func (builder ShellBuilder) Open(ctx context.Context, request OpenRequest) (Source, error) {
	config, err := builder.normalize(request.Config, true)
	if err != nil {
		return nil, err
	}
	directory, err := resolveShellDirectory(request.RunDir, config.directory)
	if err != nil {
		return nil, err
	}
	config.directory = directory
	executor := builder.Executor
	if executor == nil {
		executor = process.LocalExecutor{}
	}
	source := &shellSource{config: config, executor: executor, env: shellEnvironment(request.Env, config.env)}
	initial, err := source.poll(ctx)
	if err != nil {
		return nil, err
	}
	source.initial = cloneMap(initial)
	source.last = shellFingerprint(initial)
	return source, nil
}

func (ShellBuilder) normalize(raw map[string]any, resolved bool) (normalizedShellConfig, error) {
	declared := shellConfig{Every: defaultShellEvery.String(), Timeout: defaultShellTimeout.String(), Trigger: "change", Output: "text"}
	if err := decodeConfig(raw, &declared); err != nil {
		return normalizedShellConfig{}, err
	}
	if strings.TrimSpace(declared.Command) == "" {
		return normalizedShellConfig{}, fmt.Errorf("command is required")
	}
	every := defaultShellEvery
	if resolved || !templated(declared.Every) {
		var err error
		every, err = time.ParseDuration(declared.Every)
		if err != nil || every <= 0 {
			return normalizedShellConfig{}, fmt.Errorf("every must be a positive duration")
		}
	}
	timeout := defaultShellTimeout
	if resolved || !templated(declared.Timeout) {
		var err error
		timeout, err = time.ParseDuration(declared.Timeout)
		if err != nil || timeout <= 0 {
			return normalizedShellConfig{}, fmt.Errorf("timeout must be a positive duration")
		}
	}
	if (resolved || !templated(declared.Trigger)) && declared.Trigger != "change" && declared.Trigger != "always" {
		return normalizedShellConfig{}, fmt.Errorf("trigger must be change or always")
	}
	if (resolved || !templated(declared.Output)) && declared.Output != "text" && declared.Output != "json" {
		return normalizedShellConfig{}, fmt.Errorf("output must be text or json")
	}
	return normalizedShellConfig{
		command: declared.Command, args: declared.Args, directory: declared.WorkingDirectory,
		env: declared.Env, every: every, timeout: timeout, trigger: declared.Trigger, output: declared.Output,
	}, nil
}

// resolveShellDirectory keeps relative working directories anchored to the run directory,
// matching how the filesystem source resolves its watch root.
func resolveShellDirectory(runDir, directory string) (string, error) {
	if filepath.IsAbs(directory) {
		return filepath.Clean(directory), nil
	}
	if runDir == "" {
		var err error
		runDir, err = os.Getwd()
		if err != nil {
			return "", fmt.Errorf("finding run directory: %w", err)
		}
	}
	resolved, err := filepath.Abs(filepath.Join(runDir, directory))
	if err != nil {
		return "", fmt.Errorf("resolving working directory %s: %w", directory, err)
	}
	return filepath.Clean(resolved), nil
}

// shellEnvironment overlays the declared source environment on the workflow environment.
// The host environment stands in when a caller opens a source without one, because an empty
// map would otherwise run the command with no PATH at all.
func shellEnvironment(workflowEnv, declared map[string]string) map[string]string {
	base := workflowEnv
	if len(base) == 0 {
		base = hostEnvironment()
	}
	merged := make(map[string]string, len(base)+len(declared))
	maps.Copy(merged, base)
	maps.Copy(merged, declared)
	return merged
}

func hostEnvironment() map[string]string {
	values := os.Environ()
	environment := make(map[string]string, len(values))
	for _, value := range values {
		key, item, found := strings.Cut(value, "=")
		if found {
			environment[key] = item
		}
	}
	return environment
}

type shellSource struct {
	config   normalizedShellConfig
	executor process.Executor
	env      map[string]string
	initial  map[string]any
	last     map[string]any
}

func (source *shellSource) Initial() any { return cloneMap(source.initial) }

func (source *shellSource) Next(ctx context.Context) (any, error) {
	for {
		timer := time.NewTimer(source.config.every)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return nil, ctx.Err()
		case <-timer.C:
		}
		observation, err := source.poll(ctx)
		if err != nil {
			return nil, err
		}
		fingerprint := shellFingerprint(observation)
		changed := !reflect.DeepEqual(source.last, fingerprint)
		source.last = fingerprint
		if source.config.trigger == "always" || changed {
			return observation, nil
		}
	}
}

// poll runs the command once. A non-zero exit is an observation, not a source failure: watching
// an exit code is a reason to run the command. Failing to run it at all is fatal.
func (source *shellSource) poll(ctx context.Context) (map[string]any, error) {
	pollCtx, cancel := context.WithTimeout(ctx, source.config.timeout)
	defer cancel()
	result, err := source.executor.Run(pollCtx, process.Options{
		Command: source.config.command, Args: source.config.args,
		Dir: source.config.directory, Env: source.env,
		CaptureLimit: maxShellOutput,
		StdoutPolicy: process.OutputCapture, StderrPolicy: process.OutputCapture,
	})
	if err != nil {
		var exitErr *process.ExitError
		if !errors.As(err, &exitErr) {
			if ctx.Err() == nil && errors.Is(err, context.DeadlineExceeded) {
				return nil, fmt.Errorf("shell observation command timed out after %s", source.config.timeout)
			}
			return nil, fmt.Errorf("running shell observation command: %w", err)
		}
	}
	observation := map[string]any{
		"command":   source.config.command,
		"stdout":    result.Stdout,
		"stderr":    result.Stderr,
		"exit_code": result.ExitCode,
		"truncated": result.StdoutTruncated || result.StderrTruncated,
	}
	value, valueErr := shellValue(source.config.output, result.Stdout)
	if valueErr != nil {
		// A command that failed is allowed to write something other than the declared
		// output; its exit status is the observation. Malformed output from a command
		// that succeeded is a genuine source failure.
		if result.ExitCode == 0 {
			return nil, valueErr
		}
		value = nil
	}
	observation["value"] = value
	if result.ExitCode != 0 {
		observation["error"] = fmt.Sprintf("command exited with status %d", result.ExitCode)
	}
	return observation, nil
}

func shellValue(output, stdout string) (any, error) {
	if output != "json" {
		return strings.TrimSpace(stdout), nil
	}
	var value any
	if err := json.Unmarshal([]byte(stdout), &value); err != nil {
		return nil, fmt.Errorf("decoding shell observation JSON: %w", err)
	}
	return value, nil
}

func (*shellSource) NewBatch() Batch { return &latestBatch{root: "shell"} }

func (source *shellSource) Metadata() map[string]any {
	args := make([]any, len(source.config.args))
	for index, value := range source.config.args {
		args[index] = value
	}
	return map[string]any{
		"command": source.config.command, "args": args, "working_directory": source.config.directory,
		"every": source.config.every.String(), "timeout": source.config.timeout.String(),
		"trigger": source.config.trigger, "output": source.config.output,
	}
}

func (*shellSource) Close() error { return nil }

// shellFingerprint decides what "changed" means: what the command printed and how it exited.
// Standard error is captured for the binding but stays out, the way HTTP response headers do,
// so a command that annotates every run does not retrigger the body forever.
func shellFingerprint(observation map[string]any) map[string]any {
	fingerprint := make(map[string]any, 2)
	for _, key := range []string{"stdout", "exit_code"} {
		if value, ok := observation[key]; ok {
			fingerprint[key] = cloneValue(value)
		}
	}
	return fingerprint
}
