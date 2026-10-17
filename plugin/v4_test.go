package plugin

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"path/filepath"
	"testing"

	"github.com/up2jj/wuko/executor"
	"github.com/up2jj/wuko/plugin/sdk"
	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/workflow"
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
