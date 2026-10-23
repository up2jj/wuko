package cmd

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	pluginpkg "github.com/up2jj/wuko/plugin"
	"github.com/up2jj/wuko/workflow"
)

func commandPluginBundle(t *testing.T, dir string) ([]byte, []byte) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	archive := pluginTestArchive(t, "wuko-plugin-acme", []byte("#!/bin/sh\nWUKO_CMD_PLUGIN_HELPER=1 exec \""+executable+"\"\n"))
	hash := sha256.Sum256(archive)
	manifest, err := json.Marshal(pluginpkg.Manifest{Version: 1, Namespace: "acme", PluginVersion: "1.0.0", Protocol: pluginpkg.Protocol, Artifacts: []pluginpkg.Artifact{{OS: runtime.GOOS, Arch: runtime.GOARCH, Path: "plugin.tar.gz", Format: "tar.gz", Entry: "wuko-plugin-acme", SHA256: fmt.Sprintf("%x", hash)}}})
	if err != nil {
		t.Fatal(err)
	}
	if dir != "" {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		for name, data := range map[string][]byte{"plugin.json": manifest, "plugin.tar.gz": archive} {
			if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	return manifest, archive
}
func pluginWorkflow(source string) []byte {
	return []byte("version: 1\nname: demo\nplugins:\n  acme:\n    source: " + source + "\nsteps:\n  - id: upper\n    type: acme.uppercase\n    with:\n      value: hello\n")
}
func pluginCommandDeps(t *testing.T, dir, cache string, client *http.Client) dependencies {
	t.Helper()
	home, config := t.TempDir(), t.TempDir()
	deps := dependencies{cwd: func() (string, error) { return dir, nil }, homeDir: func() (string, error) { return home, nil }, configDir: func() (string, error) { return config, nil }, cacheDir: func() (string, error) { return cache, nil }, stdin: strings.NewReader(""), stdout: io.Discard, stderr: io.Discard, httpClient: client}
	deps.plugins = pluginpkg.NewManager(pluginpkg.Config{CWD: deps.cwd, HomeDir: deps.homeDir, ConfigDir: deps.configDir, CacheDir: deps.cacheDir, HTTPClient: client})
	deps.loader = workflow.NewLoader(client, workflow.WithPluginHelpers(deps.plugins), workflow.WithPluginResolver(deps.plugins), workflow.WithPluginActions(deps.plugins))
	return deps
}
func executePluginCommand(t *testing.T, deps dependencies, args ...string) error {
	t.Helper()
	command := newRootCmd(deps)
	command.SetArgs(args)
	return command.Execute()
}

func TestPluginLockCommandAndFrozenExecution(t *testing.T) {
	t.Parallel()
	dir, cache := t.TempDir(), t.TempDir()
	workflows := filepath.Join(dir, ".wuko", "workflows")
	commandPluginBundle(t, filepath.Join(workflows, "bundle"))
	path := filepath.Join(workflows, "demo.yaml")
	if err := os.WriteFile(path, pluginWorkflow("./bundle/plugin.json"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"validate", "demo"}, {"tree", "demo"}, {"run", "demo", "--dry-run"}, {"run", "demo", "--frozen-lockfile"}} {
		err := executePluginCommand(t, pluginCommandDeps(t, dir, cache, nil), args...)
		if err == nil || !strings.Contains(err.Error(), "missing plugin pin") {
			t.Fatalf("%v: %v", args, err)
		}
		if _, err := os.Stat(filepath.Join(workflows, "wuko.lock")); !os.IsNotExist(err) {
			t.Fatal("read-only command created lock")
		}
	}
	if err := executePluginCommand(t, pluginCommandDeps(t, dir, cache, nil), "plugin", "lock", filepath.Join(".wuko", "workflows", "demo.yaml")); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(workflows, "wuko.lock"))
	if err != nil {
		t.Fatal(err)
	}
	// Removing the source bundle proves the frozen run uses verified cached bytes.
	if err := os.RemoveAll(filepath.Join(workflows, "bundle")); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"validate", "demo"}, {"tree", "demo"}, {"run", "demo", "--dry-run"}, {"run", "demo", "--frozen-lockfile"}} {
		if err := executePluginCommand(t, pluginCommandDeps(t, dir, cache, nil), args...); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
	}
	after, err := os.ReadFile(filepath.Join(workflows, "wuko.lock"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("frozen/read-only command changed pins")
	}
}

