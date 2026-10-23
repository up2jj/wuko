package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/up2jj/wuko/internal/archivetest"
	"github.com/up2jj/wuko/internal/filelock"
	"github.com/up2jj/wuko/internal/testfs"
	"github.com/up2jj/wuko/workflow"
)

type releaseTransport struct {
	mu     sync.Mutex
	bodies map[string][]byte
	counts map[string]int
}

func (r *releaseTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.counts[req.URL.Path]++
	body, ok := r.bodies[req.URL.Path]
	if !ok {
		return nil, errors.New("source unavailable")
	}
	return &http.Response{StatusCode: 200, Status: "200 OK", Body: io.NopCloser(bytes.NewReader(body)), Header: make(http.Header), Request: req}, nil
}
func (r *releaseTransport) count(path string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.counts[path]
}
func (r *releaseTransport) replace(bodies map[string][]byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.bodies = bodies
}
func releaseBytes(t *testing.T, namespace, version string) ([]byte, []byte) {
	t.Helper()
	archive := archivetest.TarGz(t, archivetest.Entry{Name: "wuko-plugin-" + namespace, Mode: 0700, Data: []byte("#!/bin/sh\n# " + version + "\nexit 97\n")})
	manifest, err := json.Marshal(Manifest{Version: 1, Namespace: namespace, PluginVersion: version, Protocol: Protocol, Artifacts: []Artifact{{OS: runtime.GOOS, Arch: runtime.GOARCH, Path: "plugin.tar.gz", Format: "tar.gz", Entry: "wuko-plugin-" + namespace, SHA256: digest(archive)}}})
	if err != nil {
		t.Fatal(err)
	}
	return manifest, archive
}
func transportFor(manifest, archive []byte) *releaseTransport {
	return &releaseTransport{bodies: map[string][]byte{"/plugin.json": manifest, "/plugin.tar.gz": archive}, counts: make(map[string]int)}
}
func resolutionManager(t *testing.T, cache string, transport *releaseTransport) *Manager {
	t.Helper()
	return NewManager(Config{CacheDir: func() (string, error) { return cache, nil }, HTTPClient: &http.Client{Transport: transport}})
}
func acmeSource() map[string]workflow.PluginSource {
	return map[string]workflow.PluginSource{"acme": {Source: "https://plugins.test/plugin.json", With: map[string]any{"token": "private-setting"}}}
}
func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
func mustWrite(t *testing.T, path string, data []byte) {
	t.Helper()
	testfs.MkdirAll(t, filepath.Dir(path), 0700)
	testfs.WriteFile(t, path, data, 0600)
}

