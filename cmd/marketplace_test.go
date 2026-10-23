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
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/up2jj/wuko/internal/archivetest"
	"github.com/up2jj/wuko/internal/testfs"
	"github.com/up2jj/wuko/internal/transporttest"
	"github.com/up2jj/wuko/marketplace"
	pluginpkg "github.com/up2jj/wuko/plugin"
	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/steps/shell"
	"github.com/up2jj/wuko/tui"
	"github.com/up2jj/wuko/workflow"
)

func TestMarketplaceInitAndIncrementalBuild(t *testing.T) {
	t.Parallel()
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

	testfs.WriteFile(t, filepath.Join(workflowRoot, "release", "defaults.json"), []byte(`{"target":"production"}`), 0o644)
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
	t.Parallel()
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
	if _, err := os.Stat(filepath.Join(root, "plugins", "acme", marketplace.PluginSourceName)); !os.IsNotExist(err) {
		t.Fatalf("source metadata was published: %v", err)
	}

	command = marketplaceTestCommand(root, home, nil)
	command.SetArgs([]string{"marketplace", "build", "--check"})
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	metadata, err := marketplace.ReadPluginSource(imported)
	if err != nil {
		t.Fatal(err)
	}
	metadata.Description = "Changed"
	if err := marketplace.WriteJSON(filepath.Join(imported, marketplace.PluginSourceName), metadata); err != nil {
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

func TestMarketplaceWorkflowAddRemoveAndBuild(t *testing.T) {
	t.Parallel()
	root, home := t.TempDir(), t.TempDir()
	source := filepath.Join(t.TempDir(), "source")
	writeMarketplacePackage(t, source, "release", "Release safely", `{"target":"staging"}`, "")

	command := marketplaceTestCommand(root, home, nil)
	command.SetArgs([]string{"marketplace", "init"})
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	command = marketplaceTestCommand(root, home, nil)
	command.SetArgs([]string{"marketplace", "workflow", "add", source})
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	maintained := filepath.Join(root, ".wuko", "workflows", "release")
	if _, err := os.Stat(filepath.Join(maintained, "defaults.json")); err != nil {
		t.Fatalf("copied workflow package: %v", err)
	}
	command = marketplaceTestCommand(root, home, nil)
	command.SetArgs([]string{"marketplace", "build"})
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "packages", "release.tar.gz")); err != nil {
		t.Fatalf("published workflow: %v", err)
	}

	command = marketplaceTestCommand(root, home, nil)
	command.SetArgs([]string{"marketplace", "workflow", "remove", "release", "--yes"})
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(maintained); !os.IsNotExist(err) {
		t.Fatalf("workflow source still exists: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "packages", "release.tar.gz")); err != nil {
		t.Fatalf("remove unexpectedly deleted generated package before build: %v", err)
	}
	command = marketplaceTestCommand(root, home, nil)
	command.SetArgs([]string{"marketplace", "build"})
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "packages", "release.tar.gz")); !os.IsNotExist(err) {
		t.Fatalf("stale generated package still exists: %v", err)
	}
}

func TestMarketplaceWorkflowRemoveAllowsInvalidManagedSource(t *testing.T) {
	t.Parallel()
	root, home := t.TempDir(), t.TempDir()
	target := filepath.Join(root, ".wuko", "workflows", "broken")
	testfs.MkdirAll(t, target, 0o755)
	testfs.WriteFile(t, filepath.Join(target, "wuko.yaml"), []byte("not: [valid"), 0o644)
	command := marketplaceTestCommand(root, home, nil)
	command.SetArgs([]string{"marketplace", "workflow", "remove", "broken", "--yes"})
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("invalid managed source still exists: %v", err)
	}
}

