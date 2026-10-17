package plugin

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sync"

	"github.com/up2jj/wuko/internal/filelock"
	"github.com/up2jj/wuko/internal/fsutil"
)

const releaseDirectory = ".wuko-release"

// releaseStore retains original, verifiable release bytes. Executables are always
// extracted into a private run directory, never executed from mutable cache files.
type releaseStore struct {
	config    Config
	freshMu   sync.Mutex
	freshPins map[string]*freshResolution
}

// freshResolution lets concurrent callers share one manifest fetch per source.
type freshResolution struct {
	done chan struct{}
	pin  string
	err  error
}

func (s *releaseStore) root() (string, error) {
	if s.config.CacheDir != nil {
		return s.config.CacheDir()
	}
	if root := os.Getenv("WUKO_PLUGIN_CACHE_DIR"); root != "" {
		return filepath.Abs(root)
	}
	root, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("locating plugin cache: %w", err)
	}
	return filepath.Join(root, "wuko", "plugins"), nil
}

func (s *releaseStore) directory(pin string) (string, error) {
	if !validDigest(pin) {
		return "", fmt.Errorf("invalid plugin manifest sha256")
	}
	root, err := s.root()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "v1", pin, runtime.GOOS+"-"+runtime.GOARCH), nil
}

func readReleaseFile(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxArchiveBytes {
		return nil, fmt.Errorf("invalid cached release file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxArchiveBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxArchiveBytes {
		return nil, fmt.Errorf("cached release exceeds byte limit")
	}
	return data, nil
}

func readCachedRelease(directory, namespace, pin, source string) (Release, error) {
	manifest, manifestData, artifact, err := readCachedManifest(directory, namespace, pin)
	if err != nil {
		return Release{}, err
	}
	data, err := readReleaseFile(filepath.Join(directory, "artifact.tar.gz"))
	if err != nil {
		return Release{}, err
	}
	if digest(data) != artifact.SHA256 {
		return Release{}, fmt.Errorf("cached artifact sha256 mismatch")
	}
	return Release{Manifest: manifest, ManifestData: manifestData, ManifestDigest: pin, Artifact: artifact, ArtifactData: data, CanonicalSource: diagnosticPluginSource(source)}, nil
}

// readCachedManifest verifies the small manifest half of a cached release.
func readCachedManifest(directory, namespace, pin string) (Manifest, []byte, Artifact, error) {
	manifestData, err := readReleaseFile(filepath.Join(directory, "plugin.json"))
	if err != nil {
		return Manifest{}, nil, Artifact{}, err
	}
	if digest(manifestData) != pin {
		return Manifest{}, nil, Artifact{}, fmt.Errorf("cached manifest sha256 mismatch")
	}
	manifest, err := ParseManifest(manifestData)
	if err != nil {
		return Manifest{}, nil, Artifact{}, err
	}
	if manifest.Namespace != namespace {
		return Manifest{}, nil, Artifact{}, fmt.Errorf("plugin declaration namespace %q conflicts with manifest namespace %q", namespace, manifest.Namespace)
	}
	artifact, err := manifest.CurrentArtifact()
	if err != nil {
		return Manifest{}, nil, Artifact{}, err
	}
	return manifest, manifestData, artifact, nil
}

// present confirms a pinned release is available without reading its archive. pinned
// hashes the archive before any plugin starts, so resolution only checks the manifest
// and that a plausible archive file exists, falling back to a full obtain otherwise.
func (s *releaseStore) present(ctx context.Context, namespace, source, pin string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	directory, err := s.directory(pin)
	if err != nil {
		return err
	}
	if _, _, _, err := readCachedManifest(directory, namespace, pin); err == nil {
		info, statErr := os.Lstat(filepath.Join(directory, "artifact.tar.gz"))
		if statErr == nil && info.Mode().IsRegular() && info.Size() > 0 && info.Size() <= maxArchiveBytes {
			return nil
		}
	}
	_, err = s.pinned(ctx, namespace, source, pin)
	return err
}

func writeRelease(directory string, release Release) error {
	if err := os.MkdirAll(directory, 0700); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(directory, "plugin.json"), release.ManifestData, 0600); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(directory, "artifact.tar.gz"), release.ArtifactData, 0600)
}

