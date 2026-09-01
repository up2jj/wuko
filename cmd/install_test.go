package cmd

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	envload "github.com/up2jj/wuko/environment"
	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/steps/shell"
	"github.com/up2jj/wuko/workflow"
)

func lifecycleTestRegistry(t *testing.T) *step.Registry {
	t.Helper()
	registry := step.NewRegistry()
	if err := shell.Register(registry); err != nil {
		t.Fatal(err)
	}
	return registry
}

func lifecycleTestCommand(root, home string, registry *step.Registry, interactive bool, confirm func(context.Context, io.Reader, io.Writer, string, bool) (bool, error)) *cobra.Command {
	return newRootCmd(dependencies{
		stdin: bytes.NewReader(nil), stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{},
		cwd:       func() (string, error) { return root, nil },
		homeDir:   func() (string, error) { return home, nil },
		configDir: func() (string, error) { return filepath.Join(home, "config"), nil },
		environment: envload.InvocationLoaderFunc(func(context.Context, string, map[string]string, envload.Policy) (envload.InvocationEnvironment, error) {
			return envload.InvocationEnvironment{Values: map[string]string{}}, nil
		}),
		isInteractive: func(io.Reader) bool { return interactive },
		confirm:       confirm,
		registry:      registry,
	})
}

func writeLifecycleSource(t *testing.T, directory, name, install, uninstall string) string {
	t.Helper()
	path := filepath.Join(directory, name+"-source.yaml")
	data := "version: 1\nname: " + name + "\nsteps:\n  - id: run\n    type: shell\n    with: {command: true}\n"
	if install != "" {
		data += "install:\n  - id: install\n    type: shell\n    with:\n      script: " + install + "\n"
	}
	if uninstall != "" {
		data += "uninstall:\n  - id: uninstall\n    type: shell\n    with:\n      script: " + uninstall + "\n"
	}
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestInstallWorkflowUsesLocalDirectoryByDefaultAndGlobalWithFlag(t *testing.T) {
	root, home := t.TempDir(), t.TempDir()
	registry := lifecycleTestRegistry(t)
	localSource := writeLifecycleSource(t, root, "local", "pwd > install-dir", "")
	command := lifecycleTestCommand(root, home, registry, false, nil)
	command.SetArgs([]string{"install", localSource, "--env", "WUKO_TEST=yes"})
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	localDir := filepath.Join(root, ".wuko", "workflows")
	if _, err := os.Stat(filepath.Join(localDir, "local.yaml")); err != nil {
		t.Fatalf("local workflow was not installed: %v", err)
	}
	wantLocalDir, err := filepath.EvalSymlinks(localDir)
	if err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(localDir, "install-dir")); err != nil || strings.TrimSpace(string(data)) != wantLocalDir {
		t.Fatalf("local install hook directory = %q, err = %v", data, err)
	}

	globalSource := writeLifecycleSource(t, root, "global", "pwd > install-dir", "")
	command = lifecycleTestCommand(root, home, registry, false, nil)
	command.SetArgs([]string{"install", "--global", globalSource})
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	globalDir := filepath.Join(home, ".wuko", "workflows")
	if _, err := os.Stat(filepath.Join(globalDir, "global.yaml")); err != nil {
		t.Fatalf("global workflow was not installed: %v", err)
	}
	wantGlobalDir, err := filepath.EvalSymlinks(globalDir)
	if err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(globalDir, "install-dir")); err != nil || strings.TrimSpace(string(data)) != wantGlobalDir {
		t.Fatalf("global install hook directory = %q, err = %v", data, err)
	}
}

func TestUninstallWorkflowRequiresConfirmationAndRunsHookAfterApproval(t *testing.T) {
	root, home := t.TempDir(), t.TempDir()
	directory := filepath.Join(root, ".wuko", "workflows")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	path := writeLifecycleSource(t, root, "remove", "", "pwd > uninstall-dir")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	installed := filepath.Join(directory, "remove.yaml")
	if err := os.WriteFile(installed, data, 0o644); err != nil {
		t.Fatal(err)
	}
	registry := lifecycleTestRegistry(t)
	command := lifecycleTestCommand(root, home, registry, true, func(context.Context, io.Reader, io.Writer, string, bool) (bool, error) {
		return false, nil
	})
	command.SetArgs([]string{"uninstall", "remove"})
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(installed); err != nil {
		t.Fatalf("declined uninstall removed workflow: %v", err)
	}
	if _, err := os.Stat(filepath.Join(directory, "uninstall-dir")); !os.IsNotExist(err) {
		t.Fatalf("declined uninstall ran hook: %v", err)
	}

	command = lifecycleTestCommand(root, home, registry, true, func(context.Context, io.Reader, io.Writer, string, bool) (bool, error) {
		return true, nil
	})
	command.SetArgs([]string{"uninstall", "remove"})
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(installed); !os.IsNotExist(err) {
		t.Fatalf("approved uninstall left workflow: %v", err)
	}
	wantDirectory, err := filepath.EvalSymlinks(directory)
	if err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(directory, "uninstall-dir")); err != nil || strings.TrimSpace(string(data)) != wantDirectory {
		t.Fatalf("uninstall hook directory = %q, err = %v", data, err)
	}
}

func TestUninstallGlobalFlagAndYesBypassConfirmation(t *testing.T) {
	root, home := t.TempDir(), t.TempDir()
	directory := filepath.Join(home, ".wuko", "workflows")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	path := writeLifecycleSource(t, root, "global-remove", "", "pwd > uninstall-dir")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	installed := filepath.Join(directory, "global-remove.yaml")
	if err := os.WriteFile(installed, data, 0o644); err != nil {
		t.Fatal(err)
	}
	called := false
	command := lifecycleTestCommand(root, home, lifecycleTestRegistry(t), false, func(context.Context, io.Reader, io.Writer, string, bool) (bool, error) {
		called = true
		return true, nil
	})
	command.SetArgs([]string{"uninstall", "--global", "--yes", "global-remove"})
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("confirmation was called despite --yes")
	}
	if _, err := os.Stat(installed); !os.IsNotExist(err) {
		t.Fatalf("global workflow remains after uninstall: %v", err)
	}
}

