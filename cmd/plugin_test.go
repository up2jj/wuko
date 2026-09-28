package cmd

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"go/format"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

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
	useLocalWukoModule(t, directory)
	command := exec.Command("go", "test", "./...")
	command.Dir = directory
	command.Env = append(command.Environ(), "GOCACHE="+filepath.Join(t.TempDir(), "cache"))
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("generated plugin tests: %v\n%s", err, output)
	}
}

func useLocalWukoModule(t *testing.T, directory string) {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locating repository root")
	}
	repository := filepath.Dir(filepath.Dir(source))
	command := exec.Command("go", "mod", "edit", "-replace=github.com/up2jj/wuko="+repository)
	command.Dir = directory
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("using local Wuko module: %v\n%s", err, output)
	}
}

func TestGoPluginScaffoldIsFormatted(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "wuko-plugin-acme")
	if err := writeGoPluginScaffold(directory, "acme"); err != nil {
		t.Fatal(err)
	}
	err := filepath.WalkDir(directory, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !strings.HasSuffix(path, ".go") {
			return err
		}
		source, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		formatted, err := format.Source(source)
		if err != nil {
			return fmt.Errorf("%s does not parse: %w", path, err)
		}
		if !bytes.Equal(source, formatted) {
			t.Errorf("%s is not gofmt-clean; the scaffold ships it as a plugin author's starting point", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestGoPluginScaffoldUsesNewestProtocolEverywhere(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "wuko-plugin-acme")
	if err := writeGoPluginScaffold(directory, "acme"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"plugin.json", "README.md"} {
		data, err := os.ReadFile(filepath.Join(directory, filepath.FromSlash(name)))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), pluginpkg.Protocol) && name != "README.md" {
			t.Fatalf("%s does not use newest protocol %q", name, pluginpkg.Protocol)
		}
		if strings.Contains(string(data), pluginpkg.ProtocolV1) {
			t.Fatalf("%s still references old scaffold protocol", name)
		}
	}
	for _, name := range []string{"main.go", "tools/release/main.go"} {
		data, err := os.ReadFile(filepath.Join(directory, filepath.FromSlash(name)))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), "github.com/up2jj/wuko/plugin/sdk") {
			t.Fatalf("%s does not use the Go SDK", name)
		}
	}
}

func TestGoPluginScaffoldPinsSDKAndOmitsTransport(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "wuko-plugin-acme")
	if err := writeGoPluginScaffold(directory, "acme"); err != nil {
		t.Fatal(err)
	}
	module, err := os.ReadFile(filepath.Join(directory, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	want := "github.com/up2jj/wuko " + scaffoldSDKVersion()
	if !strings.Contains(string(module), want) {
		t.Fatalf("go.mod = %q, want dependency %q", module, want)
	}
	if _, err := os.Stat(filepath.Join(directory, "protocol.go")); !os.IsNotExist(err) {
		t.Fatalf("hand-written protocol.go still exists: %v", err)
	}
	// The scaffold ships a require without a go.sum, so every recipe that compiles has to resolve
	// the pinned SDK first; otherwise the first `just build` dies on a missing go.sum entry.
	recipes, err := os.ReadFile(filepath.Join(directory, "justfile"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(recipes), "deps:\n\tgo mod tidy\n") {
		t.Fatalf("justfile = %q, want a go mod tidy recipe", recipes)
	}
	for _, recipe := range []string{"build: deps", "test: deps", "vet: deps", "release version: deps"} {
		if !strings.Contains(string(recipes), recipe) {
			t.Fatalf("justfile = %q, want %q", recipes, recipe)
		}
	}
}

func TestResolveScaffoldSDKVersion(t *testing.T) {
	tests := []struct {
		name, buildVersion, cliVersion, want string
	}{
		{name: "module build", buildVersion: "v1.2.3", cliVersion: "v1.2.2", want: "v1.2.3"},
		{name: "release injection", buildVersion: "(devel)", cliVersion: "v1.2.3-rc.1", want: "v1.2.3-rc.1"},
		{name: "git describe fallback", buildVersion: "(devel)", cliVersion: "v1.2.3-4-gabc1234", want: scaffoldSDKFallbackVersion},
		{name: "source fallback", buildVersion: "(devel)", cliVersion: "dev", want: scaffoldSDKFallbackVersion},
		{name: "pseudo-version fallback", buildVersion: "v1.2.4-0.20260928150208-c48a8da14c1a", cliVersion: "v1.2.3-4-gabc1234", want: scaffoldSDKFallbackVersion},
		{name: "dirty pseudo-version fallback", buildVersion: "v1.2.4-0.20260928150208-c48a8da14c1a+dirty", cliVersion: "dev", want: scaffoldSDKFallbackVersion},
		{name: "snapshot fallback", buildVersion: "(devel)", cliVersion: "v1.2.4-next", want: scaffoldSDKFallbackVersion},
		{name: "dirty tag fallback", buildVersion: "(devel)", cliVersion: "v1.2.3-dirty", want: scaffoldSDKFallbackVersion},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := resolveScaffoldSDKVersion(test.buildVersion, test.cliVersion); got != test.want {
				t.Fatalf("version = %q, want %q", got, test.want)
			}
		})
	}
}