func TestLocalPinsReuseOfflineAndUpdateExplicitly(t *testing.T) {
	t.Parallel()
	manifest, archive := releaseBytes(t, "acme", "1.0.0")
	transport := transportFor(manifest, archive)
	cache, dir := t.TempDir(), t.TempDir()
	manager := resolutionManager(t, cache, transport)
	origin := workflow.PluginOrigin{Dir: dir}
	sources := acmeSource()
	sources["acme"] = workflow.PluginSource{Source: "https://plugins.test/plugin.json?token=secret", With: sources["acme"].With}
	first, err := manager.ResolvePluginSources(t.Context(), origin, sources)
	if err != nil {
		t.Fatal(err)
	}
	if first["acme"].SHA256 != digest(manifest) {
		t.Fatal(first)
	}
	lock := mustRead(t, filepath.Join(dir, LockfileName))
	if bytes.Contains(lock, []byte("secret")) || bytes.Contains(lock, []byte("private-setting")) {
		t.Fatalf("lock leaked credentials: %s", lock)
	}
	transport.replace(nil)
	offline := resolutionManager(t, cache, transport)
	if _, err := offline.ResolvePluginSources(t.Context(), origin, sources); err != nil {
		t.Fatal(err)
	}
	if transport.count("/plugin.json") != 1 || transport.count("/plugin.tar.gz") != 1 {
		t.Fatal("offline reuse contacted source")
	}
	nextManifest, nextArchive := releaseBytes(t, "acme", "2.0.0")
	transport.replace(map[string][]byte{"/plugin.json": nextManifest, "/plugin.tar.gz": nextArchive})
	unchanged, err := manager.ResolvePluginSources(t.Context(), origin, sources)
	if err != nil {
		t.Fatal(err)
	}
	if unchanged["acme"].SHA256 != digest(manifest) {
		t.Fatal("ordinary run refreshed existing pin")
	}
	updated, err := manager.LockPluginSources(t.Context(), origin, sources, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if updated["acme"].SHA256 != digest(nextManifest) {
		t.Fatal("update did not refresh pin")
	}
	for _, pin := range []string{digest(manifest), digest(nextManifest)} {
		path, _ := manager.store.directory(pin)
		if _, err := readCachedRelease(path, "acme", pin, sources["acme"].Source); err != nil {
			t.Fatal(err)
		}
	}
}

func TestReadOnlyAndFrozenRequirePinsWithoutMutation(t *testing.T) {
	t.Parallel()
	for _, policy := range []workflow.PluginLockPolicy{{ReadOnly: true}, {Frozen: true}} {
		t.Run(map[bool]string{true: "frozen", false: "read-only"}[policy.Frozen], func(t *testing.T) {
			manifest, archive := releaseBytes(t, "acme", "1")
			transport := transportFor(manifest, archive)
			manager := resolutionManager(t, t.TempDir(), transport)
			dir := t.TempDir()
			origin := workflow.PluginOrigin{Dir: dir}
			ctx := workflow.ContextWithPluginLockPolicy(t.Context(), policy)
			if _, err := manager.ResolvePluginSources(ctx, origin, acmeSource()); err == nil || !strings.Contains(err.Error(), "missing plugin pin") {
				t.Fatalf("error = %v", err)
			}
			files, err := os.ReadDir(dir)
			if err != nil || len(files) != 0 {
				t.Fatalf("files=%v error=%v", files, err)
			}
			if transport.count("/plugin.json") != 0 {
				t.Fatal("missing pin contacted network")
			}
			if _, err := manager.ResolvePluginSources(t.Context(), origin, acmeSource()); err != nil {
				t.Fatal(err)
			}
			before := mustRead(t, filepath.Join(dir, LockfileName))
			if err := os.RemoveAll(filepath.Join(managerCacheRoot(t, manager), "v1")); err != nil {
				t.Fatal(err)
			}
			if _, err := manager.ResolvePluginSources(ctx, origin, acmeSource()); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, mustRead(t, filepath.Join(dir, LockfileName))) {
				t.Fatal("reader modified lock")
			}
		})
	}
}
func managerCacheRoot(t *testing.T, m *Manager) string {
	t.Helper()
	root, err := m.store.root()
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func TestRemoteRefreshesManifestAndNeverTouchesLock(t *testing.T) {
	t.Parallel()
	manifest, archive := releaseBytes(t, "acme", "1")
	transport := transportFor(manifest, archive)
	cache, dir := t.TempDir(), t.TempDir()
	// Even a malformed local lock must be irrelevant to remote resolution.
	lockPath := filepath.Join(dir, LockfileName)
	mustWrite(t, lockPath, []byte("this is not a lockfile"))
	origin := workflow.PluginOrigin{Dir: dir, Root: dir, Remote: true}
	manager := resolutionManager(t, cache, transport)
	for range 2 {
		if _, err := manager.ResolvePluginSources(t.Context(), origin, acmeSource()); err != nil {
			t.Fatal(err)
		}
	}
	if transport.count("/plugin.json") != 1 || transport.count("/plugin.tar.gz") != 1 {
		t.Fatal("resolution was not memoized per invocation")
	}
	manager = resolutionManager(t, cache, transport)
	if _, err := manager.ResolvePluginSources(t.Context(), origin, acmeSource()); err != nil {
		t.Fatal(err)
	}
	if transport.count("/plugin.json") != 2 || transport.count("/plugin.tar.gz") != 1 {
		t.Fatal("remote manifest was not fetched or cached archive was not reused")
	}
	nextManifest, nextArchive := releaseBytes(t, "acme", "2")
	transport.replace(map[string][]byte{"/plugin.json": nextManifest, "/plugin.tar.gz": nextArchive})
	manager = resolutionManager(t, cache, transport)
	resolved, err := manager.ResolvePluginSources(t.Context(), origin, acmeSource())
	if err != nil {
		t.Fatal(err)
	}
	if resolved["acme"].SHA256 != digest(nextManifest) {
		t.Fatal("remote run reused stale manifest")
	}
	if string(mustRead(t, lockPath)) != "this is not a lockfile" {
		t.Fatal("remote run modified local lock")
	}
	before := transport.count("/plugin.json")
	frozen := workflow.ContextWithPluginLockPolicy(t.Context(), workflow.PluginLockPolicy{Frozen: true})
	if _, err := manager.ResolvePluginSources(frozen, origin, acmeSource()); err == nil {
		t.Fatal("frozen remote run succeeded")
	}
	if transport.count("/plugin.json") != before {
		t.Fatal("frozen remote run contacted source")
	}
}

func TestCacheRepairsDamageWithoutChangingPin(t *testing.T) {
	t.Parallel()
	for _, file := range []string{"plugin.json", "artifact.tar.gz"} {
		t.Run(file, func(t *testing.T) {
			manifest, archive := releaseBytes(t, "acme", "1")
			transport := transportFor(manifest, archive)
			manager := resolutionManager(t, t.TempDir(), transport)
			dir := t.TempDir()
			origin := workflow.PluginOrigin{Dir: dir}
			source := acmeSource()
			if _, err := manager.ResolvePluginSources(t.Context(), origin, source); err != nil {
				t.Fatal(err)
			}
			lock := mustRead(t, filepath.Join(dir, LockfileName))
			cached, _ := manager.store.directory(digest(manifest))
			// Resolution repairs a damaged manifest; archive damage is found when the
			// plugin is brought up, which hashes the archive before extracting it.
			resolveAndBring := func() error {
				if _, err := manager.ResolvePluginSources(t.Context(), origin, source); err != nil {
					return err
				}
				_, err := manager.store.pinned(t.Context(), "acme", source["acme"].Source, digest(manifest))
				return err
			}
			mustWrite(t, filepath.Join(cached, file), []byte("damaged"))
			if err := resolveAndBring(); err != nil {
				t.Fatal(err)
			}
			if transport.count("/plugin.tar.gz") != 2 {
				t.Fatal("damaged cache was not repaired")
			}
			if !bytes.Equal(lock, mustRead(t, filepath.Join(dir, LockfileName))) {
				t.Fatal("repair changed pin")
			}
			mustWrite(t, filepath.Join(cached, file), []byte("damaged"))
			nextManifest, nextArchive := releaseBytes(t, "acme", "2")
			transport.replace(map[string][]byte{"/plugin.json": nextManifest, "/plugin.tar.gz": nextArchive})
			if err := resolveAndBring(); err == nil || !strings.Contains(err.Error(), "sha256 mismatch") {
				t.Fatalf("error = %v", err)
			}
			if !bytes.Equal(lock, mustRead(t, filepath.Join(dir, LockfileName))) {
				t.Fatal("failed repair changed pin")
			}
		})
	}
}

func TestLockedResolutionDoesNotHashArchive(t *testing.T) {
	t.Parallel()
	manifest, archive := releaseBytes(t, "acme", "1")
	transport := transportFor(manifest, archive)
	cache, dir := t.TempDir(), t.TempDir()
	manager := resolutionManager(t, cache, transport)
	origin := workflow.PluginOrigin{Dir: dir}
	if _, err := manager.ResolvePluginSources(t.Context(), origin, acmeSource()); err != nil {
		t.Fatal(err)
	}
	cached, _ := manager.store.directory(digest(manifest))
	// Same-size garbage only fails a full hash, and offline repair is impossible.
	mustWrite(t, filepath.Join(cached, "artifact.tar.gz"), bytes.Repeat([]byte{'x'}, len(archive)))
	transport.replace(nil)
	offline := resolutionManager(t, cache, transport)
	resolved, err := offline.ResolvePluginSources(t.Context(), origin, acmeSource())
	if err != nil {
		t.Fatalf("locked resolution read the archive: %v", err)
	}
	if resolved["acme"].SHA256 != digest(manifest) {
		t.Fatalf("pin = %s", resolved["acme"].SHA256)
	}
	if _, err := offline.store.pinned(t.Context(), "acme", acmeSource()["acme"].Source, digest(manifest)); err == nil {
		t.Fatal("bringing up a corrupted archive must still fail")
	}
}

func TestLockedResolutionFetchesMissingRelease(t *testing.T) {
	t.Parallel()
	manifest, archive := releaseBytes(t, "acme", "1")
	transport := transportFor(manifest, archive)
	cache, dir := t.TempDir(), t.TempDir()
	manager := resolutionManager(t, cache, transport)
	origin := workflow.PluginOrigin{Dir: dir}
	if _, err := manager.ResolvePluginSources(t.Context(), origin, acmeSource()); err != nil {
		t.Fatal(err)
	}
	cached, _ := manager.store.directory(digest(manifest))
	if err := os.Remove(filepath.Join(cached, "artifact.tar.gz")); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.ResolvePluginSources(t.Context(), origin, acmeSource()); err != nil {
		t.Fatal(err)
	}
	if transport.count("/plugin.tar.gz") != 2 {
		t.Fatal("missing archive was not fetched during resolution")
	}
	transport.replace(nil)
	if err := os.Remove(filepath.Join(cached, "artifact.tar.gz")); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.ResolvePluginSources(t.Context(), origin, acmeSource()); err == nil {
		t.Fatal("unavailable release must fail resolution")
	}
}

func TestMalformedLocksFailBeforeNetwork(t *testing.T) {
	t.Parallel()
	good := "namespace: acme\n    source: https://plugins.test/plugin.json\n    sha256: " + strings.Repeat("a", 64)
	cases := []string{"", "version: 2\nplugins: []\n", "version: 1\nextra: true\n", "version: 1\nplugins:\n  - namespace: acme\n    source: https://plugins.test/plugin.json\n    sha256: nope\n", "version: 1\nplugins:\n  - " + good + "\n  - " + good + "\n", "version: 1\nplugins: []\n---\nversion: 1\n"}
	for _, body := range cases {
		t.Run(body, func(t *testing.T) {
			dir := t.TempDir()
			mustWrite(t, filepath.Join(dir, LockfileName), []byte(body))
			transport := transportFor(nil, nil)
			manager := resolutionManager(t, t.TempDir(), transport)
			if _, err := manager.ResolvePluginSources(t.Context(), workflow.PluginOrigin{Dir: dir}, acmeSource()); err == nil {
				t.Fatal("invalid lock accepted")
			}
			if transport.count("/plugin.json") != 0 {
				t.Fatal("malformed lock contacted network")
			}
		})
	}
}

func TestConcurrentCachePopulationDownloadsArchiveOnce(t *testing.T) {
	t.Parallel()
	manifest, archive := releaseBytes(t, "acme", "1")
	transport := transportFor(manifest, archive)
	cache := t.TempDir()
	managers := make([]*Manager, 8)
	origins := make([]workflow.PluginOrigin, 8)
	for i := range managers {
		managers[i] = resolutionManager(t, cache, transport)
		origins[i] = workflow.PluginOrigin{Dir: t.TempDir()}
	}
	failures := make(chan error, len(managers))
	for i, manager := range managers {
		go func() {
			_, err := manager.LockPluginSources(t.Context(), origins[i], acmeSource(), false, map[string]string{"acme": digest(manifest)})
			failures <- err
		}()
	}
	for range managers {
		if err := <-failures; err != nil {
			t.Fatal(err)
		}
	}
	if transport.count("/plugin.json") != 1 || transport.count("/plugin.tar.gz") != 1 {
		t.Fatalf("downloads: manifest=%d archive=%d", transport.count("/plugin.json"), transport.count("/plugin.tar.gz"))
	}
}

func TestConcurrentLockUpdatesPreserveBothReleases(t *testing.T) {
	t.Parallel()
	manifest, archive := releaseBytes(t, "acme", "1")
	transport := transportFor(manifest, archive)
	// Distinct source identities can share manifest content.
	transport.bodies["/other.json"] = manifest
	cache, dir := t.TempDir(), t.TempDir()
	failures := make(chan error, 2)
	for _, source := range []string{"https://plugins.test/plugin.json", "https://plugins.test/other.json"} {
		go func() {
			manager := resolutionManager(t, cache, transport)
			_, err := manager.ResolvePluginSources(t.Context(), workflow.PluginOrigin{Dir: dir}, map[string]workflow.PluginSource{"acme": {Source: source}})
			failures <- err
		}()
	}
	for range 2 {
		if err := <-failures; err != nil {
			t.Fatal(err)
		}
	}
	doc, err := readLock(filepath.Join(dir, LockfileName))
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Plugins) != 2 {
		t.Fatalf("entries=%v", doc.Plugins)
	}
}