func TestUninstallNonInteractiveWithoutYesFailsBeforeHook(t *testing.T) {
	root, home := t.TempDir(), t.TempDir()
	directory := filepath.Join(root, ".wuko", "workflows")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	path := writeLifecycleSource(t, root, "noninteractive", "", "touch uninstall-ran")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	installed := filepath.Join(directory, "noninteractive.yaml")
	if err := os.WriteFile(installed, data, 0o644); err != nil {
		t.Fatal(err)
	}
	command := lifecycleTestCommand(root, home, lifecycleTestRegistry(t), false, func(context.Context, io.Reader, io.Writer, string, bool) (bool, error) {
		t.Fatal("confirmation should not be called")
		return false, nil
	})
	command.SetArgs([]string{"uninstall", "noninteractive"})
	err = command.ExecuteContext(t.Context())
	if err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("error = %v, want non-interactive confirmation error", err)
	}
	if _, statErr := os.Stat(installed); statErr != nil {
		t.Fatalf("workflow was removed after confirmation error: %v", statErr)
	}
}

func TestInstallFailedHookPreservesExistingWorkflow(t *testing.T) {
	root, home := t.TempDir(), t.TempDir()
	directory := filepath.Join(root, ".wuko", "workflows")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	existing := filepath.Join(directory, "replace.yaml")
	old := "version: 1\nname: replace\nsteps:\n  - id: old\n    type: shell\n    with: {command: true}\n"
	if err := os.WriteFile(existing, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	source := writeLifecycleSource(t, root, "replace", "exit 1", "")
	command := lifecycleTestCommand(root, home, lifecycleTestRegistry(t), false, nil)
	command.SetArgs([]string{"install", source})
	if err := command.ExecuteContext(t.Context()); err == nil || !strings.Contains(err.Error(), "install hook") {
		t.Fatalf("error = %v, want install hook failure", err)
	}
	data, err := os.ReadFile(existing)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != old {
		t.Fatalf("existing workflow changed after failed install: %q", data)
	}
}

func TestInstallRejectsLocalCompanionFiles(t *testing.T) {
	root, home := t.TempDir(), t.TempDir()
	source := writeLifecycleSource(t, root, "standalone", "./install.sh", "")
	if err := os.WriteFile(filepath.Join(root, "install.sh"), []byte("echo setup\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	command := lifecycleTestCommand(root, home, lifecycleTestRegistry(t), false, nil)
	command.SetArgs([]string{"install", source})

	err := command.ExecuteContext(t.Context())
	if err == nil || !strings.Contains(err.Error(), "relative lifecycle script") {
		t.Fatalf("error = %v, want standalone lifecycle-script error", err)
	}
}

func TestUninstallGlobalDoesNotRemoveShadowingLocalWorkflow(t *testing.T) {
	root, home := t.TempDir(), t.TempDir()
	localDir := filepath.Join(root, ".wuko", "workflows")
	globalDir := filepath.Join(home, ".wuko", "workflows")
	if err := os.MkdirAll(localDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(globalDir, 0o755); err != nil {
		t.Fatal(err)
	}
	data := []byte("version: 1\nname: shared\nsteps:\n  - id: run\n    type: shell\n    with: {command: true}\n")
	localPath := filepath.Join(localDir, "shared.yaml")
	globalPath := filepath.Join(globalDir, "shared.yaml")
	if err := os.WriteFile(localPath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(globalPath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	command := lifecycleTestCommand(root, home, lifecycleTestRegistry(t), false, nil)
	command.SetArgs([]string{"uninstall", "--global", "--yes", "shared"})
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(globalPath); !os.IsNotExist(err) {
		t.Fatalf("global workflow remains after uninstall: %v", err)
	}
	if _, err := os.Stat(localPath); err != nil {
		t.Fatalf("shadowing local workflow was removed: %v", err)
	}
}

func TestUninstallInstalledPackageRunsHookAndRemovesSidecars(t *testing.T) {
	root, home := t.TempDir(), t.TempDir()
	packageDir := filepath.Join(root, ".wuko", "workflows", "marketplace", "release")
	if err := os.MkdirAll(packageDir, 0o755); err != nil {
		t.Fatal(err)
	}
	definition := `version: 1
name: release
steps:
  - id: run
    type: shell
    with: {command: true}
uninstall:
  - id: cleanup
    type: shell
    with:
      script: pwd > ../uninstall-dir
`
	if err := os.WriteFile(filepath.Join(packageDir, "wuko.yaml"), []byte(definition), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(packageDir, "defaults.json"), []byte(`{"target":"staging"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(packageDir, workflow.WorkflowPackageMarkerName), []byte(`{"version":1,"name":"release"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	wantRunDir, err := filepath.EvalSymlinks(packageDir)
	if err != nil {
		t.Fatal(err)
	}

	command := lifecycleTestCommand(root, home, lifecycleTestRegistry(t), false, nil)
	command.SetArgs([]string{"uninstall", "--yes", "release"})
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(packageDir); !os.IsNotExist(err) {
		t.Fatalf("installed package directory remains: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root, ".wuko", "workflows", "marketplace", "uninstall-dir"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(data)) != wantRunDir {
		t.Fatalf("uninstall hook directory = %q, want %q", strings.TrimSpace(string(data)), wantRunDir)
	}
}