// fresh fetches a manifest once per invocation, even when its archive is cached.
// Callers for the same source share that fetch so they agree on one release, while
// unrelated sources resolve in parallel. Failed fetches are not remembered.
func (s *releaseStore) fresh(ctx context.Context, namespace, source string) (Release, error) {
	key := namespace + "\x00" + source
	for {
		s.freshMu.Lock()
		entry, shared := s.freshPins[key]
		if !shared {
			entry = &freshResolution{done: make(chan struct{})}
			if s.freshPins == nil {
				s.freshPins = make(map[string]*freshResolution)
			}
			s.freshPins[key] = entry
		}
		s.freshMu.Unlock()
		if !shared {
			return s.resolveFresh(ctx, key, namespace, source, entry)
		}
		select {
		case <-entry.done:
		case <-ctx.Done():
			return Release{}, ctx.Err()
		}
		if entry.err == nil {
			return s.pinned(ctx, namespace, source, entry.pin)
		}
	}
}

func (s *releaseStore) resolveFresh(ctx context.Context, key, namespace, source string, entry *freshResolution) (release Release, err error) {
	defer func() {
		if err == nil {
			entry.pin = release.ManifestDigest
		} else {
			entry.err = err
			s.freshMu.Lock()
			if s.freshPins[key] == entry {
				delete(s.freshPins, key)
			}
			s.freshMu.Unlock()
		}
		close(entry.done)
	}()
	return s.obtain(ctx, namespace, source, "")
}

func (s *releaseStore) reset() {
	s.freshMu.Lock()
	defer s.freshMu.Unlock()
	clear(s.freshPins)
}

func (s *releaseStore) pinned(ctx context.Context, namespace, source, pin string) (Release, error) {
	return s.obtain(ctx, namespace, source, pin)
}

func (s *releaseStore) obtain(ctx context.Context, namespace, source, pin string) (Release, error) {
	if err := ctx.Err(); err != nil {
		return Release{}, err
	}
	var manifestData []byte
	var resolver artifactResolver
	var err error
	if pin == "" {
		manifestData, _, resolver, err = fetchManifest(ctx, source, s.config.HTTPClient)
		if err != nil {
			return Release{}, err
		}
		pin = digest(manifestData)
	}
	directory, err := s.directory(pin)
	if err != nil {
		return Release{}, err
	}
	// Atomic publication lets readers verify complete entries without taking a lock.
	if release, err := readCachedRelease(directory, namespace, pin, source); err == nil {
		return release, nil
	}
	if err := os.MkdirAll(filepath.Dir(directory), 0700); err != nil {
		return Release{}, fmt.Errorf("creating plugin cache: %w", err)
	}
	handle, err := filelock.Acquire(ctx, directory+".lock")
	if err != nil {
		return Release{}, err
	}
	defer handle.Release()
	if release, err := readCachedRelease(directory, namespace, pin, source); err == nil {
		return release, nil
	}
	var release Release
	for _, root := range s.installationRoots() {
		candidate, readErr := readCachedRelease(filepath.Join(root, namespace, releaseDirectory), namespace, pin, source)
		if readErr == nil {
			release = candidate
			break
		}
	}
	if release.ManifestDigest == "" {
		if manifestData == nil {
			manifestData, _, resolver, err = fetchManifest(ctx, source, s.config.HTTPClient)
			if err != nil {
				return Release{}, err
			}
		}
		if digest(manifestData) != pin {
			return Release{}, fmt.Errorf("plugin manifest sha256 mismatch for %s: the pinned release is not cached and upstream has changed; run wuko plugin lock FILE --update", diagnosticPluginSource(source))
		}
		manifest, err := ParseManifest(manifestData)
		if err != nil {
			return Release{}, err
		}
		if manifest.Namespace != namespace {
			return Release{}, fmt.Errorf("plugin declaration namespace %q conflicts with manifest namespace %q", namespace, manifest.Namespace)
		}
		artifact, err := manifest.CurrentArtifact()
		if err != nil {
			return Release{}, err
		}
		data, err := resolver(ctx, artifact.Path)
		if err != nil {
			return Release{}, err
		}
		if digest(data) != artifact.SHA256 {
			return Release{}, fmt.Errorf("plugin artifact sha256 mismatch")
		}
		release = Release{Manifest: manifest, ManifestData: manifestData, ManifestDigest: pin, Artifact: artifact, ArtifactData: data, CanonicalSource: diagnosticPluginSource(source)}
	}
	return s.publishLocked(ctx, directory, release)
}