func TestMarketplaceWorkflowRemoveMatchesDeclaredNameNotDirectory(t *testing.T) {
	t.Parallel()
	root, home := t.TempDir(), t.TempDir()
	workflows := filepath.Join(root, ".wuko", "workflows")
	misleading := filepath.Join(workflows, "release")
	actual := filepath.Join(workflows, "maintained")
	writeMarketplacePackage(t, misleading, "deploy", "Deploy", `{}`, "")
	writeMarketplacePackage(t, actual, "release", "Release", `{}`, "")

	command := marketplaceTestCommand(root, home, nil)
	command.SetArgs([]string{"marketplace", "workflow", "remove", "release", "--yes"})
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(misleading); err != nil {
		t.Fatalf("package declaring another name was removed: %v", err)
	}
	if _, err := os.Stat(actual); !os.IsNotExist(err) {
		t.Fatalf("package declaring the requested name still exists: %v", err)
	}
}

func TestMarketplacePluginReleaseUpsertsBuildsAndRemoves(t *testing.T) {
	t.Parallel()
	root, home := t.TempDir(), t.TempDir()
	v1 := filepath.Join(t.TempDir(), "v1")
	v2 := filepath.Join(t.TempDir(), "v2")
	writeMarketplacePluginRelease(t, v1, "acme", "1.0.0")
	writeMarketplacePluginRelease(t, v2, "acme", "2.0.0")

	command := marketplaceTestCommand(root, home, nil)
	command.SetArgs([]string{"marketplace", "init"})
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	command = marketplaceTestCommand(root, home, nil)
	command.SetArgs([]string{"marketplace", "plugin", "release", "--version", "1.0.0", "--description", "Acme tools", v1})
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := pluginpkg.LoadBundle(filepath.Join(root, "plugins", "acme")); err != nil {
		t.Fatalf("published release: %v", err)
	}

	command = marketplaceTestCommand(root, home, nil)
	command.SetArgs([]string{"marketplace", "plugin", "release", "--version", "2.0.0", v2})
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	metadata, err := marketplace.ReadPluginSource(filepath.Join(root, ".wuko", "plugin-sources", "acme"))
	if err != nil {
		t.Fatal(err)
	}
	if metadata.Description != "Acme tools" {
		t.Fatalf("description = %q", metadata.Description)
	}
	published, err := pluginpkg.LoadBundle(filepath.Join(root, "plugins", "acme"))
	if err != nil {
		t.Fatal(err)
	}
	if published.Manifest.PluginVersion != "2.0.0" {
		t.Fatalf("published version = %q", published.Manifest.PluginVersion)
	}

	command = marketplaceTestCommand(root, home, nil)
	command.SetArgs([]string{"marketplace", "plugin", "release", "--version", "1.0.0", v1})
	if err := command.ExecuteContext(t.Context()); err == nil || !strings.Contains(err.Error(), "older than") {
		t.Fatalf("downgrade error = %v", err)
	}
	command = marketplaceTestCommand(root, home, nil)
	command.SetArgs([]string{"marketplace", "plugin", "release", "--version", "2.0.0", v2})
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatalf("idempotent release: %v", err)
	}
	changed := filepath.Join(t.TempDir(), "changed")
	writeMarketplacePluginRelease(t, changed, "acme", "2.0.0")
	rewriteMarketplacePluginArtifact(t, changed, []byte("different binary"))
	command = marketplaceTestCommand(root, home, nil)
	command.SetArgs([]string{"marketplace", "plugin", "release", "--version", "2.0.0", changed})
	if err := command.ExecuteContext(t.Context()); err == nil || !strings.Contains(err.Error(), "different contents") {
		t.Fatalf("mutated same-version error = %v", err)
	}

	command = marketplaceTestCommand(root, home, nil)
	command.SetArgs([]string{"marketplace", "plugin", "remove", "acme", "--yes"})
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, ".wuko", "plugin-sources", "acme")); !os.IsNotExist(err) {
		t.Fatalf("plugin source still exists: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "plugins", "acme", "plugin.json")); err != nil {
		t.Fatalf("remove unexpectedly deleted generated plugin before build: %v", err)
	}
	command = marketplaceTestCommand(root, home, nil)
	command.SetArgs([]string{"marketplace", "build"})
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "plugins", "acme", "plugin.json")); !os.IsNotExist(err) {
		t.Fatalf("stale generated plugin still exists: %v", err)
	}
}

