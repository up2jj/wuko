package plugin

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"testing"

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
