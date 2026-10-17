// Package plugintest runs an SDK plugin in process against the real Wuko plugin host, so its
// steps and executors can be exercised through the same protocol, registries, and engine a
// workflow reaches them through, without building or installing a release.
package plugintest

import (
	"context"
	"io"
	"testing"

	"github.com/up2jj/wuko/executor"
	"github.com/up2jj/wuko/plugin"
	"github.com/up2jj/wuko/plugin/sdk"
	"github.com/up2jj/wuko/step"
)

// Connect serves the plugin in process and returns a manager it is attached to. The manager
// resolves the plugin's steps, output schemas, and executors without a plugins: declaration,
// and is closed, shutting the plugin down, when the test ends.
func Connect(t testing.TB, served *sdk.Plugin) *plugin.Manager {
	t.Helper()
	manager := plugin.NewManager(plugin.Config{})
	toPluginReader, toPlugin := io.Pipe()
	fromPlugin, fromPluginWriter := io.Pipe()
	done := make(chan error, 1)
	go func() {
		err := served.Serve(context.Background(), toPluginReader, fromPluginWriter)
		_ = fromPluginWriter.Close()
		done <- err
	}()
	if err := manager.Attach(context.Background(), served.Namespace(), toPlugin, fromPlugin); err != nil {
		_ = toPlugin.Close()
		t.Fatalf("plugintest: %v", err)
	}
	t.Cleanup(func() {
		if err := manager.Close(context.Background(), "completed"); err != nil {
			t.Errorf("plugintest: closing the plugin: %v", err)
		}
		if err := <-done; err != nil {
			t.Errorf("plugintest: serving the plugin: %v", err)
		}
	})
	return manager
}

// Install makes the registries fall back to the manager for plugin types, as the CLI wires its
// own manager. Either registry may be nil.
func Install(manager *plugin.Manager, steps *step.Registry, executors *executor.Registry) {
	if steps != nil {
		steps.SetResolver(manager.ResolveStep)
		steps.SetOutputSchemaResolver(manager.ResolveOutputSchema)
	}
	if executors != nil {
		executors.SetResolver(manager.ResolveExecutor)
	}
}