func TestMarketplacePluginReleaseRollsBackWhenBuildFails(t *testing.T) {
	t.Parallel()
	root, home := t.TempDir(), t.TempDir()
	v1 := filepath.Join(t.TempDir(), "v1")
	v2 := filepath.Join(t.TempDir(), "v2")
	writeMarketplacePluginRelease(t, v1, "acme", "1.0.0")
	writeMarketplacePluginRelease(t, v2, "acme", "2.0.0")
	command := marketplaceTestCommand(root, home, nil)
	command.SetArgs([]string{"marketplace", "plugin", "add", "--description", "Acme tools", v1})
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	badWorkflow := filepath.Join(root, ".wuko", "workflows", "broken")
	testfs.MkdirAll(t, badWorkflow, 0o755)
	testfs.WriteFile(t, filepath.Join(badWorkflow, "readme.txt"), []byte("missing manifest"), 0o644)

	command = marketplaceTestCommand(root, home, nil)
	command.SetArgs([]string{"marketplace", "plugin", "release", "--version", "2.0.0", v2})
	if err := command.ExecuteContext(t.Context()); err == nil || !strings.Contains(err.Error(), "building marketplace") {
		t.Fatalf("release error = %v", err)
	}
	bundle, err := pluginpkg.LoadBundle(filepath.Join(root, ".wuko", "plugin-sources", "acme"))
	if err != nil {
		t.Fatal(err)
	}
	if bundle.Manifest.PluginVersion != "1.0.0" {
		t.Fatalf("rollback version = %q", bundle.Manifest.PluginVersion)
	}
}