func TestPluginLocalRunAndStdinCreatePins(t *testing.T) {
	t.Parallel()
	for _, stdin := range []bool{false, true} {
		t.Run(fmt.Sprint(stdin), func(t *testing.T) {
			dir, cache := t.TempDir(), t.TempDir()
			commandPluginBundle(t, filepath.Join(dir, "bundle"))
			path := filepath.Join(dir, "workflow.yaml")
			body := pluginWorkflow("./bundle")
			deps := pluginCommandDeps(t, dir, cache, nil)
			if stdin {
				deps.stdin = bytes.NewReader(body)
				path = "-"
			} else if err := os.WriteFile(path, body, 0600); err != nil {
				t.Fatal(err)
			}
			if err := executePluginCommand(t, deps, "run", "--file", path); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(filepath.Join(dir, "wuko.lock"))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Contains(data, []byte("./bundle/plugin.json")) {
				t.Fatalf("lock=%s", data)
			}
		})
	}
}

func TestRemoteRunLeavesExistingLocalLockUntouched(t *testing.T) {
	t.Parallel()
	dir, cache := t.TempDir(), t.TempDir()
	manifest, archive := commandPluginBundle(t, "")
	path := filepath.Join(dir, "wuko.lock")
	original := []byte("malformed local lock must be ignored\n")
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	var manifests, archives, workflows atomic.Int32
	client := &http.Client{Transport: commandRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		var body []byte
		switch request.URL.Path {
		case "/workflow.yaml":
			workflows.Add(1)
			body = pluginWorkflow("https://plugins.test/plugin.json")
		case "/plugin.json":
			manifests.Add(1)
			body = manifest
		case "/plugin.tar.gz":
			archives.Add(1)
			body = archive
		default:
			return nil, fmt.Errorf("unexpected path %s", request.URL.Path)
		}
		return &http.Response{StatusCode: 200, Status: "200 OK", Header: make(http.Header), Request: request, Body: io.NopCloser(bytes.NewReader(body))}, nil
	})}
	for range 2 {
		if err := executePluginCommand(t, pluginCommandDeps(t, dir, cache, client), "run", "https://plugins.test/workflow.yaml"); err != nil {
			t.Fatal(err)
		}
	}
	if manifests.Load() != 2 || archives.Load() != 1 {
		t.Fatalf("manifest=%d archive=%d", manifests.Load(), archives.Load())
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(after, original) {
		t.Fatalf("local lock changed: %s %v", after, err)
	}
	err = executePluginCommand(t, pluginCommandDeps(t, dir, cache, client), "run", "https://plugins.test/workflow.yaml", "--frozen-lockfile")
	if err == nil || !strings.Contains(err.Error(), "only supported for local") {
		t.Fatalf("error=%v", err)
	}
	if workflows.Load() != 2 {
		t.Fatal("frozen remote run downloaded workflow before rejection")
	}
}

func TestInlinePluginSHARequiresExplicitMigration(t *testing.T) {
	t.Parallel()
	dir, cache := t.TempDir(), t.TempDir()
	manifest, _ := commandPluginBundle(t, filepath.Join(dir, "bundle"))
	pin := sha256.Sum256(manifest)
	body := strings.Replace(string(pluginWorkflow("./bundle")), "    source: ./bundle", "    source: ./bundle\n    sha256: "+fmt.Sprintf("%x", pin), 1)
	path := filepath.Join(dir, "demo.yaml")
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	err := executePluginCommand(t, pluginCommandDeps(t, dir, cache, nil), "run", "--file", path)
	if err == nil || !strings.Contains(err.Error(), "--migrate") {
		t.Fatalf("error=%v", err)
	}
	if err := executePluginCommand(t, pluginCommandDeps(t, dir, cache, nil), "plugin", "lock", path, "--migrate"); err != nil {
		t.Fatal(err)
	}
	if err := executePluginCommand(t, pluginCommandDeps(t, dir, cache, nil), "run", "--file", path, "--frozen-lockfile"); err != nil {
		t.Fatal(err)
	}
}