func (s *releaseStore) publishLocked(ctx context.Context, directory string, release Release) (Release, error) {
	if err := ValidateArtifact(release.Artifact, release.ArtifactData); err != nil {
		return Release{}, err
	}
	if err := ctx.Err(); err != nil {
		return Release{}, err
	}
	stage, err := os.MkdirTemp(filepath.Dir(directory), ".plugin-cache-")
	if err != nil {
		return Release{}, err
	}
	defer os.RemoveAll(stage)
	if err := writeRelease(stage, release); err != nil {
		return Release{}, err
	}
	// Remove only a corrupt entry, while holding its population lock.
	if err := os.RemoveAll(directory); err != nil {
		return Release{}, err
	}
	if err := os.Rename(stage, directory); err != nil {
		return Release{}, fmt.Errorf("publishing plugin cache: %w", err)
	}
	return release, nil
}

func (s *releaseStore) installationRoots() []string {
	var roots []string
	if s.config.CWD != nil {
		if cwd, err := s.config.CWD(); err == nil {
			for dir := cwd; ; dir = filepath.Dir(dir) {
				roots = append(roots, filepath.Join(dir, ".wuko", "plugins"))
				if filepath.Dir(dir) == dir {
					break
				}
			}
		}
	}
	if s.config.HomeDir != nil {
		if home, err := s.config.HomeDir(); err == nil {
			roots = append(roots, filepath.Join(home, ".wuko", "plugins"))
		}
	}
	if s.config.ConfigDir != nil {
		if config, err := s.config.ConfigDir(); err == nil {
			roots = append(roots, filepath.Join(config, "wuko", "plugins"))
		}
	}
	return roots
}

func writeAtomic(path string, data []byte) error {
	if old, err := os.ReadFile(path); err == nil && bytes.Equal(old, data) {
		return nil
	}
	mode := os.FileMode(0600)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	return fsutil.WriteFileAtomic(path, data, mode)
}

func (s *releaseStore) seed(ctx context.Context, release Release) error {
	directory, err := s.directory(release.ManifestDigest)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(directory), 0700); err != nil {
		return err
	}
	handle, err := filelock.Acquire(ctx, directory+".lock")
	if err != nil {
		return err
	}
	defer handle.Release()
	if _, err := readCachedRelease(directory, release.Manifest.Namespace, release.ManifestDigest, release.CanonicalSource); err == nil {
		return nil
	}
	_, err = s.publishLocked(ctx, directory, release)
	return err
}

// CacheInstallations imports retained release bytes into this manager's shared cache.
func (m *Manager) CacheInstallations(ctx context.Context, root string, markers []InstallationMarker) error {
	for _, marker := range markers {
		release, err := readCachedRelease(filepath.Join(root, marker.Namespace, releaseDirectory), marker.Namespace, marker.ManifestDigest, marker.Source)
		if err != nil {
			return err
		}
		if err := m.store.seed(ctx, release); err != nil {
			return err
		}
	}
	return nil
}