func TestMarketplacePluginReleaseRequiresDescriptionForFirstRelease(t *testing.T) {
	t.Parallel()
	root, home := t.TempDir(), t.TempDir()
	source := filepath.Join(t.TempDir(), "plugin")
	writeMarketplacePluginRelease(t, source, "acme", "1.0.0")
	command := marketplaceTestCommand(root, home, nil)
	command.SetArgs([]string{"marketplace", "plugin", "release", "--version", "1.0.0", source})
	err := command.ExecuteContext(t.Context())
	if err == nil || !strings.Contains(err.Error(), "--description") {
		t.Fatalf("error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".wuko", "plugin-sources", "acme")); !os.IsNotExist(err) {
		t.Fatalf("failed release changed sources: %v", err)
	}
}

func TestMarketplacePluginReleaseRequiresWorkflowUnlessBuildIsSkipped(t *testing.T) {
	t.Parallel()
	root, home := t.TempDir(), t.TempDir()
	source := filepath.Join(t.TempDir(), "plugin")
	writeMarketplacePluginRelease(t, source, "acme", "1.0.0")
	if err := os.Remove(filepath.Join(source, pluginReleaseWorkflowName)); err != nil {
		t.Fatal(err)
	}
	command := marketplaceTestCommand(root, home, nil)
	command.SetArgs([]string{"marketplace", "plugin", "release", "--version", "1.0.0", "--description", "Acme", source})
	if err := command.ExecuteContext(t.Context()); err == nil || !strings.Contains(err.Error(), pluginReleaseWorkflowName) {
		t.Fatalf("missing workflow error = %v", err)
	}
	command = marketplaceTestCommand(root, home, nil)
	command.SetArgs([]string{"marketplace", "plugin", "release", "--skip-build", "--description", "Acme", source})
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestMarketplacePluginReleaseValidatesWorkflowBundleAndVersion(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	root, home := filepath.Join(base, "marketplace"), t.TempDir()
	source := filepath.Join(base, "plugin")
	outside := filepath.Join(base, "outside")
	writeMarketplacePluginRelease(t, source, "acme", "1.0.0")
	testfs.MkdirAll(t, outside, 0o755)
	missingOutput := `version: 1
name: plugin-release
steps:
  - return: {outputs: {}}
`
	testfs.WriteFile(t, filepath.Join(source, pluginReleaseWorkflowName), []byte(missingOutput), 0o644)
	command := marketplaceTestCommand(root, home, nil)
	command.SetArgs([]string{"marketplace", "plugin", "release", "--version", "1.0.0", "--description", "Acme", source})
	if err := command.ExecuteContext(t.Context()); err == nil || !strings.Contains(err.Error(), "output named bundle") {
		t.Fatalf("missing bundle output error = %v", err)
	}

	escaping := `version: 1
name: plugin-release
outputs:
  bundle: {type: string, value: '"../outside"'}
steps:
  - return: {outputs: {bundle: '"../outside"'}}
`
	testfs.WriteFile(t, filepath.Join(source, pluginReleaseWorkflowName), []byte(escaping), 0o644)
	command = marketplaceTestCommand(root, home, nil)
	command.SetArgs([]string{"marketplace", "plugin", "release", "--version", "1.0.0", "--description", "Acme", source})
	if err := command.ExecuteContext(t.Context()); err == nil || !strings.Contains(err.Error(), "escapes the plugin project") {
		t.Fatalf("escaping bundle error = %v", err)
	}

	valid := `version: 1
name: plugin-release
outputs:
  bundle: {type: string, value: '"."'}
steps:
  - return: {outputs: {bundle: '"."'}}
`
	testfs.WriteFile(t, filepath.Join(source, pluginReleaseWorkflowName), []byte(valid), 0o644)
	command = marketplaceTestCommand(root, home, nil)
	command.SetArgs([]string{"marketplace", "plugin", "release", "--version", "2.0.0", "--description", "Acme", source})
	if err := command.ExecuteContext(t.Context()); err == nil || !strings.Contains(err.Error(), "requested version 2.0.0") {
		t.Fatalf("version mismatch error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".wuko", "plugin-sources", "acme")); !os.IsNotExist(err) {
		t.Fatalf("failed workflow release changed marketplace: %v", err)
	}
}

func TestMarketplacePluginReleaseReportsFailedBuildStepStderr(t *testing.T) {
	t.Parallel()
	root, home := t.TempDir(), t.TempDir()
	source := filepath.Join(t.TempDir(), "plugin")
	writeMarketplacePluginRelease(t, source, "acme", "1.0.0")
	failing := `version: 1
name: plugin-release
outputs:
  bundle: {type: string, value: '"."'}
steps:
  - id: deps
    type: shell
    with: {script: 'echo "fetching deps" >&2'}
  - id: build
    type: shell
    with: {script: 'printf "compiling\\n\\033[31merror: undefined function\\033[0m\\n" >&2; exit 3'}
`
	testfs.WriteFile(t, filepath.Join(source, pluginReleaseWorkflowName), []byte(failing), 0o644)
	registry := step.NewRegistry()
	if err := shell.Register(registry); err != nil {
		t.Fatal(err)
	}
	command := newRootCmd(dependencies{
		stdin: bytes.NewReader(nil), stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{},
		cwd: func() (string, error) { return root, nil }, homeDir: func() (string, error) { return home, nil },
		configDir: func() (string, error) { return filepath.Join(home, "config"), nil }, registry: registry,
	})
	command.SetArgs([]string{"marketplace", "plugin", "release", "--version", "1.0.0", "--description", "Acme", source})
	err := command.ExecuteContext(t.Context())
	if err == nil || !strings.HasSuffix(err.Error(), "exited with status 3\nlast stderr lines:\n  compiling\n  error: undefined function") {
		t.Fatalf("failed build error = %v", err)
	}
	if strings.Contains(err.Error(), "fetching deps") {
		t.Fatalf("failed build error includes an earlier step's stderr: %v", err)
	}
}

func TestMarketplacePluginUpdateRequiresMatchingNamespaceAndPreservesImport(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
	root, home := t.TempDir(), t.TempDir()
	source := filepath.Join(t.TempDir(), "plugin")
	writeMarketplacePluginRelease(t, source, "acme", "1.0.0")
	bundle, err := pluginpkg.LoadBundle(source)
	if err != nil {
		t.Fatal(err)
	}
	corruptPath := filepath.Join(source, filepath.FromSlash(bundle.Manifest.Artifacts[0].Path))
	testfs.WriteFile(t, corruptPath, []byte("corrupt"), 0o644)
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
	metadata, err := marketplace.ReadPluginSource(filepath.Join(root, ".wuko", "plugin-sources", "acme"))
	if err != nil {
		t.Fatal(err)
	}
	if metadata.Description != "Acme tools" {
		t.Fatalf("update description = %q", metadata.Description)
	}
}

func TestMarketplaceBuildRemovesOnlyUnmodifiedStalePluginArtifacts(t *testing.T) {
	t.Parallel()
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
	testfs.WriteFile(t, filepath.Join(imported, "plugin.json"), append(data, '\n'), 0o644)
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

	testfs.WriteFile(t, filepath.Join(root, "plugins", "acme", filepath.FromSlash(newRelative)), []byte("maintainer edit"), 0o644)
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
	t.Parallel()
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
	client := &http.Client{Transport: transporttest.RoundTripFunc(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Path {
		case "/repo/manifest.json":
			return transporttest.Response(http.StatusOK, string(manifestData)), nil
		case "/repo/packages/release.tar.gz":
			return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Body: io.NopCloser(bytes.NewReader(archiveData)), Header: make(http.Header)}, nil
		default:
			return transporttest.Response(http.StatusNotFound, ""), nil
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
	testfs.WriteFile(t, filepath.Join(installed, "obsolete.txt"), []byte("old"), 0o644)
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
	t.Parallel()
	root, home := t.TempDir(), t.TempDir()
	item := workflow.MarketplacePackage{
		Name: "release", PackageVersion: "1.0.0", Source: ".wuko/workflows/release", Path: "packages/release.tar.gz", Format: "tar.gz", Entry: "wuko.yaml",
		SourceSHA256: strings.Repeat("a", 64), SHA256: strings.Repeat("b", 64),
	}
	manifestData, err := json.Marshal(workflow.MarketplaceManifest{Version: workflow.MarketplaceManifestVersion, Packages: []workflow.MarketplacePackage{item}})
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: transporttest.RoundTripFunc(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Path {
		case "/repo/manifest.json":
			return transporttest.Response(http.StatusOK, string(manifestData)), nil
		case "/repo/packages/release.tar.gz":
			return transporttest.Response(http.StatusOK, "not the recorded archive"), nil
		default:
			return transporttest.Response(http.StatusNotFound, ""), nil
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
	t.Parallel()
	root, home := t.TempDir(), t.TempDir()
	item := workflow.MarketplacePackage{
		Name: "release", PackageVersion: "1.0.0", Source: ".wuko/workflows/release", Path: "packages/release.tar.gz", Format: "tar.gz", Entry: "wuko.yaml",
		SourceSHA256: strings.Repeat("a", 64), SHA256: strings.Repeat("b", 64),
	}
	manifestData, err := json.Marshal(workflow.MarketplaceManifest{Version: workflow.MarketplaceManifestVersion, Packages: []workflow.MarketplacePackage{item}})
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: transporttest.RoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path == "/repo/manifest.json" {
			return transporttest.Response(http.StatusOK, string(manifestData)), nil
		}
		return transporttest.Response(http.StatusNotFound, ""), nil
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
	t.Parallel()
	t.Run("missing root manifest", func(t *testing.T) {
		root, home := t.TempDir(), t.TempDir()
		directory := filepath.Join(root, ".wuko", "workflows", "broken")
		testfs.MkdirAll(t, directory, 0o755)
		testfs.WriteFile(t, filepath.Join(directory, "defaults.json"), []byte(`{}`), 0o644)
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
	t.Parallel()
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

	testfs.WriteFile(t, filepath.Join(packageDir, "defaults.json"), []byte(`{"target":"production"}`), 0o644)
	build, err := marketplace.BuildManifest(t.Context(), root, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer build.Cleanup()
	stagedArchive := build.Replacements["packages/release.tar.gz"]
	if stagedArchive == "" {
		t.Fatal("changed archive was not staged")
	}
	if err := os.Remove(stagedArchive); err != nil {
		t.Fatal(err)
	}
	if err := marketplace.Publish(root, manifestPath, build); err == nil {
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
	testfs.MkdirAll(t, directory, 0o755)
	data := fmt.Sprintf("version: 1\npackage_version: 1.0.0\nname: %s\ndescription: %s\n%ssteps:\n  - id: run\n    type: shell\n    with: {command: true}\n", name, description, extra)
	testfs.WriteFile(t, filepath.Join(directory, "wuko.yaml"), []byte(data), 0o644)
	testfs.WriteFile(t, filepath.Join(directory, "defaults.json"), []byte(defaults), 0o644)
}

func writeMarketplacePluginRelease(t *testing.T, directory, namespace, version string) {
	t.Helper()
	testfs.MkdirAll(t, filepath.Join(directory, "dist"), 0o755)
	platforms := []workflow.MarketplacePlatform{{OS: runtime.GOOS, Arch: runtime.GOARCH}, {OS: "linux", Arch: "arm64"}}
	if runtime.GOOS == "linux" && runtime.GOARCH == "arm64" {
		platforms[1] = workflow.MarketplacePlatform{OS: "darwin", Arch: "amd64"}
	}
	manifest := pluginpkg.Manifest{Version: 1, Namespace: namespace, PluginVersion: version, Protocol: pluginpkg.Protocol}
	for _, platform := range platforms {
		name := platform.OS + "-" + platform.Arch + ".tar.gz"
		archive := archivetest.TarGz(t, archivetest.Entry{Name: "wuko-plugin-" + namespace, Mode: 0o755, Data: []byte("binary")})
		testfs.WriteFile(t, filepath.Join(directory, "dist", name), archive, 0o644)
		digest := sha256.Sum256(archive)
		manifest.Artifacts = append(manifest.Artifacts, pluginpkg.Artifact{OS: platform.OS, Arch: platform.Arch, Path: "dist/" + name, Format: "tar.gz", Entry: "wuko-plugin-" + namespace, SHA256: fmt.Sprintf("%x", digest[:])})
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	testfs.WriteFile(t, filepath.Join(directory, "plugin.json"), append(data, '\n'), 0o644)
	releaseWorkflow := `version: 1
name: plugin-release
vars: {version: ""}
outputs:
  bundle: {type: string, value: '"."'}
steps:
  - return: {outputs: {bundle: '"."'}}
`
	testfs.WriteFile(t, filepath.Join(directory, pluginReleaseWorkflowName), []byte(releaseWorkflow), 0o644)
}

func rewriteMarketplacePluginArtifact(t *testing.T, directory string, payload []byte) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(directory, "plugin.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest pluginpkg.Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	archive := archivetest.TarGz(t, archivetest.Entry{Name: manifest.Artifacts[0].Entry, Mode: 0o755, Data: payload})
	artifactPath := filepath.Join(directory, filepath.FromSlash(manifest.Artifacts[0].Path))
	testfs.WriteFile(t, artifactPath, archive, 0o644)
	digest := sha256.Sum256(archive)
	manifest.Artifacts[0].SHA256 = fmt.Sprintf("%x", digest[:])
	updated, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	testfs.WriteFile(t, filepath.Join(directory, "plugin.json"), append(updated, '\n'), 0o644)
}