func TestCachePopulationCancellationLeavesNoRelease(t *testing.T) {
	t.Parallel()
	manifest, archive := releaseBytes(t, "acme", "1")
	manager := resolutionManager(t, t.TempDir(), transportFor(manifest, archive))
	directory, _ := manager.store.directory(digest(manifest))
	testfs.MkdirAll(t, filepath.Dir(directory), 0700)
	handle, err := filelock.Acquire(t.Context(), directory+".lock")
	if err != nil {
		t.Fatal(err)
	}
	defer handle.Release()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := manager.store.pinned(ctx, "acme", "https://plugins.test/plugin.json", digest(manifest)); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := os.Stat(directory); !os.IsNotExist(err) {
		t.Fatalf("partial entry exists: %v", err)
	}
}

func TestLocalBundlesResolveRelativeToWorkflowAndRejectRemoteEscape(t *testing.T) {
	t.Parallel()
	manifest, archive := releaseBytes(t, "acme", "1")
	dir := t.TempDir()
	bundle := filepath.Join(dir, "plugins", "acme")
	mustWrite(t, filepath.Join(bundle, "plugin.json"), manifest)
	mustWrite(t, filepath.Join(bundle, "plugin.tar.gz"), archive)
	manager := resolutionManager(t, t.TempDir(), transportFor(nil, nil))
	sources := map[string]workflow.PluginSource{"acme": {Source: "./plugins/acme"}}
	resolved, err := manager.ResolvePluginSources(t.Context(), workflow.PluginOrigin{Dir: dir}, sources)
	if err != nil {
		t.Fatal(err)
	}
	if resolved["acme"].Source != filepath.Join(bundle, "plugin.json") {
		t.Fatal(resolved)
	}
	doc, err := readLock(filepath.Join(dir, LockfileName))
	if err != nil {
		t.Fatal(err)
	}
	if doc.Plugins[0].Source != "./plugins/acme/plugin.json" {
		t.Fatal(doc)
	}
	remoteDir := t.TempDir()
	if _, err := manager.ResolvePluginSources(t.Context(), workflow.PluginOrigin{Dir: remoteDir, Root: remoteDir, Remote: true}, map[string]workflow.PluginSource{"acme": {Source: filepath.Join(bundle, "plugin.json")}}); err == nil {
		t.Fatal("remote bundle escaped package")
	}
}