func TestGoPluginScaffoldCancelsExecutorProcessGroup(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "wuko-plugin-acme")
	if err := writeGoPluginScaffold(directory, "acme"); err != nil {
		t.Fatal(err)
	}
	useLocalWukoModule(t, directory)
	binary := filepath.Join(directory, "wuko-plugin-acme")
	build := exec.Command("go", "build", "-o", binary, ".")
	build.Dir = directory
	build.Env = append(build.Environ(), "GOCACHE="+filepath.Join(t.TempDir(), "cache"))
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building generated plugin: %v\n%s", err, output)
	}
	command := exec.Command(binary)
	stdin, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	command.Stderr = io.Discard
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = command.Process.Kill() }()
	encoder := json.NewEncoder(stdin)
	scanner := bufio.NewScanner(stdout)
	readFrame := func() map[string]any {
		t.Helper()
		if !scanner.Scan() {
			t.Fatalf("generated plugin closed output: %v", scanner.Err())
		}
		var frame map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &frame); err != nil {
			t.Fatal(err)
		}
		return frame
	}
	if err := encoder.Encode(map[string]any{"id": "1", "method": "initialize", "params": map[string]any{"protocol": pluginpkg.Protocol}}); err != nil {
		t.Fatal(err)
	}
	if frame := readFrame(); frame["id"] != "1" || frame["error"] != nil {
		t.Fatalf("initialize frame = %#v", frame)
	} else {
		assertScaffoldDeclarations(t, frame["result"])
	}
	if err := encoder.Encode(map[string]any{"id": "open", "method": "executor.open", "params": map[string]any{"type": "acme.local", "with": map[string]any{}, "context": map[string]any{}}}); err != nil {
		t.Fatal(err)
	}
	openFrame := readFrame()
	openResult, ok := openFrame["result"].(map[string]any)
	if openFrame["id"] != "open" || openFrame["error"] != nil || !ok {
		t.Fatalf("executor.open frame = %#v", openFrame)
	}
	session, ok := openResult["session"].(string)
	if !ok || session == "" {
		t.Fatalf("executor.open result = %#v", openResult)
	}
	commandText := "sleep 30 & child=$!; printf '%s' \"$child\"; wait"
	if err := encoder.Encode(map[string]any{"id": "2", "method": "executor.run", "params": map[string]any{"session": session, "command": "sh", "args": []string{"-c", commandText}, "env": map[string]string{}, "stdin": "", "capture_limit": 64, "stdout_policy": 0, "stderr_policy": 3}}); err != nil {
		t.Fatal(err)
	}
	var childPID int
	for childPID == 0 {
		frame := readFrame()
		if frame["event"] != "stdout" {
			continue
		}
		encoded, _ := frame["data"].(string)
		decoded, err := io.ReadAll(base64.NewDecoder(base64.StdEncoding, strings.NewReader(encoded)))
		if err != nil {
			t.Fatal(err)
		}
		childPID, err = strconv.Atoi(string(decoded))
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := encoder.Encode(map[string]any{"method": "cancel", "params": map[string]any{"id": "2"}}); err != nil {
		t.Fatal(err)
	}
	for {
		frame := readFrame()
		if frame["id"] == "2" && frame["event"] == nil {
			break
		}
	}
	deadline := time.Now().Add(2 * time.Second)
	for syscall.Kill(childPID, 0) == nil && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if err := syscall.Kill(childPID, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("executor descendant %d survived cancellation: %v", childPID, err)
	}
	if err := encoder.Encode(map[string]any{"id": "3", "method": "shutdown", "params": map[string]any{}}); err != nil {
		t.Fatal(err)
	}
	if frame := readFrame(); frame["id"] != "3" || frame["error"] != nil {
		t.Fatalf("shutdown frame = %#v", frame)
	}
	_ = stdin.Close()
	if err := command.Wait(); err != nil {
		t.Fatal(err)
	}
}

func assertScaffoldDeclarations(t *testing.T, value any) {
	t.Helper()
	result, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("initialize result = %#v", value)
	}
	if result["protocol"] != pluginpkg.Protocol || result["namespace"] != "acme" || result["lifecycle"] != true {
		t.Fatalf("initialize result = %#v", result)
	}
	containsName := func(value any, field, want string) bool {
		items, _ := value.([]any)
		for _, item := range items {
			declaration, _ := item.(map[string]any)
			if declaration[field] == want {
				return true
			}
		}
		return false
	}
	if !containsName(result["helpers"], "name", "slug") || !containsName(result["steps"], "type", "acme.uppercase") || !containsName(result["executors"], "type", "acme.local") {
		t.Fatalf("initialize declarations = %#v", result)
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
	useLocalWukoModule(t, directory)
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
	if bundle.Manifest.PluginVersion != "1.2.3" || bundle.Manifest.Protocol != pluginpkg.Protocol || len(bundle.Manifest.Artifacts) != 4 {
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
