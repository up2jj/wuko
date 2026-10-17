package stepadapter

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"slices"

	"github.com/up2jj/wuko/executor"
	"github.com/up2jj/wuko/plugin/sdk"
	"github.com/up2jj/wuko/process"
	"github.com/up2jj/wuko/step"
)

// Registration describes one native step exposed by a plugin.
type Registration struct {
	Builder step.Builder
	// Cleanup must be true when runners built by Builder implement step.Cleaner and their
	// successful results need cleaning up. As in process, a runner that does not implement it
	// for its configuration has nothing to clean.
	Cleanup bool
	// Outputs is the step's static output schema, as in step.Registration. Nil leaves it
	// undeclared, which the host treats as open.
	Outputs *step.OutputSchema
	// Executor lets the step run inside executor blocks of this plugin's executors registered
	// with RegisterExecutor. The runner then receives the block's native session as
	// Request.Executor, with every capability it has, not only those the protocol carries.
	Executor bool
	// Terminal reports whether a step with the given configuration reads the user's terminal,
	// and whether in raw mode. When it does and the run is interactive, the runner's request is
	// Interactive with a file-backed Stdin carrying the borrowed terminal's input, as it would be
	// in process. The step must be registered WithHostCallbacks(sdk.HostTerminal).
	Terminal func(config map[string]any) (use, raw bool)
}

// Register exposes a Wuko-native step runner through a plugin. Validation, cleanup, output
// streaming, host template rendering, secrets, and helpers are adapted as RegisterService
// adapts them; Registration adds output schemas, executor access, and the user's terminal.
func Register(plugin *sdk.Plugin, name string, registration Registration, options ...sdk.StepOption) error {
	if registration.Builder == nil {
		return fmt.Errorf("step %q requires a builder", name)
	}
	callbacks, err := sdk.DeclaredHostCallbacks(options...)
	if err != nil {
		return fmt.Errorf("step %q: %w", name, err)
	}
	if registration.Terminal != nil && !slices.Contains(callbacks, sdk.HostTerminal) {
		return fmt.Errorf("step %q reads the terminal and must declare sdk.HostTerminal", name)
	}
	secretsReachable := slices.Contains(callbacks, sdk.HostFunctionCall)
	options = slices.Clone(options)
	if registration.Outputs != nil && registration.Outputs.Kind != step.OutputUnknown {
		options = append(options, sdk.WithOutputSchema(sdkOutputSchema(*registration.Outputs)))
	}
	if registration.Executor {
		options = append(options, sdk.WithExecutorAccess())
	}
	build := func(config map[string]any) (step.Runner, error) {
		runner, err := registration.Builder(normalizeMap(config))
		if err != nil {
			return nil, fmt.Errorf("building step %q: %w", name, err)
		}
		return runner, nil
	}
	handler := sdk.StepHandler[map[string]any]{
		Validate: func(ctx context.Context, request sdk.ValidateRequest[map[string]any]) error {
			runner, err := build(request.Config)
			if err != nil {
				return pluginError(err)
			}
			validator, ok := runner.(step.Validator)
			if !ok {
				return nil
			}
			return pluginError(validator.Validate(ctx, stepRequest(ctx, request.Context, request.Host, secretsReachable, nil, nil, nil)))
		},
		Run: func(ctx context.Context, request sdk.RunRequest[map[string]any]) (sdk.Result, error) {
			runner, err := build(request.Config)
			if err != nil {
				return sdk.Result{}, pluginError(err)
			}
			native := stepRequest(ctx, request.Context, request.Host, secretsReachable, request.Stdout, request.Stderr, nil)
			native.Executor = process.LocalExecutor{}
			if request.Executor != nil {
				session, ok := request.Executor.(*nativeSession)
				if !ok {
					return sdk.Result{}, pluginError(fmt.Errorf("step %q runs only in executors registered with stepadapter.RegisterExecutor", name))
				}
				native.Executor, native.InsideExecutor = session.session, true
			}
			if registration.Terminal != nil && request.Context.Interactive {
				if use, raw := registration.Terminal(normalizeMap(request.Config)); use {
					release, err := lendTerminal(ctx, request.Host, raw, &native)
					if err != nil {
						return sdk.Result{}, pluginError(err)
					}
					defer release()
				}
			}
			result, err := runner.Run(ctx, native)
			return sdk.Result{Outputs: result.Outputs, Variables: result.Variables}, pluginError(err)
		},
	}
	if registration.Cleanup {
		handler.Cleanup = func(ctx context.Context, request sdk.CleanupRequest[map[string]any]) error {
			runner, err := build(request.Config)
			if err != nil {
				return pluginError(err)
			}
			cleaner, ok := runner.(step.Cleaner)
			if !ok {
				return nil
			}
			return pluginError(cleaner.Cleanup(ctx, step.Result{
				Outputs: normalizeMap(request.Result.Outputs), Variables: normalizeMap(request.Result.Variables),
			}))
		}
	}
	return sdk.RegisterStep(plugin, name, handler, options...)
}