func TestLockWorkflowsSharesReleasesAndMigratesWithoutStartingPlugins(t *testing.T) {
	t.Parallel()
	manifest, archive := releaseBytes(t, "acme", "1") // Executable exits 97 if accidentally launched.
	transport := transportFor(manifest, archive)
	manager := resolutionManager(t, t.TempDir(), transport)
	dir := t.TempDir()
	build := filepath.Join(dir, "build.yaml")
	test := filepath.Join(dir, "test.yaml")
	body := "# keep this comment\nversion: 1\nname: example\nplugins:\n  acme:\n    source: https://plugins.test/plugin.json\n    sha256: " + digest(manifest) + "\nsteps:\n  - type: acme.uppercase\n"
	mustWrite(t, build, []byte(body))
	mustWrite(t, test, []byte(strings.Replace(body, "name: example", "name: test", 1)))
	if _, err := manager.LockWorkflows(t.Context(), []string{build, test}, false, true); err != nil {
		t.Fatal(err)
	}
	migrated := mustRead(t, build)
	if bytes.Contains(migrated, []byte("sha256:")) || !bytes.Contains(migrated, []byte("# keep this comment")) {
		t.Fatalf("migration = %s", migrated)
	}
	doc, err := readLock(filepath.Join(dir, LockfileName))
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Plugins) != 1 || doc.Plugins[0].SHA256 != digest(manifest) {
		t.Fatal(doc)
	}
	if transport.count("/plugin.json") != 1 || transport.count("/plugin.tar.gz") != 1 {
		t.Fatal("shared release downloaded repeatedly")
	}
}

