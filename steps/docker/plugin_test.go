package docker

// These tests serve the docker step and executor from a plugin, connected in process to the real
// plugin host, under the names a docker plugin would give them. Passing here shows the package
// can move into a plugin without losing what the built-in registration provides.

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"path/filepath"
	"testing"

	"github.com/up2jj/wuko/executor"
	"github.com/up2jj/wuko/plugin"
	"github.com/up2jj/wuko/plugin/plugintest"
	"github.com/up2jj/wuko/plugin/sdk"
	"github.com/up2jj/wuko/plugin/stepadapter"
	"github.com/up2jj/wuko/process"
	"github.com/up2jj/wuko/step"
)

func dockerPlugin(t *testing.T, client dockerClient) *plugin.Manager {
	t.Helper()
	fake := func() (dockerClient, error) { return client, nil }
	served, err := sdk.New("docker")
	if err != nil {
		t.Fatal(err)
	}
	if err := stepadapter.RegisterExecutor(served, "container", stepadapter.ExecutorRegistration{
		Builder: func(raw map[string]any) (executor.Provider, error) {
			provider, err := NewExecutor(raw)
			if err != nil {
				return nil, err
			}
			provider.(*ExecutorProvider).newClient = fake
			return provider, nil
		},
		FileSystem: true, Stdin: true,
	}); err != nil {
		t.Fatal(err)
	}
	outputs := step.OpenObject()
	if err := stepadapter.Register(served, "run", stepadapter.Registration{
		Builder: func(raw map[string]any) (step.Runner, error) {
			runner, err := New(raw)
			if err != nil {
				return nil, err
			}
			switch typed := runner.(type) {
			case *Runner:
				typed.newClient = fake
			case *managedRunner:
				typed.newClient = fake
			}
			return runner, nil
		},
		Outputs: &outputs, Cleanup: true,
		Terminal: func(config map[string]any) (bool, bool) { return config["tty"] == true, false },
	}, sdk.WithHostCallbacks(sdk.HostTerminal)); err != nil {
		t.Fatal(err)
	}
	return plugintest.Connect(t, served)
}

func TestDockerStepThroughPlugin(t *testing.T) {
	client := &fakeClient{output: []byte("hi"), exitCode: 0}
	manager := dockerPlugin(t, client)
	runner, err := manager.ResolveStep(t.Context(), "docker.run", map[string]any{"image": "alpine", "command": "cat", "tty": true, "stdin": "hi"})
	if err != nil {
		t.Fatal(err)
	}
	var live bytes.Buffer
	result, err := runner.Run(t.Context(), step.Request{StepID: "cat", RunDir: t.TempDir(), Stdout: &live, Stderr: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outputs["stdout"] != "hi" || live.String() != "hi" {
		t.Fatalf("outputs = %#v, live = %q", result.Outputs, live.String())
	}
	// The container config is built in the plugin exactly as in process.
	if !client.created.Config.Tty || !client.created.Config.StdinOnce {
		t.Fatalf("container config = %#v", client.created.Config)
	}
	if !client.started || !client.removed {
		t.Fatalf("container lifecycle = started:%v removed:%v", client.started, client.removed)
	}
}

func TestFailedDockerStepThroughPluginKeepsItsExitCode(t *testing.T) {
	manager := dockerPlugin(t, &fakeClient{output: multiplexedOutput("", "failed\n"), exitCode: 17})
	runner, err := manager.ResolveStep(t.Context(), "docker.run", map[string]any{"image": "alpine", "command": "false"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(t.Context(), step.Request{StepID: "fail", RunDir: t.TempDir(), Stdout: io.Discard, Stderr: io.Discard})
	if err == nil {
		t.Fatal("expected the container's failure")
	}
	if result.Outputs["exit_code"] != int64(17) || result.Outputs["stderr"] != "failed\n" {
		t.Fatalf("outputs of the failed run = %#v", result.Outputs)
	}
}

func openPluginContainer(t *testing.T, client dockerClient) (executor.Provider, executor.Session, string) {
	t.Helper()
	provider, err := dockerPlugin(t, client).ResolveExecutor(t.Context(), "docker.container", map[string]any{"image": "golang:1.26"})
	if err != nil {
		t.Fatal(err)
	}
	runDir := t.TempDir()
	session, err := provider.Open(t.Context(), executor.Request{RunDir: runDir, Stdout: io.Discard, Stderr: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	return provider, session, runDir
}

func TestDockerExecutorThroughPlugin(t *testing.T) {
	client := &fakeClient{output: multiplexedOutput("built\n", "warning\n")}
	_, session, runDir := openPluginContainer(t, client)
	result, err := session.Run(t.Context(), process.Options{Command: "go", Args: []string{"build"}, Dir: runDir, Stdout: io.Discard, Stderr: io.Discard})
	if err != nil || result.Stdout != "built\n" || result.Stderr != "warning\n" {
		t.Fatalf("Run() = %#v, %v", result, err)
	}
	if client.execOptions.WorkingDir != defaultExecutorWorkspace {
		t.Fatalf("exec working directory = %q", client.execOptions.WorkingDir)
	}
	if err := session.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !client.removed {
		t.Fatal("closing the session did not remove the container")
	}
}

func TestDockerExecutorFileSystemThroughPlugin(t *testing.T) {
	client := newFilesystemClient()
	provider, session, runDir := openPluginContainer(t, client)
	defer session.Close(context.Background())
	if !provider.(interface{ SupportsFileSystem() bool }).SupportsFileSystem() {
		t.Fatal("the plugin executor does not expose the container filesystem")
	}
	// File steps reach the container through the session's filesystem, path mapping included.
	client.content[defaultExecutorWorkspace+"/config.yaml"] = []byte("version: 1\n")
	filesystem, err := executor.FileSystemFor(session)
	if err != nil {
		t.Fatal(err)
	}
	data, err := filesystem.ReadFile(t.Context(), filepath.Join(runDir, "config.yaml"), 0)
	if err != nil || string(data) != "version: 1\n" {
		t.Fatalf("ReadFile() = %q, %v", data, err)
	}
	if _, err := filesystem.ReadFile(t.Context(), filepath.Join(runDir, "missing.yaml"), 0); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("ReadFile(missing) = %v, want fs.ErrNotExist", err)
	}
}