// lendTerminal borrows the user's terminal for a native runner, which expects a file-backed
// Stdin: the terminal's input is pumped into a pipe whose read end the runner gets. A run that
// cannot lend one proceeds without, exactly as a non-interactive run does in process.
func lendTerminal(ctx context.Context, host sdk.Host, raw bool, request *step.Request) (func(), error) {
	terminal, err := host.Terminal(ctx, sdk.TerminalOptions{Raw: raw})
	if errors.Is(err, sdk.ErrTerminalUnavailable) {
		return func() {}, nil
	}
	if err != nil {
		return nil, err
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		_ = terminal.Release(context.WithoutCancel(ctx))
		return nil, fmt.Errorf("creating terminal input pipe: %w", err)
	}
	pumped := make(chan struct{})
	go func() {
		defer close(pumped)
		_, _ = io.Copy(writer, terminal.Input)
		_ = writer.Close()
	}()
	request.Stdin, request.Interactive = reader, true
	return func() {
		// Releasing ends the host's input, which ends the pump.
		_ = terminal.Release(context.WithoutCancel(ctx))
		<-pumped
		_ = reader.Close()
	}, nil
}

func sdkOutputSchema(schema step.OutputSchema) sdk.OutputSchema {
	switch schema.Kind {
	case step.OutputArray:
		if schema.Items == nil {
			return sdk.Array(sdk.Scalar())
		}
		return sdk.Array(sdkOutputSchema(*schema.Items))
	case step.OutputObject:
		fields := make(map[string]sdk.OutputSchema, len(schema.Fields))
		for name, field := range schema.Fields {
			fields[name] = sdkOutputSchema(field)
		}
		if schema.Open {
			return sdk.OpenObject(fields)
		}
		return sdk.Object(fields)
	case step.OutputScalar:
		return sdk.Scalar()
	default:
		return sdk.OpenObject(nil)
	}
}

// ExecutorRegistration describes one native executor exposed by a plugin.
type ExecutorRegistration struct {
	Builder executor.Builder
	// FileSystem, Stdin, and TTY declare the session capabilities, as sdk.ExecutorHandler does.
	// The protocol declares them per executor type, so a configuration whose sessions cannot
	// use a declared capability fails when it is used rather than when the workflow is validated.
	FileSystem bool
	Stdin      bool
	TTY        bool
	// CancelStopsProcess is the default for sessions that do not implement process.CancelPolicy.
	CancelStopsProcess bool
}

// RegisterExecutor exposes a Wuko-native executor through a plugin. Its sessions keep running in
// the plugin; steps registered with Register and Executor set receive them unchanged.
func RegisterExecutor(plugin *sdk.Plugin, name string, registration ExecutorRegistration) error {
	if registration.Builder == nil {
		return fmt.Errorf("executor %q requires a builder", name)
	}
	build := func(config map[string]any) (executor.Provider, error) {
		provider, err := registration.Builder(normalizeMap(config))
		if err != nil {
			return nil, fmt.Errorf("building executor %q: %w", name, err)
		}
		return provider, nil
	}
	return sdk.RegisterExecutor(plugin, name, sdk.ExecutorHandler[map[string]any]{
		FileSystem: registration.FileSystem, Stdin: registration.Stdin, TTY: registration.TTY,
		CancelStopsProcess: registration.CancelStopsProcess,
		Validate: func(ctx context.Context, request sdk.ExecutorRequest[map[string]any]) error {
			provider, err := build(request.Config)
			if err != nil {
				return pluginError(err)
			}
			if validator, ok := provider.(executor.Validator); ok {
				return pluginError(validator.Validate(ctx, executorRequest(request)))
			}
			return nil
		},
		Open: func(ctx context.Context, request sdk.ExecutorRequest[map[string]any]) (sdk.ExecutorSession, error) {
			provider, err := build(request.Config)
			if err != nil {
				return nil, pluginError(err)
			}
			session, err := provider.Open(ctx, executorRequest(request))
			if err != nil {
				return nil, pluginError(err)
			}
			return &nativeSession{session: session, cancelStops: registration.CancelStopsProcess}, nil
		},
	})
}