func TestFailedBatchDoesNotPublishPartialLock(t *testing.T) {
	t.Parallel()
	manifest, archive := releaseBytes(t, "acme", "1")
	manager := resolutionManager(t, t.TempDir(), transportFor(manifest, archive))
	dir := t.TempDir()
	first, second := filepath.Join(dir, "first.yaml"), filepath.Join(dir, "second.yaml")
	mustWrite(t, first, []byte("version: 1\nname: first\nplugins:\n  acme:\n    source: https://plugins.test/plugin.json\nsteps: []\n"))
	mustWrite(t, second, []byte("version: 1\nname: second\nplugins:\n  missing:\n    source: https://plugins.test/missing.json\nsteps: []\n"))
	if _, err := manager.LockWorkflows(t.Context(), []string{first, second}, false, false); err == nil {
		t.Fatal("expected missing release error")
	}
	if _, err := os.Stat(filepath.Join(dir, LockfileName)); !os.IsNotExist(err) {
		t.Fatalf("partial lock exists: %v", err)
	}
}

func TestRetainedInstallationCanSeedCacheOffline(t *testing.T) {
	t.Parallel()
	manifest, archive := releaseBytes(t, "acme", "1")
	transport := transportFor(nil, nil)
	manager := resolutionManager(t, t.TempDir(), transport)
	project := t.TempDir()
	manager.store.config.CWD = func() (string, error) { return project, nil }
	manifestObject, err := ParseManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := manifestObject.CurrentArtifact()
	if err != nil {
		t.Fatal(err)
	}
	release := Release{Manifest: manifestObject, ManifestData: manifest, ManifestDigest: digest(manifest), Artifact: artifact, ArtifactData: archive}
	if err := writeRelease(filepath.Join(project, ".wuko", "plugins", "acme", releaseDirectory), release); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.LockPluginSources(t.Context(), workflow.PluginOrigin{Dir: project}, acmeSource(), false, map[string]string{"acme": digest(manifest)}); err != nil {
		t.Fatal(err)
	}
	if transport.count("/plugin.json") != 0 {
		t.Fatal("matching install contacted source")
	}
}