func TestFrozenPolicyReachesDependencyWorkflow(t *testing.T) {
	t.Parallel()
	dir, cache := t.TempDir(), t.TempDir()
	workflows := filepath.Join(dir, ".wuko", "workflows")
	commandPluginBundle(t, filepath.Join(workflows, "bundle"))
	childDir := filepath.Join(workflows, "child")
	if err := os.MkdirAll(childDir, 0700); err != nil {
		t.Fatal(err)
	}
	parent := filepath.Join(workflows, "parent.yaml")
	body := strings.Replace(string(pluginWorkflow("./bundle")), "name: demo", "name: parent\ndepends_on:\n  prerequisite: child", 1)
	if err := os.WriteFile(parent, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	child := filepath.Join(childDir, "wuko.yaml")
	if err := os.WriteFile(filepath.Join(childDir, workflow.WorkflowPackageMarkerName), []byte(`{"version":1,"name":"child"}`), 0600); err != nil {
		t.Fatal(err)
	}
	childBody := strings.Replace(string(pluginWorkflow("../bundle")), "name: demo", "name: child", 1)
	if err := os.WriteFile(child, []byte(childBody), 0600); err != nil {
		t.Fatal(err)
	}
	if err := executePluginCommand(t, pluginCommandDeps(t, dir, cache, nil), "plugin", "lock", parent); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(childDir, "wuko.lock")); err != nil {
		t.Fatal(err)
	}
	err := executePluginCommand(t, pluginCommandDeps(t, dir, cache, nil), "run", "parent", "--frozen-lockfile")
	if err == nil || !strings.Contains(err.Error(), "missing plugin pin") {
		t.Fatalf("error=%v", err)
	}
	if _, err := os.Stat(filepath.Join(childDir, "wuko.lock")); !os.IsNotExist(err) {
		t.Fatal("frozen dependency created lock")
	}
}

func TestCompositeActionInheritsResolvedPluginPins(t *testing.T) {
	t.Parallel()
	dir, cache := t.TempDir(), t.TempDir()
	commandPluginBundle(t, filepath.Join(dir, "bundle"))
	action := []byte("version: 1\nname: nested\nsteps:\n  - id: upper\n    type: acme.uppercase\n    with:\n      value: hello\n")
	if err := os.WriteFile(filepath.Join(dir, "action.yaml"), action, 0600); err != nil {
		t.Fatal(err)
	}
	main := []byte("version: 1\nname: caller\nplugins:\n  acme:\n    source: ./bundle\nsteps:\n  - id: nested\n    uses: ./action.yaml\n")
	path := filepath.Join(dir, "workflow.yaml")
	if err := os.WriteFile(path, main, 0600); err != nil {
		t.Fatal(err)
	}
	if err := executePluginCommand(t, pluginCommandDeps(t, dir, cache, nil), "run", "--file", path); err != nil {
		t.Fatal(err)
	}
}

