package cmd

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	pluginpkg "github.com/up2jj/wuko/plugin"
	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/tui"
	"github.com/up2jj/wuko/workflow"
)

func TestMarketplaceInitAndIncrementalBuild(t *testing.T) {
	root, home := t.TempDir(), t.TempDir()
	workflowRoot := filepath.Join(root, ".wuko", "workflows")
	writeMarketplacePackage(t, filepath.Join(workflowRoot, "release"), "release", "Release", `{"target":"staging"}`, "")
	writeMarketplacePackage(t, filepath.Join(workflowRoot, "nested", "publish"), "publish", "Publish", `{"target":"production"}`, "")

	command := marketplaceTestCommand(root, home, nil)
	command.SetArgs([]string{"marketplace", "init"})
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	pluginSource := filepath.Join(t.TempDir(), "plugin")
	writeMarketplacePluginRelease(t, pluginSource, "acme", "1.0.0")
	command = marketplaceTestCommand(root, home, nil)
	command.SetArgs([]string{"marketplace", "plugin", "add", pluginSource})
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
	if manifest.Version != workflow.MarketplaceManifestVersion || len(manifest.Packages) != 2 || len(manifest.Plugins) != 1 {
		t.Fatalf("manifest = %#v", manifest)
	}
	if manifest.Packages[0].PackageVersion != "1.0.0" || manifest.Packages[1].PackageVersion != "1.0.0" {
		t.Fatalf("package versions = %#v", manifest.Packages)
	}
	paths := []string{manifest.Packages[0].Path, manifest.Packages[1].Path}
	if !slices.Equal(paths, []string{"packages/publish.tar.gz", "packages/release.tar.gz"}) {
		t.Fatalf("paths = %#v", paths)
	}
	for _, item := range manifest.Packages {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(item.Path))); err != nil {
			t.Fatalf("archive %s: %v", item.Path, err)
		}
	}
	publishArchive := filepath.Join(root, "packages", "publish.tar.gz")
	releaseArchive := filepath.Join(root, "packages", "release.tar.gz")
	publishBefore, err := os.ReadFile(publishArchive)
	if err != nil {
		t.Fatal(err)
	}
	releaseBefore, err := os.ReadFile(releaseArchive)
	if err != nil {
		t.Fatal(err)
	}
	manifestInfoBefore, err := os.Stat(filepath.Join(root, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	archiveInfoBefore, err := os.Stat(publishArchive)
	if err != nil {
		t.Fatal(err)
	}

	command = marketplaceTestCommand(root, home, nil)
	command.SetArgs([]string{"marketplace", "build"})
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	publishAfter, _ := os.ReadFile(publishArchive)
	releaseAfter, _ := os.ReadFile(releaseArchive)
	manifestInfoAfter, _ := os.Stat(filepath.Join(root, "manifest.json"))
	archiveInfoAfter, _ := os.Stat(publishArchive)
	if !bytes.Equal(publishBefore, publishAfter) || !bytes.Equal(releaseBefore, releaseAfter) {
		t.Fatal("no-op build changed an archive")
	}
	if !manifestInfoBefore.ModTime().Equal(manifestInfoAfter.ModTime()) || !archiveInfoBefore.ModTime().Equal(archiveInfoAfter.ModTime()) {
		t.Fatal("no-op build changed timestamps")
	}

	if err := os.WriteFile(filepath.Join(workflowRoot, "release", "defaults.json"), []byte(`{"target":"production"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	command = marketplaceTestCommand(root, home, nil)
	command.SetArgs([]string{"marketplace", "build"})
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	var changed workflow.MarketplaceManifest
	data, err = os.ReadFile(filepath.Join(root, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &changed); err != nil {
		t.Fatal(err)
	}
	changedByName := make(map[string]workflow.MarketplacePackage, len(changed.Packages))
	for _, item := range changed.Packages {
		changedByName[item.Name] = item
	}
	manifestByName := make(map[string]workflow.MarketplacePackage, len(manifest.Packages))
	for _, item := range manifest.Packages {
		manifestByName[item.Name] = item
	}
	if changedByName["release"].SHA256 == manifestByName["release"].SHA256 || changedByName["release"].SourceSHA256 == manifestByName["release"].SourceSHA256 {
		t.Fatal("changed package was not rebuilt")
	}
	if changedByName["publish"].SHA256 != manifestByName["publish"].SHA256 {
		t.Fatal("unchanged package was rebuilt")
	}

	if err := os.RemoveAll(filepath.Join(workflowRoot, "nested")); err != nil {
		t.Fatal(err)
	}
	command = marketplaceTestCommand(root, home, nil)
	command.SetArgs([]string{"marketplace", "build"})
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(publishArchive); !os.IsNotExist(err) {
		t.Fatalf("stale archive remains: %v", err)
	}

	command = marketplaceTestCommand(root, home, nil)
	command.SetArgs([]string{"marketplace", "init"})
	if err := command.ExecuteContext(t.Context()); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("second init error = %v", err)
	}
}

func TestMarketplacePluginAddBuildAndCheck(t *testing.T) {
	root, home := t.TempDir(), t.TempDir()
	source := filepath.Join(t.TempDir(), "plugin")
	writeMarketplacePluginRelease(t, source, "acme", "1.0.0")

	command := marketplaceTestCommand(root, home, nil)
	command.SetArgs([]string{"marketplace", "init"})
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	command = marketplaceTestCommand(root, home, nil)
	command.SetArgs([]string{"marketplace", "plugin", "add", "--description", "Acme tools", source})
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	imported := filepath.Join(root, ".wuko", "plugin-sources", "acme")
	if _, err := pluginpkg.LoadBundle(imported); err != nil {
		t.Fatalf("imported bundle: %v", err)
	}
	// An import must never land in the local plugin installation root: it holds no executable, so
	// local discovery would fail for the namespace here and in every directory below.
	if _, err := os.Stat(filepath.Join(root, ".wuko", "plugins")); !os.IsNotExist(err) {
		t.Fatalf("import used the local plugin installation root: %v", err)
	}

	command = marketplaceTestCommand(root, home, nil)
	command.SetArgs([]string{"marketplace", "build"})
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest workflow.MarketplaceManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Version != workflow.MarketplaceManifestVersion || len(manifest.Plugins) != 1 || manifest.Plugins[0].Namespace != "acme" || manifest.Plugins[0].Description != "Acme tools" {
		t.Fatalf("manifest = %#v", manifest)
	}
	if _, err := pluginpkg.LoadBundle(filepath.Join(root, "plugins", "acme")); err != nil {
		t.Fatalf("published bundle: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "plugins", "acme", marketplacePluginSourceName)); !os.IsNotExist(err) {
		t.Fatalf("source metadata was published: %v", err)
	}

	command = marketplaceTestCommand(root, home, nil)
	command.SetArgs([]string{"marketplace", "build", "--check"})
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	metadata, err := readMarketplacePluginSource(imported)
	if err != nil {
		t.Fatal(err)
	}
	metadata.Description = "Changed"
	if err := writeJSONAtomically(filepath.Join(imported, marketplacePluginSourceName), metadata); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(root, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	command = marketplaceTestCommand(root, home, nil)
	command.SetArgs([]string{"marketplace", "build", "--check"})
	if err := command.ExecuteContext(t.Context()); err == nil || !strings.Contains(err.Error(), "not up to date") {
		t.Fatalf("check error = %v", err)
	}
	after, err := os.ReadFile(filepath.Join(root, "manifest.json"))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("check modified manifest: %v", err)
	}
}

func TestMarketplacePluginUpdateRequiresMatchingNamespaceAndPreservesImport(t *testing.T) {
	root, home := t.TempDir(), t.TempDir()
	acme := filepath.Join(t.TempDir(), "acme")
	other := filepath.Join(t.TempDir(), "other")
	writeMarketplacePluginRelease(t, acme, "acme", "1.0.0")
	writeMarketplacePluginRelease(t, other, "other", "2.0.0")
	command := marketplaceTestCommand(root, home, nil)
	command.SetArgs([]string{"marketplace", "plugin", "add", acme})
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	manifestBefore, err := os.ReadFile(filepath.Join(root, ".wuko", "plugin-sources", "acme", "plugin.json"))
	if err != nil {
		t.Fatal(err)
	}
	command = marketplaceTestCommand(root, home, nil)
	command.SetArgs([]string{"marketplace", "plugin", "update", "acme", other})
	if err := command.ExecuteContext(t.Context()); err == nil || !strings.Contains(err.Error(), "conflicts") {
		t.Fatalf("update error = %v", err)
	}
	manifestAfter, err := os.ReadFile(filepath.Join(root, ".wuko", "plugin-sources", "acme", "plugin.json"))
	if err != nil || !bytes.Equal(manifestBefore, manifestAfter) {
		t.Fatalf("failed update changed import: %v", err)
	}
}

func TestMarketplacePluginImportRejectsCorruptionAndDuplicatesTransactionally(t *testing.T) {
	root, home := t.TempDir(), t.TempDir()
	source := filepath.Join(t.TempDir(), "plugin")
	writeMarketplacePluginRelease(t, source, "acme", "1.0.0")
	bundle, err := pluginpkg.LoadBundle(source)
	if err != nil {
		t.Fatal(err)
	}
	corruptPath := filepath.Join(source, filepath.FromSlash(bundle.Manifest.Artifacts[0].Path))
	if err := os.WriteFile(corruptPath, []byte("corrupt"), 0o644); err != nil {
		t.Fatal(err)
	}
	command := marketplaceTestCommand(root, home, nil)
	command.SetArgs([]string{"marketplace", "plugin", "add", source})
	if err := command.ExecuteContext(t.Context()); err == nil || !strings.Contains(err.Error(), "sha256 mismatch") {
		t.Fatalf("corrupt import error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".wuko", "plugin-sources", "acme")); !os.IsNotExist(err) {
		t.Fatalf("failed import created a target: %v", err)
	}

	writeMarketplacePluginRelease(t, source, "acme", "1.0.0")
	command = marketplaceTestCommand(root, home, nil)
	command.SetArgs([]string{"marketplace", "plugin", "add", "--description", "Acme tools", source})
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(root, ".wuko", "plugin-sources", "acme", "plugin.json"))
	if err != nil {
		t.Fatal(err)
	}
	command = marketplaceTestCommand(root, home, nil)
	command.SetArgs([]string{"marketplace", "plugin", "add", source})
	if err := command.ExecuteContext(t.Context()); err == nil || !strings.Contains(err.Error(), "already imported") {
		t.Fatalf("duplicate import error = %v", err)
	}
	after, err := os.ReadFile(filepath.Join(root, ".wuko", "plugin-sources", "acme", "plugin.json"))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("duplicate import changed the target: %v", err)
	}

	writeMarketplacePluginRelease(t, source, "acme", "2.0.0")
	command = marketplaceTestCommand(root, home, nil)
	command.SetArgs([]string{"marketplace", "plugin", "update", "acme", source})
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	metadata, err := readMarketplacePluginSource(filepath.Join(root, ".wuko", "plugin-sources", "acme"))
	if err != nil {
		t.Fatal(err)
	}
	if metadata.Description != "Acme tools" {
		t.Fatalf("update description = %q", metadata.Description)
	}
}

func TestMarketplaceBuildRemovesOnlyUnmodifiedStalePluginArtifacts(t *testing.T) {
	root, home := t.TempDir(), t.TempDir()
	source := filepath.Join(t.TempDir(), "plugin")
	writeMarketplacePluginRelease(t, source, "acme", "1.0.0")
	command := marketplaceTestCommand(root, home, nil)
	command.SetArgs([]string{"marketplace", "plugin", "add", source})
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	command = marketplaceTestCommand(root, home, nil)
	command.SetArgs([]string{"marketplace", "build"})
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	imported := filepath.Join(root, ".wuko", "plugin-sources", "acme")
	bundle, err := pluginpkg.LoadBundle(imported)
	if err != nil {
		t.Fatal(err)
	}
	oldRelative := bundle.Manifest.Artifacts[0].Path
	oldPublished := filepath.Join(root, "plugins", "acme", filepath.FromSlash(oldRelative))
	newRelative := "dist/replacement.tar.gz"
	if err := os.Rename(filepath.Join(imported, filepath.FromSlash(oldRelative)), filepath.Join(imported, filepath.FromSlash(newRelative))); err != nil {
		t.Fatal(err)
	}
	bundle.Manifest.Artifacts[0].Path = newRelative
	data, err := json.MarshalIndent(bundle.Manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(imported, "plugin.json"), append(data, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	command = marketplaceTestCommand(root, home, nil)
	command.SetArgs([]string{"marketplace", "build"})
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(oldPublished); !os.IsNotExist(err) {
		t.Fatalf("stale artifact remains: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "plugins", "acme", filepath.FromSlash(newRelative))); err != nil {
		t.Fatalf("replacement artifact is missing: %v", err)
	}

	if err := os.WriteFile(filepath.Join(root, "plugins", "acme", filepath.FromSlash(newRelative)), []byte("maintainer edit"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(imported); err != nil {
		t.Fatal(err)
	}
	command = marketplaceTestCommand(root, home, nil)
	command.SetArgs([]string{"marketplace", "build"})
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(root, "plugins", "acme", filepath.FromSlash(newRelative))); err != nil || string(data) != "maintainer edit" {
		t.Fatalf("modified stale artifact was removed: %q, %v", data, err)
	}
}

func TestMarketplaceInstallExplicitPackagePreservesSidecarsAndRunsHook(t *testing.T) {
	root, home := t.TempDir(), t.TempDir()
	sourceDir := filepath.Join(t.TempDir(), "release")
	writeMarketplacePackage(t, sourceDir, "release", "Release", `{"target":"staging"}`, `
install:
  - id: setup
    type: shell
    with:
      command: sh
      args: [-c, "cat defaults.json > install-ran.json"]
`)
	archivePath := filepath.Join(t.TempDir(), "release.tar.gz")
	_, archiveDigest, err := workflow.BuildWorkflowPackage(sourceDir, archivePath)
	if err != nil {
		t.Fatal(err)
	}
	archiveData, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	item := workflow.MarketplacePackage{
		Name: "release", PackageVersion: "1.0.0", Source: ".wuko/workflows/release", Path: "packages/release.tar.gz", Format: "tar.gz", Entry: "wuko.yaml",
		SourceSHA256: strings.Repeat("a", 64), SHA256: archiveDigest,
	}
	manifestData, err := json.Marshal(workflow.MarketplaceManifest{Version: workflow.MarketplaceManifestVersion, Packages: []workflow.MarketplacePackage{item}})
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: commandRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Path {
		case "/repo/manifest.json":
			return commandTestResponse(http.StatusOK, string(manifestData)), nil
		case "/repo/packages/release.tar.gz":
			return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Body: io.NopCloser(bytes.NewReader(archiveData)), Header: make(http.Header)}, nil
		default:
			return commandTestResponse(http.StatusNotFound, ""), nil
		}
	})}
	registry := lifecycleTestRegistry(t)
	var pickerCalls int
	var output bytes.Buffer
	deps := dependencies{
		stdin: bytes.NewReader(nil), stdout: &output, stderr: &bytes.Buffer{},
		cwd: func() (string, error) { return root, nil }, homeDir: func() (string, error) { return home, nil },
		configDir: func() (string, error) { return filepath.Join(home, "config"), nil }, registry: registry,
		loader: workflow.NewLoader(client), isInteractive: func(io.Reader) bool { return false },
		selectMany: func(context.Context, io.Reader, io.Writer, string, []tui.Option) ([]int, error) {
			pickerCalls++
			return nil, nil
		},
	}
	command := newRootCmd(deps)
	command.SetArgs([]string{"install", "--package", "release", "https://example.test/repo"})
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if pickerCalls != 0 {
		t.Fatal("explicit package install opened the picker")
	}
	installed := filepath.Join(root, ".wuko", "workflows", "repo", "release")
	for _, filename := range []string{"wuko.yaml", "defaults.json", "install-ran.json", workflow.WorkflowPackageMarkerName} {
		if _, err := os.Stat(filepath.Join(installed, filename)); err != nil {
			t.Fatalf("installed package file %s: %v", filename, err)
		}
	}
	data, err := os.ReadFile(filepath.Join(installed, "install-ran.json"))
	if err != nil || string(data) != `{"target":"staging"}` {
		t.Fatalf("install hook output = %q, err = %v", data, err)
	}
	markerData, err := os.ReadFile(filepath.Join(installed, workflow.WorkflowPackageMarkerName))
	if err != nil {
		t.Fatal(err)
	}
	var marker marketplacePackageInstallMarker
	if err := json.Unmarshal(markerData, &marker); err != nil {
		t.Fatal(err)
	}
	if marker.PackageVersion != "1.0.0" {
		t.Fatalf("installed package version = %q", marker.PackageVersion)
	}
	if marker.Marketplace != "https://example.test/repo/" {
		t.Fatalf("installed marketplace = %q", marker.Marketplace)
	}
	if err := os.WriteFile(filepath.Join(installed, "obsolete.txt"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := reinstallMarketplaceWorkflow(command, deps, workflow.Source{
		Name: "release", PackageDir: installed, MarketplaceURL: "https://example.test/repo/",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(installed, "obsolete.txt")); !os.IsNotExist(err) {
		t.Fatalf("reinstall preserved obsolete file: %v", err)
	}
	if !strings.Contains(output.String(), "reinstalled release") {
		t.Fatalf("output = %q", output.String())
	}
}

func TestMarketplaceInstallRejectsDigestMismatchBeforeWriting(t *testing.T) {
	root, home := t.TempDir(), t.TempDir()
	item := workflow.MarketplacePackage{
		Name: "release", PackageVersion: "1.0.0", Source: ".wuko/workflows/release", Path: "packages/release.tar.gz", Format: "tar.gz", Entry: "wuko.yaml",
		SourceSHA256: strings.Repeat("a", 64), SHA256: strings.Repeat("b", 64),
	}
	manifestData, err := json.Marshal(workflow.MarketplaceManifest{Version: workflow.MarketplaceManifestVersion, Packages: []workflow.MarketplacePackage{item}})
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: commandRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Path {
		case "/repo/manifest.json":
			return commandTestResponse(http.StatusOK, string(manifestData)), nil
		case "/repo/packages/release.tar.gz":
			return commandTestResponse(http.StatusOK, "not the recorded archive"), nil
		default:
			return commandTestResponse(http.StatusNotFound, ""), nil
		}
	})}
	command := newRootCmd(dependencies{
		stdin: bytes.NewReader(nil), stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{},
		cwd: func() (string, error) { return root, nil }, homeDir: func() (string, error) { return home, nil },
		configDir: func() (string, error) { return filepath.Join(home, "config"), nil }, registry: step.NewRegistry(),
		loader: workflow.NewLoader(client), isInteractive: func(io.Reader) bool { return false },
	})
	command.SetArgs([]string{"install", "--package", "release", "https://example.test/repo"})
	err = command.ExecuteContext(t.Context())
	if err == nil || !strings.Contains(err.Error(), "checksum does not match") {
		t.Fatalf("error = %v, want digest verification failure", err)
	}
	if _, statErr := os.Stat(filepath.Join(root, ".wuko", "workflows")); !os.IsNotExist(statErr) {
		t.Fatalf("marketplace storage was written after digest failure: %v", statErr)
	}
}

func TestMarketplaceInstallRequiresPackagesInNonInteractiveMode(t *testing.T) {
	root, home := t.TempDir(), t.TempDir()
	item := workflow.MarketplacePackage{
		Name: "release", PackageVersion: "1.0.0", Source: ".wuko/workflows/release", Path: "packages/release.tar.gz", Format: "tar.gz", Entry: "wuko.yaml",
		SourceSHA256: strings.Repeat("a", 64), SHA256: strings.Repeat("b", 64),
	}
	manifestData, err := json.Marshal(workflow.MarketplaceManifest{Version: workflow.MarketplaceManifestVersion, Packages: []workflow.MarketplacePackage{item}})
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: commandRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path == "/repo/manifest.json" {
			return commandTestResponse(http.StatusOK, string(manifestData)), nil
		}
		return commandTestResponse(http.StatusNotFound, ""), nil
	})}
	command := newRootCmd(dependencies{
		stdin: bytes.NewReader(nil), stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{},
		cwd: func() (string, error) { return root, nil }, homeDir: func() (string, error) { return home, nil },
		configDir: func() (string, error) { return filepath.Join(home, "config"), nil }, registry: step.NewRegistry(),
		loader: workflow.NewLoader(client), isInteractive: func(io.Reader) bool { return false },
	})
	command.SetArgs([]string{"install", "https://example.test/repo"})
	err = command.ExecuteContext(t.Context())
	if err == nil || !strings.Contains(err.Error(), "--package") {
		t.Fatalf("error = %v, want --package usage hint", err)
	}
	if _, statErr := os.Stat(filepath.Join(root, ".wuko", "workflows")); !os.IsNotExist(statErr) {
		t.Fatalf("marketplace storage was written without package selection: %v", statErr)
	}
}

func TestMarketplaceBuildRejectsMissingManifestAndSymlink(t *testing.T) {
	t.Run("missing root manifest", func(t *testing.T) {
		root, home := t.TempDir(), t.TempDir()
		directory := filepath.Join(root, ".wuko", "workflows", "broken")
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(directory, "defaults.json"), []byte(`{}`), 0o644); err != nil {
			t.Fatal(err)
		}
		command := marketplaceTestCommand(root, home, nil)
		command.SetArgs([]string{"marketplace", "build"})
		if err := command.ExecuteContext(t.Context()); err == nil || !strings.Contains(err.Error(), "no root wuko.yaml") {
			t.Fatalf("error = %v, want missing-manifest error", err)
		}
	})

	t.Run("symlink", func(t *testing.T) {
		root, home := t.TempDir(), t.TempDir()
		directory := filepath.Join(root, ".wuko", "workflows", "release")
		writeMarketplacePackage(t, directory, "release", "Release", `{}`, "")
		if err := os.Symlink(filepath.Join(directory, "defaults.json"), filepath.Join(directory, "link.json")); err != nil {
			t.Fatal(err)
		}
		command := marketplaceTestCommand(root, home, nil)
		command.SetArgs([]string{"marketplace", "build"})
		if err := command.ExecuteContext(t.Context()); err == nil || !strings.Contains(err.Error(), "contains symlink") {
			t.Fatalf("error = %v, want symlink error", err)
		}
	})
}

func TestMarketplaceBuildPublicationFailureRestoresPublishedFiles(t *testing.T) {
	root, home := t.TempDir(), t.TempDir()
	packageDir := filepath.Join(root, ".wuko", "workflows", "release")
	writeMarketplacePackage(t, packageDir, "release", "Release", `{"target":"staging"}`, "")
	command := marketplaceTestCommand(root, home, nil)
	command.SetArgs([]string{"marketplace", "build"})
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(root, "manifest.json")
	archivePath := filepath.Join(root, "packages", "release.tar.gz")
	originalManifest, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	originalArchive, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(packageDir, "defaults.json"), []byte(`{"target":"production"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	build, err := buildMarketplaceManifest(root, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer build.cleanup()
	stagedArchive := build.replacements["packages/release.tar.gz"]
	if stagedArchive == "" {
		t.Fatal("changed archive was not staged")
	}
	if err := os.Remove(stagedArchive); err != nil {
		t.Fatal(err)
	}
	if err := publishMarketplaceBuild(root, manifestPath, build); err == nil {
		t.Fatal("publication succeeded with a missing staged archive")
	}

	manifestAfter, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	archiveAfter, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(manifestAfter, originalManifest) {
		t.Fatal("failed publication changed the published manifest")
	}
	if !bytes.Equal(archiveAfter, originalArchive) {
		t.Fatal("failed publication changed the published archive")
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

func writeMarketplacePackage(t *testing.T, directory, name, description, defaults, extra string) {
	t.Helper()
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	data := fmt.Sprintf("version: 1\npackage_version: 1.0.0\nname: %s\ndescription: %s\n%ssteps:\n  - id: run\n    type: shell\n    with: {command: true}\n", name, description, extra)
	if err := os.WriteFile(filepath.Join(directory, "wuko.yaml"), []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "defaults.json"), []byte(defaults), 0o644); err != nil {
		t.Fatal(err)
	}
}

func commandTestResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Status: fmt.Sprintf("%d %s", status, http.StatusText(status)), Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}

func writeMarketplacePluginRelease(t *testing.T, directory, namespace, version string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(directory, "dist"), 0o755); err != nil {
		t.Fatal(err)
	}
	platforms := []workflow.MarketplacePlatform{{OS: runtime.GOOS, Arch: runtime.GOARCH}, {OS: "linux", Arch: "arm64"}}
	if runtime.GOOS == "linux" && runtime.GOARCH == "arm64" {
		platforms[1] = workflow.MarketplacePlatform{OS: "darwin", Arch: "amd64"}
	}
	manifest := pluginpkg.Manifest{Version: 1, Namespace: namespace, PluginVersion: version, Protocol: pluginpkg.Protocol}
	for _, platform := range platforms {
		name := platform.OS + "-" + platform.Arch + ".tar.gz"
		archive := pluginTestArchive(t, "wuko-plugin-"+namespace, []byte("binary"))
		if err := os.WriteFile(filepath.Join(directory, "dist", name), archive, 0o644); err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(archive)
		manifest.Artifacts = append(manifest.Artifacts, pluginpkg.Artifact{OS: platform.OS, Arch: platform.Arch, Path: "dist/" + name, Format: "tar.gz", Entry: "wuko-plugin-" + namespace, SHA256: fmt.Sprintf("%x", digest[:])})
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "plugin.json"), append(data, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}

func pluginTestArchive(t *testing.T, name string, data []byte) []byte {
	t.Helper()
	var result bytes.Buffer
	gzipWriter := gzip.NewWriter(&result)
	tarWriter := tar.NewWriter(gzipWriter)
	if err := tarWriter.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(data)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tarWriter.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	return result.Bytes()
}