func TestLockSubprocessHelper(t *testing.T) {
	t.Parallel()
	path := os.Getenv("WUKO_LOCK_TEST_WORKFLOW")
	if path == "" {
		return
	}
	cache := os.Getenv("WUKO_LOCK_TEST_CACHE")
	manager := NewManager(Config{CacheDir: func() (string, error) { return cache, nil }})
	if _, err := manager.LockWorkflows(t.Context(), []string{path}, false, false); err != nil {
		t.Fatal(err)
	}
}

func TestIndependentProcessesMergeSharedLock(t *testing.T) {
	t.Parallel()
	manifest, archive := releaseBytes(t, "acme", "1")
	dir, cache := t.TempDir(), t.TempDir()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	var processes []*exec.Cmd
	var outputs []*bytes.Buffer
	for i := range 4 {
		bundle := fmt.Sprintf("bundle-%d", i)
		mustWrite(t, filepath.Join(dir, bundle, "plugin.json"), manifest)
		mustWrite(t, filepath.Join(dir, bundle, "plugin.tar.gz"), archive)
		path := filepath.Join(dir, fmt.Sprintf("workflow-%d.yaml", i))
		mustWrite(t, path, []byte("version: 1\nname: concurrent\nplugins:\n  acme:\n    source: ./"+bundle+"\nsteps: []\n"))
		command := exec.CommandContext(t.Context(), executable, "-test.run=^TestLockSubprocessHelper$")
		command.Env = append(command.Environ(), "WUKO_LOCK_TEST_WORKFLOW="+path, "WUKO_LOCK_TEST_CACHE="+cache)
		output := &bytes.Buffer{}
		command.Stdout = output
		command.Stderr = output
		if err := command.Start(); err != nil {
			t.Fatal(err)
		}
		processes = append(processes, command)
		outputs = append(outputs, output)
	}
	for i, command := range processes {
		if err := command.Wait(); err != nil {
			t.Fatalf("process %d: %v\n%s", i, err, outputs[i])
		}
	}
	doc, err := readLock(filepath.Join(dir, LockfileName))
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Plugins) != 4 {
		t.Fatalf("lost concurrent pin: %v", doc.Plugins)
	}
	entries, err := os.ReadDir(filepath.Join(cache, "v1", digest(manifest)))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != runtime.GOOS+"-"+runtime.GOARCH {
		t.Fatalf("incomplete cache publication: %v", entries)
	}
}

func TestLockWorkflowDependencyGetsItsOwnDirectoryLock(t *testing.T) {
	t.Parallel()
	manifest, archive := releaseBytes(t, "acme", "1")
	dir := t.TempDir()
	workflows := filepath.Join(dir, ".wuko", "workflows")
	parent := filepath.Join(workflows, "parent.yaml")
	child := filepath.Join(workflows, "child", "wuko.yaml")
	mustWrite(t, parent, []byte("version: 1\nname: parent\ndepends_on:\n  prerequisite: child\nsteps: []\n"))
	mustWrite(t, filepath.Join(filepath.Dir(child), workflow.WorkflowPackageMarkerName), []byte(`{"version":1,"name":"child"}`))
	mustWrite(t, child, []byte("version: 1\nname: child\nplugins:\n  acme:\n    source: https://plugins.test/plugin.json\nsteps: []\n"))
	manager := resolutionManager(t, t.TempDir(), transportFor(manifest, archive))
	manager.config.CWD = func() (string, error) { return dir, nil }
	if _, err := manager.LockWorkflows(t.Context(), []string{parent}, false, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(child), LockfileName)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(workflows, LockfileName)); !os.IsNotExist(err) {
		t.Fatalf("parent without plugins acquired lock: %v", err)
	}
}

