package cmd

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	pluginpkg "github.com/up2jj/wuko/plugin"
	"github.com/up2jj/wuko/tui"
	"github.com/up2jj/wuko/workflow"
)

func TestMain(main *testing.M) {
	if os.Getenv("WUKO_CMD_PLUGIN_HELPER") == "1" {
		runCommandPluginHelper()
		os.Exit(0)
	}
	os.Exit(main.Run())
}

func runCommandPluginHelper() {
	scanner := bufio.NewScanner(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	for scanner.Scan() {
		var request struct {
			ID     string `json:"id"`
			Method string `json:"method"`
		}
		if json.Unmarshal(scanner.Bytes(), &request) != nil || request.ID == "" {
			continue
		}
		result := any(map[string]any{})
		if request.Method == "initialize" {
			result = map[string]any{"protocol": pluginpkg.Protocol, "namespace": "acme", "steps": []any{}, "executors": []any{}, "helpers": []any{}}
		}
		_ = encoder.Encode(map[string]any{"id": request.ID, "result": result})
		if request.Method == "shutdown" {
			return
		}
	}
}

func TestGoPluginScaffoldBuilds(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "wuko-plugin-acme")
	if err := writeGoPluginScaffold(directory, "acme"); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("go", "test", "./...")
	command.Dir = directory
	command.Env = append(command.Environ(), "GOCACHE="+filepath.Join(t.TempDir(), "cache"))
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("generated plugin tests: %v\n%s", err, output)
	}
}

func TestPluginInitLivesUnderMarketplace(t *testing.T) {
	root, home := t.TempDir(), t.TempDir()
	destination := filepath.Join(root, "wuko-plugin-acme")
	command := marketplaceTestCommand(root, home, nil)
	command.SetArgs([]string{"marketplace", "plugin", "init", "acme", destination})
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(destination, "plugin.json")); err != nil {
		t.Fatalf("plugin scaffold was not created: %v", err)
	}
	command = marketplaceTestCommand(root, home, nil)
	pluginCommand, _, err := command.Find([]string{"plugin"})
	if err != nil {
		t.Fatal(err)
	}
	for _, child := range pluginCommand.Commands() {
		if child.Name() == "init" {
			t.Fatal("legacy plugin init command remains available")
		}
	}
}

