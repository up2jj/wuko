package plugin

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/workflow"
)

func TestMain(main *testing.M) {
	if os.Getenv("WUKO_PLUGIN_TEST_HELPER") == "1" {
		runProtocolHelper()
		os.Exit(0)
	}
	os.Exit(main.Run())
}

func runProtocolHelper() {
	scanner := bufio.NewScanner(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	for scanner.Scan() {
		var request requestFrame
		if json.Unmarshal(scanner.Bytes(), &request) != nil {
			os.Exit(2)
		}
		if request.ID == "" {
			continue
		}
		var result any = map[string]any{}
		switch request.Method {
		case "initialize":
			result = initializeResult{Protocol: Protocol, Namespace: "acme", Lifecycle: true, Steps: []stepDeclaration{{Type: "acme.uppercase"}}, Helpers: []helperDeclaration{{Name: "slug"}, {Name: "nullable"}}}
		case "plugin.start":
			parameters, _ := request.Params.(map[string]any)
			with, _ := parameters["with"].(map[string]any)
			fmt.Fprintf(os.Stderr, "started:%v\n", with["mode"])
		case "plugin.stop":
			fmt.Fprintln(os.Stderr, "stopped")
		case "step.run":
			result = step.Result{Outputs: map[string]any{"value": "HELLO"}}
		case "helper.call":
			parameters, _ := request.Params.(map[string]any)
			if parameters["name"] == "nullable" {
				result = map[string]any{"value": nil}
				break
			}
			arguments, _ := parameters["args"].([]any)
			result = map[string]any{"value": strings.ToLower(strings.ReplaceAll(arguments[0].(string), " ", "-"))}
		case "shutdown":
			_ = encoder.Encode(responseFrame{ID: request.ID, Result: json.RawMessage(`{}`)})
			return
		}
		data, _ := json.Marshal(result)
		_ = encoder.Encode(responseFrame{ID: request.ID, Result: data})
	}
}

func TestWorkflowPluginHelpersStartAndCall(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	script := []byte("#!/bin/sh\nWUKO_PLUGIN_TEST_HELPER=1 exec \"" + executable + "\"\n")
	archive := makeArchive(t, "wuko-plugin-acme", 0755, script)
	manifestData, _ := json.Marshal(Manifest{Version: 1, Namespace: "acme", PluginVersion: "1.0.0", Protocol: Protocol, Artifacts: []Artifact{{OS: runtime.GOOS, Arch: runtime.GOARCH, Path: "plugin.tar.gz", Format: "tar.gz", Entry: "wuko-plugin-acme", SHA256: digest(archive)}}})
	var diagnostics synchronizedBuffer
	manager := NewManager(Config{HTTPClient: &http.Client{Transport: memoryTransport{manifest: manifestData, archive: archive}}, Stderr: &diagnostics})
	sources := map[string]workflow.PluginSource{"acme": {Source: "https://plugins.test/plugin.json", SHA256: digest(manifestData), With: map[string]any{"mode": "helpers"}}}
	helpers, err := manager.LoadHelpers(t.Context(), sources)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(diagnostics.String(), "started") {
		t.Fatal("loading declarations called plugin.start")
	}
	value, err := helpers["acme_slug"](t.Context(), []any{"Hello World"})
	if err != nil {
		t.Fatal(err)
	}
	if value != "hello-world" {
		t.Fatalf("helper value = %#v", value)
	}
	value, err = helpers["acme_nullable"](t.Context(), nil)
	if err != nil || value != nil {
		t.Fatalf("nullable helper value = %#v, error = %v", value, err)
	}
	if err := manager.Close(t.Context(), "completed"); err != nil {
		t.Fatal(err)
	}
	if strings.Count(diagnostics.String(), "started:helpers") != 1 || strings.Count(diagnostics.String(), "stopped") != 1 {
		t.Fatalf("lifecycle diagnostics: %q", diagnostics.String())
	}
}

func TestPluginHelperDeclarations(t *testing.T) {
	if got := exposedHelperName("acme-tools", "slug"); got != "acme_tools_slug" {
		t.Fatalf("exposed helper name = %q", got)
	}
	for _, test := range []struct {
		name      string
		namespace string
		helpers   []helperDeclaration
	}{
		{name: "invalid", namespace: "acme", helpers: []helperDeclaration{{Name: "Bad-Name"}}},
		{name: "duplicate", namespace: "acme", helpers: []helperDeclaration{{Name: "slug"}, {Name: "slug"}}},
		{name: "built-in collision", namespace: "parse", helpers: []helperDeclaration{{Name: "time"}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := validateInitializeDeclarations(test.namespace, initializeResult{Helpers: test.helpers})
			if err == nil {
				t.Fatal("expected invalid helper declaration")
			}
		})
	}
}

func TestPluginDeclarationConflictsReportIdentityWithoutConfigurationValues(t *testing.T) {
	manager := NewManager(Config{})
	digestA := strings.Repeat("a", 64)
	digestB := strings.Repeat("b", 64)
	if err := manager.configureSources(map[string]workflow.PluginSource{"acme": {Source: "github:acme/plugin@v1", SHA256: digestA, With: map[string]any{"token": "first-secret"}}}); err != nil {
		t.Fatal(err)
	}
	err := manager.configureSources(map[string]workflow.PluginSource{"acme": {Source: "github:acme/plugin@v2", SHA256: digestB, With: map[string]any{"token": "second-secret"}}})
	if err == nil || !strings.Contains(err.Error(), "github:acme/plugin@v1:plugin.json") || !strings.Contains(err.Error(), digestA[:12]) || strings.Contains(err.Error(), "secret") {
		t.Fatalf("source conflict error = %v", err)
	}

	manager = NewManager(Config{})
	base := workflow.PluginSource{Source: "github:acme/plugin@v1", SHA256: digestA, With: map[string]any{"token": "first-secret"}}
	if err := manager.configureSources(map[string]workflow.PluginSource{"acme": base}); err != nil {
		t.Fatal(err)
	}
	base.With = map[string]any{"token": "second-secret"}
	err = manager.configureSources(map[string]workflow.PluginSource{"acme": base})
	if err == nil || !strings.Contains(err.Error(), "conflicting workflow configuration") || strings.Contains(err.Error(), "secret") {
		t.Fatalf("configuration conflict error = %v", err)
	}
}

func TestExecutorRunResultUsesLanguageNeutralFieldNames(t *testing.T) {
	var wire executorRunResult
	if err := json.Unmarshal([]byte(`{"stdout":"out","stderr":"err","exit_code":7,"stdout_truncated":true,"stderr_truncated":true}`), &wire); err != nil {
		t.Fatal(err)
	}
	result := wire.processResult()
	if result.Stdout != "out" || result.Stderr != "err" || result.ExitCode != 7 || !result.StdoutTruncated || !result.StderrTruncated {
		t.Fatalf("result = %#v", result)
	}
}

type memoryTransport struct{ manifest, archive []byte }

type synchronizedBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (buffer *synchronizedBuffer) Write(data []byte) (int, error) {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.buffer.Write(data)
}
func (buffer *synchronizedBuffer) String() string {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.buffer.String()
}

func (transport memoryTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	data := transport.manifest
	if strings.HasSuffix(request.URL.Path, "plugin.tar.gz") {
		data = transport.archive
	}
	return &http.Response{StatusCode: 200, Status: "200 OK", Body: io.NopCloser(bytes.NewReader(data)), Header: make(http.Header), Request: request}, nil
}

func TestWorkflowPluginStartAndStopOnce(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	script := []byte("#!/bin/sh\nWUKO_PLUGIN_TEST_HELPER=1 exec \"" + executable + "\"\n")
	archive := makeArchive(t, "wuko-plugin-acme", 0755, script)
	manifestData, _ := json.Marshal(Manifest{Version: 1, Namespace: "acme", PluginVersion: "1.0.0", Protocol: Protocol, Artifacts: []Artifact{{OS: runtime.GOOS, Arch: runtime.GOARCH, Path: "plugin.tar.gz", Format: "tar.gz", Entry: "wuko-plugin-acme", SHA256: digest(archive)}}})
	var diagnostics synchronizedBuffer
	manager := NewManager(Config{HTTPClient: &http.Client{Transport: memoryTransport{manifest: manifestData, archive: archive}}, Stderr: &diagnostics})
	registry := step.NewRegistry(step.WithResolver(manager.ResolveStep))
	ctx := workflow.ContextWithPlugins(context.Background(), map[string]workflow.PluginSource{"acme": {Source: "https://plugins.test/plugin.json", SHA256: digest(manifestData), With: map[string]any{"mode": "test"}}})
	runner, err := registry.BuildContext(ctx, "acme.uppercase", map[string]any{"value": "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.(step.Validator).Validate(ctx, step.Request{}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(diagnostics.String(), "started") {
		t.Fatal("validation called plugin.start")
	}
	for range 2 {
		result, err := runner.Run(ctx, step.Request{})
		if err != nil {
			t.Fatal(err)
		}
		if result.Outputs["value"] != "HELLO" {
			t.Fatal(result)
		}
	}
	if err := manager.Close(ctx, "completed"); err != nil {
		t.Fatal(err)
	}
	if strings.Count(diagnostics.String(), "started:test") != 1 || strings.Count(diagnostics.String(), "stopped") != 1 {
		t.Fatalf("lifecycle diagnostics: %q", diagnostics.String())
	}
}

func TestLocalResolutionPrecedence(t *testing.T) {
	root := t.TempDir()
	cwd := filepath.Join(root, "project", "nested")
	home := filepath.Join(root, "home")
	config := filepath.Join(root, "config")
	if err := os.MkdirAll(cwd, 0700); err != nil {
		t.Fatal(err)
	}
	makeExecutable := func(directory string) string {
		t.Helper()
		if err := os.MkdirAll(directory, 0700); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(directory, "wuko-plugin-acme")
		if err := os.WriteFile(target, []byte("x"), 0700); err != nil {
			t.Fatal(err)
		}
		return target
	}
	projectPath := makeExecutable(filepath.Join(root, "project", ".wuko", "plugins", "acme"))
	homePath := makeExecutable(filepath.Join(home, ".wuko", "plugins", "acme"))
	configPath := makeExecutable(filepath.Join(config, "wuko", "plugins", "acme"))
	pathPath := makeExecutable(filepath.Join(root, "path"))
	manager := NewManager(Config{CWD: func() (string, error) { return cwd, nil }, HomeDir: func() (string, error) { return home, nil }, ConfigDir: func() (string, error) { return config, nil }, LookPath: func(string) (string, error) { return pathPath, nil }})
	for _, expected := range []string{projectPath, homePath, configPath, pathPath} {
		actual, err := manager.findLocal("acme")
		if err != nil {
			t.Fatal(err)
		}
		if actual != expected {
			t.Fatalf("path = %s, want %s", actual, expected)
		}
		if expected != pathPath {
			if err := os.RemoveAll(filepath.Dir(expected)); err != nil {
				t.Fatal(err)
			}
		}
	}
}