func TestFailedMigrationKeepsInlinePinAndExistingLock(t *testing.T) {
	t.Parallel()
	manifest, archive := releaseBytes(t, "acme", "1")
	manager := resolutionManager(t, t.TempDir(), transportFor(manifest, archive))
	dir := t.TempDir()
	path := filepath.Join(dir, "demo.yaml")
	body := []byte("# preserve\nversion: 1\nname: demo\nplugins:\n  acme:\n    source: https://plugins.test/plugin.json\n    sha256: " + strings.Repeat("b", 64) + "\nsteps: []\n")
	mustWrite(t, path, body)
	if _, err := manager.LockWorkflows(t.Context(), []string{path}, false, true); err == nil {
		t.Fatal("accepted mismatched legacy pin")
	}
	if !bytes.Equal(body, mustRead(t, path)) {
		t.Fatal("failed migration changed workflow")
	}
	if _, err := os.Stat(filepath.Join(dir, LockfileName)); !os.IsNotExist(err) {
		t.Fatal("failed migration published lock")
	}
}

func TestUnsafeCachedArchiveCannotEstablishPin(t *testing.T) {
	t.Parallel()
	manifest, _ := releaseBytes(t, "acme", "unsafe")
	var object Manifest
	if err := json.Unmarshal(manifest, &object); err != nil {
		t.Fatal(err)
	}
	archive := archivetest.TarGz(t, archivetest.Entry{Name: "../wuko-plugin-acme", Mode: 0700, Data: []byte("bad")})
	object.Artifacts[0].SHA256 = digest(archive)
	manifest, err := json.Marshal(object)
	if err != nil {
		t.Fatal(err)
	}
	manager := resolutionManager(t, t.TempDir(), transportFor(manifest, archive))
	directory, _ := manager.store.directory(digest(manifest))
	if err := writeRelease(directory, Release{ManifestData: manifest, ArtifactData: archive}); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if _, err := manager.ResolvePluginSources(t.Context(), workflow.PluginOrigin{Dir: dir}, acmeSource()); err == nil {
		t.Fatal("unsafe cached archive established pin")
	}
	if _, err := os.Stat(filepath.Join(dir, LockfileName)); !os.IsNotExist(err) {
		t.Fatal("unsafe release wrote lock")
	}
}

func TestLoadedNamespaceStillRequiresCurrentWorkflowDeclaration(t *testing.T) {
	t.Parallel()
	manifest, archive := pluginFixture(t, "acme")
	// This helper fixture uses the namespace-specific artifact path.
	transport := transportFor(manifest, archive)
	transport.bodies["/acme.tar.gz"] = archive
	manager := resolutionManager(t, t.TempDir(), transport)
	sources := map[string]workflow.ResolvedPluginSource{"acme": {Source: "https://plugins.test/plugin.json", SHA256: digest(manifest)}}
	if _, err := manager.LoadHelpers(t.Context(), sources); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close(context.Background(), "completed") })
	for _, resolve := range []func() error{
		func() error { _, err := manager.ResolveStep(t.Context(), "acme.uppercase", nil); return err },
		func() error { _, err := manager.ResolveExecutor(t.Context(), "acme.local", nil); return err },
		func() error { _, _, err := manager.ResolveOutputSchema(t.Context(), "acme.uppercase"); return err },
		func() error { _, err := manager.LoadAction(t.Context(), "acme", "build"); return err },
	} {
		if err := resolve(); err == nil || !strings.Contains(err.Error(), "must be declared") {
			t.Fatalf("error=%v", err)
		}
	}
}

func TestPackagedLocalPluginIdentitySurvivesRemoteCopy(t *testing.T) {
	t.Parallel()
	manifest, archive := releaseBytes(t, "acme", "1")
	manager := resolutionManager(t, t.TempDir(), transportFor(nil, nil))
	var previous workflow.ResolvedPluginSource
	for range 2 {
		dir := t.TempDir()
		mustWrite(t, filepath.Join(dir, "bundle", "plugin.json"), manifest)
		mustWrite(t, filepath.Join(dir, "bundle", "plugin.tar.gz"), archive)
		origin := workflow.PluginOrigin{Dir: dir, Root: dir, Remote: true, Source: "https://example.test/package.tar.gz"}
		resolved, err := manager.ResolvePluginSources(t.Context(), origin, map[string]workflow.PluginSource{"acme": {Source: "./bundle"}})
		if err != nil {
			t.Fatal(err)
		}
		source := resolved["acme"]
		if previous.Source != "" && source.SourceIdentity() != previous.SourceIdentity() {
			t.Fatalf("identities differ: %v %v", previous, source)
		}
		if err := manager.configureSources(resolved); err != nil {
			t.Fatal(err)
		}
		previous = source
		if _, err := os.Stat(filepath.Join(dir, LockfileName)); !os.IsNotExist(err) {
			t.Fatal("packaged remote plugin wrote lock")
		}
	}
}