func TestMarketplacePluginSelectionFiltersPlatformsAndRejectsExplicitMismatch(t *testing.T) {
	plugins := []workflow.MarketplacePluginPackage{
		{Namespace: "compatible", PluginVersion: "1", Path: "plugins/compatible/plugin.json", Platforms: []workflow.MarketplacePlatform{{OS: runtime.GOOS, Arch: runtime.GOARCH}}},
		{Namespace: "incompatible", PluginVersion: "1", Path: "plugins/incompatible/plugin.json", Platforms: []workflow.MarketplacePlatform{{OS: "plan9", Arch: "amd64"}}},
	}
	command := &cobra.Command{}
	command.SetIn(bytes.NewReader(nil))
	command.SetOut(io.Discard)
	var options []tui.Option
	deps := dependencies{
		isInteractive: func(io.Reader) bool { return true },
		selectMany: func(_ context.Context, _ io.Reader, _ io.Writer, _ string, got []tui.Option) ([]int, error) {
			options = got
			return []int{0}, nil
		},
	}
	selected, err := selectMarketplacePlugins(command, deps, plugins, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(options) != 1 || options[0].Label != "compatible" {
		t.Fatalf("options = %#v", options)
	}
	if _, ok := selected[0]; !ok {
		t.Fatalf("selected = %#v", selected)
	}
	_, err = selectMarketplacePlugins(command, deps, plugins, []string{"incompatible"})
	if err == nil || !strings.Contains(err.Error(), runtime.GOOS+"/"+runtime.GOARCH) {
		t.Fatalf("error = %v", err)
	}
}

func TestPluginInstallFromMarketplaceVerifiesPinnedManifest(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	archive := pluginTestArchive(t, "wuko-plugin-acme", []byte("#!/bin/sh\nWUKO_CMD_PLUGIN_HELPER=1 exec \""+executable+"\"\n"))
	archiveDigest := sha256.Sum256(archive)
	pluginManifest, err := json.Marshal(pluginpkg.Manifest{
		Version: 1, Namespace: "acme", PluginVersion: "1.0.0", Protocol: pluginpkg.Protocol,
		Artifacts: []pluginpkg.Artifact{{OS: runtime.GOOS, Arch: runtime.GOARCH, Path: "plugin.tar.gz", Format: "tar.gz", Entry: "wuko-plugin-acme", SHA256: fmt.Sprintf("%x", archiveDigest[:])}},
	})
	if err != nil {
		t.Fatal(err)
	}
	manifestDigest := sha256.Sum256(pluginManifest)
	marketplaceManifest, err := json.Marshal(workflow.MarketplaceManifest{
		Version: workflow.MarketplaceManifestVersion, Packages: []workflow.MarketplacePackage{},
		Plugins: []workflow.MarketplacePluginPackage{{
			Namespace: "acme", PluginVersion: "1.0.0", Source: ".wuko/plugin-sources/acme", SourceSHA256: strings.Repeat("a", 64),
			Path: "plugins/acme/plugin.json", SHA256: fmt.Sprintf("%x", manifestDigest[:]), Platforms: []workflow.MarketplacePlatform{{OS: runtime.GOOS, Arch: runtime.GOARCH}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: commandRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		var response *http.Response
		switch request.URL.Path {
		case "/repo/manifest.json":
			response = commandTestResponse(http.StatusOK, string(marketplaceManifest))
		case "/repo/plugins/acme/plugin.json":
			response = commandTestResponse(http.StatusOK, string(pluginManifest))
		case "/repo/plugins/acme/plugin.tar.gz":
			response = &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Body: io.NopCloser(bytes.NewReader(archive)), Header: make(http.Header)}
		default:
			response = commandTestResponse(http.StatusNotFound, "")
		}
		response.Request = request
		return response, nil
	})}
	root, home := t.TempDir(), t.TempDir()
	var output bytes.Buffer
	command := newRootCmd(dependencies{
		stdin: bytes.NewReader(nil), stdout: &output, stderr: &bytes.Buffer{}, httpClient: client,
		cwd: func() (string, error) { return root, nil }, homeDir: func() (string, error) { return home, nil }, configDir: func() (string, error) { return filepath.Join(home, "config"), nil },
		loader: workflow.NewLoader(client), isInteractive: func(io.Reader) bool { return false },
	})
	command.SetArgs([]string{"plugin", "install", "--package", "acme", "https://example.test/repo"})
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := pluginpkg.ValidateInstallation(filepath.Join(root, ".wuko", "plugins", "acme"), "acme"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "Installed plugin acme 1.0.0") {
		t.Fatalf("output = %q", output.String())
	}
}

func TestGoPluginScaffoldBuildsCompleteRelease(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "wuko-plugin-acme")
	if err := writeGoPluginScaffold(directory, "acme"); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("go", "run", "./tools/release", "-version", "1.2.3")
	command.Dir = directory
	command.Env = append(command.Environ(), "GOCACHE="+filepath.Join(t.TempDir(), "cache"))
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("generated release helper: %v\n%s", err, output)
	}
	bundle, err := pluginpkg.LoadBundle(directory)
	if err != nil {
		t.Fatal(err)
	}
	if bundle.Manifest.PluginVersion != "1.2.3" || len(bundle.Manifest.Artifacts) != 4 {
		t.Fatalf("manifest = %#v", bundle.Manifest)
	}
	firstDigest := pluginpkg.DigestBundle(bundle)
	command = exec.Command("go", "run", "./tools/release", "-version", "1.2.3")
	command.Dir = directory
	command.Env = append(command.Environ(), "GOCACHE="+filepath.Join(t.TempDir(), "cache"))
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("second generated release: %v\n%s", err, output)
	}
	second, err := pluginpkg.LoadBundle(directory)
	if err != nil {
		t.Fatal(err)
	}
	if pluginpkg.DigestBundle(second) != firstDigest {
		t.Fatal("release helper did not produce deterministic output")
	}
	data, err := json.Marshal(bundle.Manifest)
	if err != nil || len(data) == 0 {
		t.Fatalf("marshaling generated manifest: %v", err)
	}
}
