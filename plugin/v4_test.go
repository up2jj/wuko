package plugin

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/up2jj/wuko/executor"
	"github.com/up2jj/wuko/plugin/sdk"
	"github.com/up2jj/wuko/process"
	"github.com/up2jj/wuko/ptyinteract"
	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/workflow"
	"golang.org/x/term"
)

// attachSDK serves an SDK plugin in process and attaches it to a fresh manager, so the real host
// side of the protocol talks to the real SDK side.
func attachSDK(t *testing.T, plugin *sdk.Plugin) *Manager {
	t.Helper()
	manager := NewManager(Config{})
	toPluginReader, toPlugin := io.Pipe()
	fromPlugin, fromPluginWriter := io.Pipe()
	served := make(chan error, 1)
	go func() {
		err := plugin.Serve(context.Background(), toPluginReader, fromPluginWriter)
		_ = fromPluginWriter.Close()
		served <- err
	}()
	if err := manager.Attach(t.Context(), plugin.Namespace(), toPlugin, fromPlugin); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := manager.Close(context.Background(), "completed"); err != nil {
			t.Errorf("closing manager: %v", err)
		}
		if err := <-served; err != nil {
			t.Errorf("serving plugin: %v", err)
		}
	})
	return manager
}

func TestAttachedPluginNeedsNoWorkflowDeclaration(t *testing.T) {
	plugin, err := sdk.New("acme")
	if err != nil {
		t.Fatal(err)
	}
	if err := sdk.RegisterStep(plugin, "echo", sdk.StepHandler[map[string]any]{Run: func(_ context.Context, request sdk.RunRequest[map[string]any]) (sdk.Result, error) {
		return sdk.Result{Outputs: map[string]any{"value": request.Config["value"]}}, nil
	}}); err != nil {
		t.Fatal(err)
	}
	manager := attachSDK(t, plugin)
	// A workflow that declares other plugins still reaches the attached one.
	ctx := workflow.ContextWithResolvedPlugins(t.Context(), map[string]workflow.ResolvedPluginSource{})
	runner, err := manager.ResolveStep(ctx, "acme.echo", map[string]any{"value": "hi"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(ctx, step.Request{StepID: "echo"})
	if err != nil || result.Outputs["value"] != "hi" {
		t.Fatalf("Run() = %#v, %v", result, err)
	}
}

func TestSDKAnswersV3UnlessItUsesV4Capabilities(t *testing.T) {
	for _, test := range []struct {
		name       string
		filesystem bool
		accepted   bool
	}{
		{name: "v3 plugin", accepted: true},
		{name: "v4 executor", filesystem: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			plugin, err := sdk.New("acme")
			if err != nil {
				t.Fatal(err)
			}
			if err := sdk.RegisterExecutor(plugin, "box", sdk.ExecutorHandler[map[string]any]{FileSystem: test.filesystem, Open: func(context.Context, sdk.ExecutorRequest[map[string]any]) (sdk.ExecutorSession, error) {
				return nil, errors.New("unused")
			}}); err != nil {
				t.Fatal(err)
			}
			toPluginReader, toPlugin := io.Pipe()
			fromPlugin, fromPluginWriter := io.Pipe()
			go func() {
				_ = plugin.Serve(context.Background(), toPluginReader, fromPluginWriter)
				_ = fromPluginWriter.Close()
			}()
			v3 := newClient(nil, toPlugin)
			go v3.read(fromPlugin)
			defer closeClient(v3)
			var initialized initializeResult
			err = v3.call(t.Context(), "initialize", map[string]any{"protocol": ProtocolV3}, &initialized, nil)
			if test.accepted {
				if err != nil || initialized.Protocol != ProtocolV3 {
					t.Fatalf("v3 handshake = %#v, %v", initialized, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("v3 handshake accepted a plugin with v4 capabilities: %#v", initialized)
			}
		})
	}
}

func TestV4DeclarationsRequireProtocolV4(t *testing.T) {
	declarations := []initializeResult{
		{Steps: []stepDeclaration{{Type: "acme.run", Executor: true}}},
		{Steps: []stepDeclaration{{Type: "acme.run", HostCallbacks: []string{hostTerminalAcquire}}}},
		{Executors: []executorDeclaration{{Type: "acme.box", FileSystem: true}}},
		{Executors: []executorDeclaration{{Type: "acme.box", Stdin: true}}},
		{Executors: []executorDeclaration{{Type: "acme.box", TTY: true}}},
	}
	for _, initialized := range declarations {
		if err := validateInitializeDeclarations("acme", ProtocolV3, initialized); err == nil {
			t.Fatalf("v3 accepted %#v", initialized)
		}
		if err := validateInitializeDeclarations("acme", ProtocolV4, initialized); err != nil {
			t.Fatalf("v4 rejected %#v: %v", initialized, err)
		}
	}
}

func TestRemoteErrorCodesMatchStandardErrors(t *testing.T) {
	plugin, err := sdk.New("acme")
	if err != nil {
		t.Fatal(err)
	}
	if err := sdk.RegisterStep(plugin, "missing", sdk.StepHandler[map[string]any]{Run: func(context.Context, sdk.RunRequest[map[string]any]) (sdk.Result, error) {
		return sdk.Result{}, &fs.PathError{Op: "open", Path: "/nope", Err: fs.ErrNotExist}
	}}); err != nil {
		t.Fatal(err)
	}
	manager := attachSDK(t, plugin)
	runner, err := manager.ResolveStep(t.Context(), "acme.missing", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.Run(t.Context(), step.Request{StepID: "missing"})
	if !errors.Is(err, fs.ErrNotExist) || err.Error() != "open /nope: file does not exist" {
		t.Fatalf("Run() error = %v, want fs.ErrNotExist with the plugin's message", err)
	}
}

// localSession is an SDK executor session over the host filesystem.
type localSession struct{ local executor.LocalFileSystem }

func (localSession) Run(context.Context, sdk.Command) (sdk.CommandResult, error) {
	return sdk.CommandResult{}, errors.New("unused")
}
func (localSession) Close(context.Context) error { return nil }
func (s localSession) Stat(ctx context.Context, path string) (sdk.FileInfo, error) {
	info, err := s.local.Stat(ctx, path)
	return sdk.FileInfo{Size: info.Size, Mode: info.Mode, ModTime: info.ModTime}, err
}
func (s localSession) ReadFile(ctx context.Context, path string, maximum int64) ([]byte, error) {
	return s.local.ReadFile(ctx, path, maximum)
}
func (s localSession) WriteFile(ctx context.Context, path string, data []byte, options sdk.WriteOptions) error {
	return s.local.WriteFile(ctx, path, data, executor.WriteOptions{Mode: options.Mode, Replace: options.Replace})
}
func (s localSession) MkdirAll(ctx context.Context, path string, mode fs.FileMode) error {
	return s.local.MkdirAll(ctx, path, mode)
}

func openTestExecutor(t *testing.T, handler sdk.ExecutorHandler[map[string]any]) (executor.Provider, executor.Session) {
	t.Helper()
	plugin, err := sdk.New("acme")
	if err != nil {
		t.Fatal(err)
	}
	if err := sdk.RegisterExecutor(plugin, "box", handler); err != nil {
		t.Fatal(err)
	}
	manager := attachSDK(t, plugin)
	provider, err := manager.ResolveExecutor(t.Context(), "acme.box", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	session, err := provider.Open(t.Context(), executor.Request{RunDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close(context.Background()) })
	return provider, session
}

func TestPluginExecutorFileSystem(t *testing.T) {
	provider, session := openTestExecutor(t, sdk.ExecutorHandler[map[string]any]{FileSystem: true, Open: func(context.Context, sdk.ExecutorRequest[map[string]any]) (sdk.ExecutorSession, error) {
		return localSession{}, nil
	}})
	if supports, ok := provider.(interface{ SupportsFileSystem() bool }); !ok || !supports.SupportsFileSystem() {
		t.Fatal("provider does not advertise its filesystem")
	}
	filesystem, err := executor.FileSystemFor(session)
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	directory := filepath.Join(t.TempDir(), "nested", "dir")
	if err := filesystem.MkdirAll(ctx, directory, 0o750); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "file.txt")
	if err := filesystem.WriteFile(ctx, path, []byte("hello world"), executor.WriteOptions{Mode: 0o640}); err != nil {
		t.Fatal(err)
	}
	if err := filesystem.WriteFile(ctx, path, []byte("again"), executor.WriteOptions{Mode: 0o640}); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("WriteFile() over an existing file = %v, want fs.ErrExist", err)
	}
	info, err := filesystem.Stat(ctx, path)
	if err != nil || info.Size != 11 || info.Mode.Perm() != 0o640 || info.ModTime.IsZero() {
		t.Fatalf("Stat() = %+v, %v", info, err)
	}
	data, err := filesystem.ReadFile(ctx, path, 4)
	if err != nil || string(data) != "hello" {
		t.Fatalf("ReadFile(max 4) = %q, %v; want maximum+1 bytes", data, err)
	}
	if _, err := filesystem.Stat(ctx, filepath.Join(directory, "missing")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Stat(missing) = %v, want fs.ErrNotExist", err)
	}
	if err := filesystem.WriteFile(ctx, path, make([]byte, maxFileFrameData+1), executor.WriteOptions{Replace: true}); err == nil {
		t.Fatal("WriteFile() accepted a file larger than one frame")
	}
}

func TestPluginExecutorWithoutFileSystemExposesNone(t *testing.T) {
	provider, session := openTestExecutor(t, sdk.ExecutorHandler[map[string]any]{Open: func(context.Context, sdk.ExecutorRequest[map[string]any]) (sdk.ExecutorSession, error) {
		return localSession{}, nil
	}})
	if provider.(interface{ SupportsFileSystem() bool }).SupportsFileSystem() {
		t.Fatal("provider advertises an undeclared filesystem")
	}
	if _, err := executor.FileSystemFor(session); err == nil {
		t.Fatal("FileSystemFor() found a filesystem the plugin did not declare")
	}
}

// scriptSession runs every command with run.
type scriptSession struct {
	run func(context.Context, sdk.Command) (sdk.CommandResult, error)
}

func (s scriptSession) Run(ctx context.Context, command sdk.Command) (sdk.CommandResult, error) {
	return s.run(ctx, command)
}
func (scriptSession) Close(context.Context) error { return nil }

func scriptExecutor(handler sdk.ExecutorHandler[map[string]any], run func(context.Context, sdk.Command) (sdk.CommandResult, error)) sdk.ExecutorHandler[map[string]any] {
	handler.Open = func(context.Context, sdk.ExecutorRequest[map[string]any]) (sdk.ExecutorSession, error) {
		return scriptSession{run: run}, nil
	}
	return handler
}

// catCommand copies stdin to stdout and reports what it read.
func catCommand(_ context.Context, command sdk.Command) (sdk.CommandResult, error) {
	if command.Input == nil {
		return sdk.CommandResult{Stdout: "<no stdin>"}, nil
	}
	data, err := io.ReadAll(command.Input)
	return sdk.CommandResult{Stdout: string(data)}, err
}

func TestPluginExecutorStdin(t *testing.T) {
	large := strings.Repeat("0123456789abcdef", 16<<10)
	for _, test := range []struct {
		name     string
		streamed bool
		stdin    io.Reader
		want     string
	}{
		{name: "streamed", streamed: true, stdin: strings.NewReader(large), want: large},
		{name: "sent whole", stdin: strings.NewReader("whole"), want: "whole"},
		{name: "none", streamed: true, want: "<no stdin>"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, session := openTestExecutor(t, scriptExecutor(sdk.ExecutorHandler[map[string]any]{Stdin: test.streamed}, catCommand))
			result, err := session.Run(t.Context(), process.Options{Command: "cat", Stdin: test.stdin, StdoutPolicy: process.OutputCapture})
			if err != nil || result.Stdout != test.want {
				t.Fatalf("Run() = %d bytes, %v; want %d bytes", len(result.Stdout), err, len(test.want))
			}
		})
	}
}

func TestPluginExecutorStdinThatOutlivesTheCommand(t *testing.T) {
	_, session := openTestExecutor(t, scriptExecutor(sdk.ExecutorHandler[map[string]any]{Stdin: true}, func(_ context.Context, command sdk.Command) (sdk.CommandResult, error) {
		if !command.StdinOutlivesProcess {
			return sdk.CommandResult{}, errors.New("the command was not told its stdin outlives it")
		}
		line, err := bufio.NewReader(command.Input).ReadString('\n')
		return sdk.CommandResult{Stdout: line}, err
	}))
	reader, writer := io.Pipe()
	defer writer.Close()
	go func() { _, _ = writer.Write([]byte("first\n")) }()
	done := make(chan struct{})
	var result process.Result
	var err error
	go func() {
		defer close(done)
		result, err = session.Run(t.Context(), process.Options{Command: "head", Stdin: reader, StdinOutlivesProcess: true, StdoutPolicy: process.OutputCapture})
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run() waited for stdin that outlives the command")
	}
	if err != nil || result.Stdout != "first\n" {
		t.Fatalf("Run() = %q, %v", result.Stdout, err)
	}
}

func TestPluginExecutorRejectsUndeclaredStreams(t *testing.T) {
	_, session := openTestExecutor(t, scriptExecutor(sdk.ExecutorHandler[map[string]any]{}, catCommand))
	for name, options := range map[string]process.Options{
		"stdin outliving the command": {Command: "cat", Stdin: strings.NewReader("x"), StdinOutlivesProcess: true},
		"tty":                         {Command: "sh", TTY: true},
	} {
		if _, err := session.Run(t.Context(), options); err == nil {
			t.Fatalf("Run() with %s succeeded on an executor that did not declare it", name)
		}
	}
}

func TestPluginExecutorTTYRunsInteractions(t *testing.T) {
	_, session := openTestExecutor(t, scriptExecutor(sdk.ExecutorHandler[map[string]any]{TTY: true}, func(_ context.Context, command sdk.Command) (sdk.CommandResult, error) {
		if command.TTY == nil || command.Resize == nil {
			return sdk.CommandResult{}, errors.New("command is not a TTY run")
		}
		command.Started()
		fmt.Fprintf(command.Stdout, "size=%dx%d\r\nlogin:", command.TTY.Rows, command.TTY.Cols)
		name, err := bufio.NewReader(command.Input).ReadString('\r')
		if err != nil {
			return sdk.CommandResult{}, err
		}
		fmt.Fprintf(command.Stdout, "\r\nhello %s\n", strings.TrimSpace(name))
		return sdk.CommandResult{ExitCode: 4}, nil
	}))
	plan, err := ptyinteract.Compile([]ptyinteract.Spec{{HasExpect: true, Expect: "login:", Send: "bob", Newline: true}})
	if err != nil {
		t.Fatal(err)
	}
	var started atomic.Bool
	result, err := session.Run(t.Context(), process.Options{Command: "login", TTY: true, Interactions: plan, StdoutPolicy: process.OutputCapture, Started: func() { started.Store(true) }})
	var exitErr *process.ExitError
	if !errors.As(err, &exitErr) || exitErr.Code != 4 || result.ExitCode != 4 {
		t.Fatalf("Run() = %+v, %v; want exit code 4", result, err)
	}
	for _, want := range []string{"size=24x80", "hello bob"} {
		if !strings.Contains(result.Stdout, want) {
			t.Fatalf("captured output = %q, want %q", result.Stdout, want)
		}
	}
	if !started.Load() {
		t.Fatal("Started was not called")
	}
}

func TestPluginExecutorTTYCancellationStopsTheCommand(t *testing.T) {
	stopped := make(chan struct{})
	_, session := openTestExecutor(t, scriptExecutor(sdk.ExecutorHandler[map[string]any]{TTY: true}, func(ctx context.Context, command sdk.Command) (sdk.CommandResult, error) {
		fmt.Fprint(command.Stdout, "waiting:")
		<-ctx.Done()
		close(stopped)
		return sdk.CommandResult{}, ctx.Err()
	}))
	plan, err := ptyinteract.Compile([]ptyinteract.Spec{{HasExpect: true, Expect: "never", Send: "x"}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()
	if _, err := session.Run(ctx, process.Options{Command: "wait", TTY: true, Interactions: plan}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Run() = %v, want the deadline", err)
	}
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("the plugin's command was not canceled")
	}
}

// terminalStep reads keystrokes up to "q" from a raw terminal, or reports that it fell back.
func terminalStep(t *testing.T, release bool) *Manager {
	t.Helper()
	plugin, err := sdk.New("acme")
	if err != nil {
		t.Fatal(err)
	}
	if err := sdk.RegisterStep(plugin, "attach", sdk.StepHandler[map[string]any]{Run: func(ctx context.Context, request sdk.RunRequest[map[string]any]) (sdk.Result, error) {
		terminal, err := request.Host.Terminal(ctx, sdk.TerminalOptions{Raw: true})
		if errors.Is(err, sdk.ErrTerminalUnavailable) {
			return sdk.Result{Outputs: map[string]any{"interactive": request.Context.Interactive, "typed": "<fallback>"}}, nil
		}
		if err != nil {
			return sdk.Result{}, err
		}
		typed, err := bufio.NewReader(terminal.Input).ReadString('q')
		if err != nil {
			return sdk.Result{}, err
		}
		if release {
			if err := terminal.Release(ctx); err != nil {
				return sdk.Result{}, err
			}
		}
		return sdk.Result{Outputs: map[string]any{"interactive": request.Context.Interactive, "typed": typed, "rows": int(terminal.Size.Rows)}}, nil
	}}, sdk.WithHostCallbacks(sdk.HostTerminal)); err != nil {
		t.Fatal(err)
	}
	return attachSDK(t, plugin)
}

func TestPluginStepBorrowsAndRestoresTheTerminal(t *testing.T) {
	for _, release := range []bool{true, false} {
		t.Run(fmt.Sprintf("released by plugin %v", release), func(t *testing.T) {
			manager := terminalStep(t, release)
			master, terminal, err := pty.Open()
			if err != nil {
				t.Skipf("no pseudo-terminal: %v", err)
			}
			defer master.Close()
			defer terminal.Close()
			if err := pty.Setsize(master, &pty.Winsize{Rows: 33, Cols: 90}); err != nil {
				t.Fatal(err)
			}
			before, err := term.GetState(int(terminal.Fd()))
			if err != nil {
				t.Fatal(err)
			}
			runner, err := manager.ResolveStep(t.Context(), "acme.attach", nil)
			if err != nil {
				t.Fatal(err)
			}
			go func() {
				// Raw mode is in place once the plugin holds the terminal, so input typed now
				// arrives unbuffered and unechoed.
				time.Sleep(100 * time.Millisecond)
				_, _ = master.Write([]byte("abq"))
			}()
			go func() { _, _ = io.Copy(io.Discard, master) }()
			result, err := runner.Run(t.Context(), step.Request{StepID: "attach", Interactive: true, Stdin: terminal})
			if err != nil {
				t.Fatal(err)
			}
			if result.Outputs["typed"] != "abq" || result.Outputs["interactive"] != true || fmt.Sprint(result.Outputs["rows"]) != "33" {
				t.Fatalf("outputs = %#v", result.Outputs)
			}
			after, err := term.GetState(int(terminal.Fd()))
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, after) {
				t.Fatal("the terminal mode was not restored")
			}
		})
	}
}

func TestPluginStepTerminalIsUnavailableWhenNotInteractive(t *testing.T) {
	manager := terminalStep(t, true)
	runner, err := manager.ResolveStep(t.Context(), "acme.attach", nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(t.Context(), step.Request{StepID: "attach", Stdin: strings.NewReader("abq")})
	if err != nil || result.Outputs["typed"] != "<fallback>" || result.Outputs["interactive"] != false {
		t.Fatalf("Run() = %#v, %v; want the fallback", result.Outputs, err)
	}
}

// taskSession is a native session with a capability no protocol method carries.
type taskSession struct {
	scriptSession
	name string
}

func (s *taskSession) task(name string) string { return s.name + ":" + name }

func TestPluginStepRunsInItsOwnPluginsExecutor(t *testing.T) {
	plugin, err := sdk.New("acme")
	if err != nil {
		t.Fatal(err)
	}
	if err := sdk.RegisterExecutor(plugin, "shell", sdk.ExecutorHandler[map[string]any]{Open: func(_ context.Context, request sdk.ExecutorRequest[map[string]any]) (sdk.ExecutorSession, error) {
		return &taskSession{name: fmt.Sprint(request.Config["name"])}, nil
	}}); err != nil {
		t.Fatal(err)
	}
	if err := sdk.RegisterStep(plugin, "task", sdk.StepHandler[map[string]any]{Run: func(_ context.Context, request sdk.RunRequest[map[string]any]) (sdk.Result, error) {
		session, ok := request.Executor.(*taskSession)
		if !ok {
			return sdk.Result{Outputs: map[string]any{"ran": "outside"}}, nil
		}
		return sdk.Result{Outputs: map[string]any{"ran": session.task("build")}}, nil
	}}, sdk.WithExecutorAccess()); err != nil {
		t.Fatal(err)
	}
	other, err := sdk.New("other")
	if err != nil {
		t.Fatal(err)
	}
	if err := sdk.RegisterExecutor(other, "box", sdk.ExecutorHandler[map[string]any]{Open: func(context.Context, sdk.ExecutorRequest[map[string]any]) (sdk.ExecutorSession, error) {
		return localSession{}, nil
	}}); err != nil {
		t.Fatal(err)
	}
	manager := attachSDK(t, plugin)
	foreignManager := attachSDK(t, other)

	runner, err := manager.ResolveStep(t.Context(), "acme.task", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := runner.(step.ExecutorAware); !ok {
		t.Fatal("the step is not executor-aware")
	}
	open := func(manager *Manager, name string) executor.Session {
		provider, err := manager.ResolveExecutor(t.Context(), name, map[string]any{"name": "dev"})
		if err != nil {
			t.Fatal(err)
		}
		session, err := provider.Open(t.Context(), executor.Request{})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = session.Close(context.Background()) })
		return session
	}

	for _, test := range []struct {
		name    string
		request step.Request
		want    string
	}{
		{name: "own executor", request: step.Request{InsideExecutor: true, Executor: open(manager, "acme.shell")}, want: "dev:build"},
		{name: "outside executors", request: step.Request{Executor: process.LocalExecutor{}}, want: "outside"},
	} {
		result, err := runner.Run(t.Context(), test.request)
		if err != nil || result.Outputs["ran"] != test.want {
			t.Fatalf("%s: Run() = %#v, %v; want %q", test.name, result.Outputs, err, test.want)
		}
	}
	if _, err := runner.Run(t.Context(), step.Request{InsideExecutor: true, Executor: open(foreignManager, "other.box")}); err == nil || !strings.Contains(err.Error(), `runs only inside executors of plugin "acme"`) {
		t.Fatalf("Run() inside another plugin's executor = %v", err)
	}
	if err := runner.(step.Validator).Validate(t.Context(), step.Request{InsideExecutor: true}); err != nil {
		t.Fatalf("Validate() before the session exists = %v", err)
	}
}

func TestPluginExecutorOpenStreamsOutputAndSeesWorkflowDir(t *testing.T) {
	plugin, err := sdk.New("acme")
	if err != nil {
		t.Fatal(err)
	}
	if err := sdk.RegisterExecutor(plugin, "env", sdk.ExecutorHandler[map[string]any]{Open: func(_ context.Context, request sdk.ExecutorRequest[map[string]any]) (sdk.ExecutorSession, error) {
		fmt.Fprintf(request.Stdout, "building in %s\n", request.Context.WorkflowDir)
		fmt.Fprint(request.Stderr, "warning\n")
		return localSession{}, nil
	}}); err != nil {
		t.Fatal(err)
	}
	manager := attachSDK(t, plugin)
	provider, err := manager.ResolveExecutor(t.Context(), "acme.env", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr strings.Builder
	session, err := provider.Open(t.Context(), executor.Request{WorkflowDir: "/work/flow", Stdout: &stdout, Stderr: &stderr})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close(context.Background())
	if stdout.String() != "building in /work/flow\n" || stderr.String() != "warning\n" {
		t.Fatalf("open output = %q, %q", stdout.String(), stderr.String())
	}
}

type stoppingSession struct{ localSession }

func (stoppingSession) CancelStopsProcess() bool { return true }

func TestPluginExecutorSessionRefinesItsCancelPolicy(t *testing.T) {
	_, declared := openTestExecutor(t, sdk.ExecutorHandler[map[string]any]{Open: func(context.Context, sdk.ExecutorRequest[map[string]any]) (sdk.ExecutorSession, error) {
		return localSession{}, nil
	}})
	_, refined := openTestExecutor(t, sdk.ExecutorHandler[map[string]any]{Open: func(context.Context, sdk.ExecutorRequest[map[string]any]) (sdk.ExecutorSession, error) {
		return stoppingSession{}, nil
	}})
	if declared.(process.CancelPolicy).CancelStopsProcess() || !refined.(process.CancelPolicy).CancelStopsProcess() {
		t.Fatal("the session's own cancel policy did not override the declaration")
	}
}

func TestFailedPluginStepKeepsItsOutputs(t *testing.T) {
	plugin, err := sdk.New("acme")
	if err != nil {
		t.Fatal(err)
	}
	if err := sdk.RegisterStep(plugin, "fail", sdk.StepHandler[map[string]any]{Run: func(context.Context, sdk.RunRequest[map[string]any]) (sdk.Result, error) {
		return sdk.Result{Outputs: map[string]any{"exit_code": 75, "stderr": "rate limit"}}, errors.New("command exited with status 75")
	}}); err != nil {
		t.Fatal(err)
	}
	runner, err := attachSDK(t, plugin).ResolveStep(t.Context(), "acme.fail", nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(t.Context(), step.Request{StepID: "fail"})
	if err == nil || err.Error() != "command exited with status 75" {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Outputs["exit_code"] != int64(75) || result.Outputs["stderr"] != "rate limit" {
		t.Fatalf("outputs of the failed step = %#v", result.Outputs)
	}
}