func TestLockedResolutionDoesNotTakeDirectoryLock(t *testing.T) {
	t.Parallel()
	manifest, archive := releaseBytes(t, "acme", "1.0.0")
	cache, dir := t.TempDir(), t.TempDir()
	manager := resolutionManager(t, cache, transportFor(manifest, archive))
	origin := workflow.PluginOrigin{Dir: dir}
	if _, err := manager.ResolvePluginSources(t.Context(), origin, acmeSource()); err != nil {
		t.Fatal(err)
	}
	lockGuard := filepath.Join(dir, LockfileName+".lock")
	if err := os.Remove(lockGuard); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if _, err := manager.ResolvePluginSources(t.Context(), origin, acmeSource()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(lockGuard); !os.IsNotExist(err) {
		t.Fatalf("locked resolution created %s: %v", lockGuard, err)
	}
}

// stallingTransport holds requests for one path until released, honoring cancellation.
type stallingTransport struct {
	inner   http.RoundTripper
	path    string
	entered chan struct{}
	release chan struct{}
}

func (s *stallingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Path == s.path {
		select {
		case s.entered <- struct{}{}:
		default:
		}
		select {
		case <-s.release:
		case <-req.Context().Done():
			return nil, req.Context().Err()
		}
	}
	return s.inner.RoundTrip(req)
}

func TestFreshResolutionDoesNotSerializeUnrelatedSources(t *testing.T) {
	t.Parallel()
	acmeManifest, acmeArchive := releaseBytes(t, "acme", "1")
	slowManifest, slowArchive := releaseBytes(t, "slow", "1")
	transport := transportFor(acmeManifest, acmeArchive)
	transport.bodies["/slow/plugin.json"] = slowManifest
	transport.bodies["/slow/plugin.tar.gz"] = slowArchive
	stalling := &stallingTransport{inner: transport, path: "/slow/plugin.json", entered: make(chan struct{}, 1), release: make(chan struct{})}
	cache := t.TempDir()
	store := &releaseStore{config: Config{CacheDir: func() (string, error) { return cache, nil }, HTTPClient: &http.Client{Transport: stalling}}}

	const slowSource = "https://plugins.test/slow/plugin.json"
	results := make(chan error, 2)
	for range 2 {
		go func() {
			_, err := store.fresh(t.Context(), "slow", slowSource)
			results <- err
		}()
	}
	<-stalling.entered
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if _, err := store.fresh(ctx, "acme", "https://plugins.test/plugin.json"); err != nil {
		t.Fatalf("unrelated source waited on a stalled fetch: %v", err)
	}
	close(stalling.release)
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	if count := transport.count("/slow/plugin.json"); count != 1 {
		t.Fatalf("shared source manifest fetched %d times, want 1", count)
	}
}

func TestFreshResolutionRetriesAfterFailure(t *testing.T) {
	t.Parallel()
	manifest, archive := releaseBytes(t, "acme", "1")
	transport := &releaseTransport{bodies: map[string][]byte{}, counts: make(map[string]int)}
	manager := resolutionManager(t, t.TempDir(), transport)
	if _, err := manager.store.fresh(t.Context(), "acme", "https://plugins.test/plugin.json"); err == nil {
		t.Fatal("expected unavailable source to fail")
	}
	transport.replace(map[string][]byte{"/plugin.json": manifest, "/plugin.tar.gz": archive})
	release, err := manager.store.fresh(t.Context(), "acme", "https://plugins.test/plugin.json")
	if err != nil {
		t.Fatalf("failed fetch was remembered: %v", err)
	}
	if release.ManifestDigest != digest(manifest) {
		t.Fatalf("pin = %s, want %s", release.ManifestDigest, digest(manifest))
	}
}

func TestFreshResolutionWaiterHonorsCancellation(t *testing.T) {
	t.Parallel()
	manifest, archive := releaseBytes(t, "acme", "1")
	stalling := &stallingTransport{inner: transportFor(manifest, archive), path: "/plugin.json", entered: make(chan struct{}, 1), release: make(chan struct{})}
	cache := t.TempDir()
	store := &releaseStore{config: Config{CacheDir: func() (string, error) { return cache, nil }, HTTPClient: &http.Client{Transport: stalling}}}
	leader := make(chan error, 1)
	go func() {
		_, err := store.fresh(t.Context(), "acme", "https://plugins.test/plugin.json")
		leader <- err
	}()
	<-stalling.entered
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := store.fresh(ctx, "acme", "https://plugins.test/plugin.json"); !errors.Is(err, context.Canceled) {
		t.Fatalf("waiter error = %v, want context.Canceled", err)
	}
	close(stalling.release)
	if err := <-leader; err != nil {
		t.Fatal(err)
	}
}
