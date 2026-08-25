package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/steps/shell"
	"github.com/up2jj/wuko/tui"
	"github.com/up2jj/wuko/workflow"
)

func TestMarketplaceInitAndBuild(t *testing.T) {
	root, home := t.TempDir(), t.TempDir()
	workflowRoot := filepath.Join(root, ".wuko", "workflows")
	if err := os.MkdirAll(filepath.Join(workflowRoot, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeMarketplaceWorkflow(t, filepath.Join(workflowRoot, "release.yaml"), "release", "Release")
	writeMarketplaceWorkflow(t, filepath.Join(workflowRoot, "nested", "publish.yml"), "publish", "Publish")
	command := marketplaceTestCommand(root, home, nil)
	command.SetArgs([]string{"marketplace", "init"})
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	command = marketplaceTestCommand(root, home, nil)
	command.SetArgs([]string{"marketplace", "build"})
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	var manifest workflow.MarketplaceManifest
	data, err := os.ReadFile(filepath.Join(root, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Version != workflow.MarketplaceManifestVersion || len(manifest.Workflows) != 2 {
		t.Fatalf("manifest = %#v", manifest)
	}
	paths := []string{manifest.Workflows[0].Path, manifest.Workflows[1].Path}
	if !slices.Equal(paths, []string{".wuko/workflows/nested/publish.yml", ".wuko/workflows/release.yaml"}) {
		t.Fatalf("paths = %#v", paths)
	}
	if manifest.Workflows[0].Description != "Publish" {
		t.Fatalf("description = %q", manifest.Workflows[0].Description)
	}

	command = marketplaceTestCommand(root, home, nil)
	command.SetArgs([]string{"marketplace", "init"})
	if err := command.ExecuteContext(t.Context()); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("second init error = %v", err)
	}
}

func TestMarketplaceInstallSelectsManifestEntriesAndScopesRepository(t *testing.T) {
	root, home := t.TempDir(), t.TempDir()
	manifest := `{"version":1,"workflows":[{"name":"first","path":"first.yaml"},{"name":"second","path":"nested/second.yaml"}]}`
	workflowData := map[string]string{
		"/repo/first.yaml":         "first",
		"/repo/nested/second.yaml": "second",
	}
	client := &http.Client{Transport: commandRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Path {
		case "/repo/manifest.json":
			return commandTestResponse(http.StatusOK, manifest), nil
		case "/repo/first.yaml", "/repo/nested/second.yaml":
			name := workflowData[request.URL.Path]
			return commandTestResponse(http.StatusOK, fmt.Sprintf("version: 1\nname: %s\nsteps:\n  - id: run\n    type: shell\n    with: {command: true}\n", name)), nil
		default:
			return commandTestResponse(http.StatusNotFound, ""), nil
		}
	})}
	registry := step.NewRegistry()
	if err := shell.Register(registry); err != nil {
		t.Fatal(err)
	}
	var selectedCalls int
	command := newRootCmd(dependencies{
		stdin: bytes.NewReader(nil), stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{},
		cwd: func() (string, error) { return root, nil }, homeDir: func() (string, error) { return home, nil },
		configDir: func() (string, error) { return filepath.Join(home, "config"), nil }, registry: registry,
		loader: workflow.NewLoader(client), isInteractive: func(io.Reader) bool { return true },
		selectMany: func(context.Context, io.Reader, io.Writer, string, []tui.Option) ([]int, error) {
			selectedCalls++
			return []int{1, 0}, nil
		},
	})
	command.SetArgs([]string{"install", "https://example.test/repo"})
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if selectedCalls != 1 {
		t.Fatalf("picker calls = %d, want 1", selectedCalls)
	}
	installDir := filepath.Join(root, ".wuko", "workflows", "repo")
	for _, name := range []string{"first", "second"} {
		if _, err := os.Stat(filepath.Join(installDir, name+".yaml")); err != nil {
			t.Fatalf("installed %s: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, ".wuko", "workflows", "first.yaml")); !os.IsNotExist(err) {
		t.Fatalf("workflow escaped repository directory: %v", err)
	}
	marker, err := os.ReadFile(filepath.Join(installDir, marketplaceMarkerName))
	if err != nil || !strings.Contains(string(marker), "https://example.test/repo/") {
		t.Fatalf("marker = %q, err = %v", marker, err)
	}
}

func TestMarketplaceInstallRejectsDuplicateWorkflowNamesBeforeWriting(t *testing.T) {
	root, home := t.TempDir(), t.TempDir()
	manifest := `{"version":1,"workflows":[{"path":"a.yaml"},{"path":"b.yaml"}]}`
	client := &http.Client{Transport: commandRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Path {
		case "/repo/manifest.json":
			return commandTestResponse(http.StatusOK, manifest), nil
		case "/repo/a.yaml", "/repo/b.yaml":
			return commandTestResponse(http.StatusOK, "version: 1\nname: same\nsteps:\n  - id: run\n    type: shell\n    with: {command: true}\n"), nil
		default:
			return commandTestResponse(http.StatusNotFound, ""), nil
		}
	})}
	command := newRootCmd(dependencies{
		stdin: bytes.NewReader(nil), stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{},
		cwd: func() (string, error) { return root, nil }, homeDir: func() (string, error) { return home, nil },
		configDir: func() (string, error) { return filepath.Join(home, "config"), nil },
		registry:  lifecycleTestRegistry(t), loader: workflow.NewLoader(client),
		isInteractive: func(io.Reader) bool { return true },
		selectMany: func(context.Context, io.Reader, io.Writer, string, []tui.Option) ([]int, error) {
			return []int{0, 1}, nil
		},
	})
	command.SetArgs([]string{"install", "https://example.test/repo"})
	err := command.ExecuteContext(t.Context())
	if err == nil || !strings.Contains(err.Error(), "both define workflow name") {
		t.Fatalf("error = %v, want duplicate workflow-name error", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".wuko", "workflows")); !os.IsNotExist(err) {
		t.Fatalf("marketplace directory was created after preflight failure: %v", err)
	}
}

func marketplaceTestCommand(root, home string, loader *workflow.Loader) *cobra.Command {
	return newRootCmd(dependencies{
		stdin: bytes.NewReader(nil), stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{},
		cwd: func() (string, error) { return root, nil }, homeDir: func() (string, error) { return home, nil },
		configDir: func() (string, error) { return filepath.Join(home, "config"), nil },
		registry:  step.NewRegistry(), loader: loader,
	})
}

func writeMarketplaceWorkflow(t *testing.T, filename, name, description string) {
	t.Helper()
	data := fmt.Sprintf("version: 1\nname: %s\ndescription: %s\nsteps:\n  - id: run\n    type: shell\n    with: {command: true}\n", name, description)
	if err := os.WriteFile(filename, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

func commandTestResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Status: fmt.Sprintf("%d %s", status, http.StatusText(status)), Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}