func executorRequest(request sdk.ExecutorRequest[map[string]any]) executor.Request {
	return executor.Request{
		WorkflowName: request.Context.WorkflowName, WorkflowDir: request.Context.WorkflowDir,
		RunDir: request.Context.RunDir, Env: request.Context.Env, Stdout: request.Stdout, Stderr: request.Stderr,
	}
}

// nativeSession serves one native executor session over the protocol.
type nativeSession struct {
	session     executor.Session
	cancelStops bool
}

func (s *nativeSession) Run(ctx context.Context, command sdk.Command) (sdk.CommandResult, error) {
	options := process.Options{
		Command: command.Command, Args: command.Args, Dir: command.Dir, Env: command.Env,
		Stdin: command.Input, StdinOutlivesProcess: command.StdinOutlivesProcess,
		Stdout: command.Stdout, Stderr: command.Stderr, Started: command.Started, CaptureLimit: command.CaptureLimit,
		StdoutPolicy: process.OutputPolicy(command.StdoutPolicy), StderrPolicy: process.OutputPolicy(command.StderrPolicy),
	}
	if command.TTY != nil {
		sizes := make(chan process.TerminalSize, 1)
		done := make(chan struct{})
		defer close(done)
		go func() {
			for {
				select {
				case size := <-command.Resize:
					select {
					case sizes <- process.TerminalSize{Rows: size.Rows, Cols: size.Cols}:
					case <-done:
						return
					}
				case <-done:
					return
				}
			}
		}()
		options.TTY = true
		options.RemoteTerminal = &process.RemoteTerminal{Size: process.TerminalSize{Rows: command.TTY.Rows, Cols: command.TTY.Cols}, Resize: sizes}
	}
	result, err := s.session.Run(ctx, options)
	wire := sdk.CommandResult{Stdout: result.Stdout, Stderr: result.Stderr, ExitCode: result.ExitCode, StdoutTruncated: result.StdoutTruncated, StderrTruncated: result.StderrTruncated}
	// The host turns a nonzero exit code back into a process.ExitError, so an exit is a result
	// rather than a failure of the run.
	var exited *process.ExitError
	if errors.As(err, &exited) {
		wire.ExitCode = exited.Code
		return wire, nil
	}
	return wire, err
}

func (s *nativeSession) Close(ctx context.Context) error { return s.session.Close(ctx) }

func (s *nativeSession) CancelStopsProcess() bool {
	if policy, ok := s.session.(process.CancelPolicy); ok {
		return policy.CancelStopsProcess()
	}
	return s.cancelStops
}

func (s *nativeSession) filesystem() (executor.FileSystem, error) {
	return executor.FileSystemFor(s.session)
}

func (s *nativeSession) Stat(ctx context.Context, path string) (sdk.FileInfo, error) {
	filesystem, err := s.filesystem()
	if err != nil {
		return sdk.FileInfo{}, err
	}
	info, err := filesystem.Stat(ctx, path)
	return sdk.FileInfo{Size: info.Size, Mode: info.Mode, ModTime: info.ModTime}, err
}

func (s *nativeSession) ReadFile(ctx context.Context, path string, maximum int64) ([]byte, error) {
	filesystem, err := s.filesystem()
	if err != nil {
		return nil, err
	}
	return filesystem.ReadFile(ctx, path, maximum)
}

func (s *nativeSession) WriteFile(ctx context.Context, path string, data []byte, options sdk.WriteOptions) error {
	filesystem, err := s.filesystem()
	if err != nil {
		return err
	}
	return filesystem.WriteFile(ctx, path, data, executor.WriteOptions{Mode: options.Mode, Replace: options.Replace})
}

func (s *nativeSession) MkdirAll(ctx context.Context, path string, mode fs.FileMode) error {
	filesystem, err := s.filesystem()
	if err != nil {
		return err
	}
	return filesystem.MkdirAll(ctx, path, mode)
}

var (
	_ sdk.ExecutorFileSystem = (*nativeSession)(nil)
	_ sdk.CancelPolicy       = (*nativeSession)(nil)
)