func TestMaterializedRemoteKeepsRemotePluginPolicy(t *testing.T) {
	t.Parallel()
	dir, cache, snapshot := t.TempDir(), t.TempDir(), t.TempDir()
	manifest, archive := commandPluginBundle(t, "")
	path := filepath.Join(snapshot, "wuko.yaml")
	if err := os.WriteFile(path, pluginWorkflow("https://plugins.test/plugin.json"), 0600); err != nil {
		t.Fatal(err)
	}
	var manifests atomic.Int32
	client := &http.Client{Transport: commandRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		body := archive
		if request.URL.Path == "/plugin.json" {
			body = manifest
			manifests.Add(1)
		}
		return &http.Response{StatusCode: 200, Status: "200 OK", Header: make(http.Header), Request: request, Body: io.NopCloser(bytes.NewReader(body))}, nil
	})}
	deps := pluginCommandDeps(t, dir, cache, client)
	t.Cleanup(func() { _ = deps.plugins.Close(context.Background(), "completed") })
	if _, err := deps.loader.LoadMaterializedRemote(t.Context(), path, "https://example.test/wuko.yaml", workflow.LoadOptions{RunDir: dir}); err != nil {
		t.Fatal(err)
	}
	frozen := workflow.ContextWithPluginLockPolicy(t.Context(), workflow.PluginLockPolicy{Frozen: true})
	if _, err := deps.loader.DecodeMaterializedRemote(frozen, path, "https://example.test/wuko.yaml", workflow.LoadOptions{}); err == nil {
		t.Fatal("frozen materialized remote workflow accepted")
	}

	if manifests.Load() != 1 {
		t.Fatalf("manifest requests=%d", manifests.Load())
	}
	for _, root := range []string{dir, snapshot} {
		if _, err := os.Stat(filepath.Join(root, "wuko.lock")); !os.IsNotExist(err) {
			t.Fatal("materialized remote workflow wrote lock")
		}
	}
}

func TestRemoteInvocationDoesNotMutateDependencyLock(t *testing.T) {
	t.Parallel()
	dir, cache := t.TempDir(), t.TempDir()
	workflows := filepath.Join(dir, ".wuko", "workflows")
	if err := os.MkdirAll(workflows, 0700); err != nil {
		t.Fatal(err)
	}
	child := strings.Replace(string(pluginWorkflow("https://plugins.test/plugin.json")), "name: demo", "name: child", 1)
	if err := os.WriteFile(filepath.Join(workflows, "child.yaml"), []byte(child), 0600); err != nil {
		t.Fatal(err)
	}
	manifest, archive := commandPluginBundle(t, "")
	remote := strings.Replace(string(pluginWorkflow("https://plugins.test/plugin.json")), "name: demo", "name: remote\ndepends_on:\n  prerequisite: child", 1)
	client := &http.Client{Transport: commandRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		var body []byte
		switch request.URL.Path {
		case "/workflow.yaml":
			body = []byte(remote)
		case "/plugin.json":
			body = manifest
		case "/plugin.tar.gz":
			body = archive
		default:
			return nil, fmt.Errorf("unexpected path %s", request.URL.Path)
		}
		return &http.Response{StatusCode: 200, Status: "200 OK", Header: make(http.Header), Request: request, Body: io.NopCloser(bytes.NewReader(body))}, nil
	})}
	if err := executePluginCommand(t, pluginCommandDeps(t, dir, cache, client), "run", "https://plugins.test/workflow.yaml"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(workflows, "wuko.lock")); !os.IsNotExist(err) {
		t.Fatal("remote invocation modified dependency lock")
	}
}

func TestMarketplaceBuildRequiresCommittedPluginPins(t *testing.T) {
	t.Parallel()
	dir, cache := t.TempDir(), t.TempDir()
	commandPluginBundle(t, filepath.Join(dir, "bundle"))
	packageDir := filepath.Join(dir, ".wuko", "workflows", "demo")
	if err := os.MkdirAll(packageDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(packageDir, workflow.WorkflowPackageMarkerName), []byte(`{"version":1,"name":"demo"}`), 0600); err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(packageDir, "wuko.yaml")
	if err := os.WriteFile(manifest, pluginWorkflow("../../../bundle"), 0600); err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(packageDir, "wuko.lock")
	err := executePluginCommand(t, pluginCommandDeps(t, dir, cache, nil), "marketplace", "build")
	if err == nil || !strings.Contains(err.Error(), "missing plugin pin") {
		t.Fatalf("unlocked build error = %v", err)
	}
	if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
		t.Fatal("marketplace build created a lock in the package source")
	}
	if err := executePluginCommand(t, pluginCommandDeps(t, dir, cache, nil), "plugin", "lock", manifest); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := executePluginCommand(t, pluginCommandDeps(t, dir, cache, nil), "marketplace", "build"); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("marketplace build rewrote the committed lock")
	}
}
