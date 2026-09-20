package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestInstallIsTransactionalAndWritesMarker(t *testing.T) {
	source := t.TempDir()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	archive := makeArchive(t, "wuko-plugin-acme", 0755, []byte("#!/bin/sh\nWUKO_PLUGIN_TEST_HELPER=1 exec \""+executable+"\"\n"))
	if err := os.WriteFile(filepath.Join(source, "plugin.tar.gz"), archive, 0600); err != nil {
		t.Fatal(err)
	}
	manifest, _ := json.Marshal(Manifest{Version: 1, Namespace: "acme", PluginVersion: "1.0.0", Protocol: Protocol, Artifacts: []Artifact{{OS: runtime.GOOS, Arch: runtime.GOARCH, Path: "plugin.tar.gz", Format: "tar.gz", Entry: "wuko-plugin-acme", SHA256: digest(archive)}}})
	if err := os.WriteFile(filepath.Join(source, "plugin.json"), manifest, 0600); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	marker, err := Install(context.Background(), source, root, false, nil, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if marker.Namespace != "acme" {
		t.Fatal(marker)
	}
	if marker.Protocol != Protocol {
		t.Fatalf("marker protocol = %q", marker.Protocol)
	}
	directory := filepath.Join(root, "acme")
	stored, err := os.ReadFile(filepath.Join(directory, MarkerName))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(stored), `"marker_version": 2`) || !strings.Contains(string(stored), `"executable_digest"`) || !strings.Contains(string(stored), `"protocol": "`+Protocol+`"`) {
		t.Fatalf("v2 marker is incomplete: %s", stored)
	}
	if _, err := ValidateInstallation(directory, "acme"); err != nil {
		t.Fatal(err)
	}
	installedExecutable := filepath.Join(directory, "wuko-plugin-acme")
	originalExecutable, err := os.ReadFile(installedExecutable)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(installedExecutable, append(originalExecutable, '\n'), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateInstallation(directory, "acme"); err == nil || !strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("tampered executable error = %v", err)
	}
	if err := os.WriteFile(installedExecutable, originalExecutable, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := Install(context.Background(), source, root, false, nil, io.Discard); err == nil {
		t.Fatal("expected existing installation error")
	}
	if err := os.WriteFile(filepath.Join(directory, MarkerName), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateInstallation(directory, "acme"); err == nil {
		t.Fatal("expected corrupted marker error")
	}
}

func TestInstallReleasesRollsBackEveryCommittedPlugin(t *testing.T) {
	root := t.TempDir()
	for _, namespace := range []string{"acme", "beta"} {
		directory := filepath.Join(root, namespace)
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(directory, "old"), []byte(namespace), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	releases := []Release{testInstallRelease(t, "acme"), testInstallRelease(t, "beta")}
	commitErr := errors.New("injected publish failure")
	ops := installationOps{
		rename: func(oldPath, newPath string) error {
			if strings.HasPrefix(filepath.Base(oldPath), ".plugin-stage-") && filepath.Base(newPath) == "beta" {
				return commitErr
			}
			return os.Rename(oldPath, newPath)
		},
		removeAll: os.RemoveAll,
	}
	if _, err := installReleasesWithOps(t.Context(), releases, root, true, io.Discard, ops); !errors.Is(err, commitErr) {
		t.Fatalf("install error = %v", err)
	}
	for _, namespace := range []string{"acme", "beta"} {
		data, err := os.ReadFile(filepath.Join(root, namespace, "old"))
		if err != nil || string(data) != namespace {
			t.Fatalf("plugin %s was not restored: %q, %v", namespace, data, err)
		}
	}
}

func TestInstallReleasesSurfacesBackupCleanupFailure(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "acme")
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatal(err)
	}
	cleanupErr := errors.New("injected cleanup failure")
	ops := installationOps{
		rename: os.Rename,
		removeAll: func(path string) error {
			if strings.HasPrefix(filepath.Base(path), ".plugin-backup-") {
				return cleanupErr
			}
			return os.RemoveAll(path)
		},
	}
	markers, err := installReleasesWithOps(t.Context(), []Release{testInstallRelease(t, "acme")}, root, true, io.Discard, ops)
	if !errors.Is(err, cleanupErr) || len(markers) != 1 {
		t.Fatalf("markers = %#v, error = %v", markers, err)
	}
	if _, err := ValidateInstallation(target, "acme"); err != nil {
		t.Fatalf("committed installation is invalid: %v", err)
	}
}

func testInstallRelease(t *testing.T, namespace string) Release {
	t.Helper()
	script := []byte("#!/bin/sh\nread line\nprintf '{\"id\":\"1\",\"result\":{\"protocol\":\"" + Protocol + "\",\"namespace\":\"" + namespace + "\",\"steps\":[],\"executors\":[],\"helpers\":[]}}\\n'\nread line\nprintf '{\"id\":\"2\",\"result\":{}}\\n'\n")
	archive := makeArchive(t, "wuko-plugin-"+namespace, 0o755, script)
	manifest := Manifest{Version: 1, Namespace: namespace, PluginVersion: "1.0.0", Protocol: Protocol, Artifacts: []Artifact{{OS: runtime.GOOS, Arch: runtime.GOARCH, Path: "plugin.tar.gz", Format: "tar.gz", Entry: "wuko-plugin-" + namespace, SHA256: digest(archive)}}}
	manifestData, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	return Release{Manifest: manifest, ManifestData: manifestData, ManifestDigest: digest(manifestData), Artifact: manifest.Artifacts[0], ArtifactData: archive, CanonicalSource: "/tmp/" + namespace + "/plugin.json"}
}

func TestLegacyInstallationMarkerDefaultsToV1(t *testing.T) {
	directory := t.TempDir()
	executable := filepath.Join(directory, "wuko-plugin-acme")
	if err := os.WriteFile(executable, []byte("binary"), 0o700); err != nil {
		t.Fatal(err)
	}
	marker := map[string]any{
		"manifest_version": 1, "namespace": "acme", "plugin_version": "1.0.0",
		"source": "https://plugins.test/acme", "manifest_digest": strings.Repeat("a", 64),
		"artifact_digest": strings.Repeat("b", 64), "os": runtime.GOOS, "arch": runtime.GOARCH,
	}
	data, err := json.Marshal(marker)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, MarkerName), data, 0o600); err != nil {
		t.Fatal(err)
	}
	validated, err := ValidateInstallation(directory, "acme")
	if err != nil {
		t.Fatal(err)
	}
	if validated.Protocol != ProtocolV1 {
		t.Fatalf("legacy marker protocol = %q", validated.Protocol)
	}
	marker["os"] = "plan9"
	data, err = json.Marshal(marker)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, MarkerName), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateInstallation(directory, "acme"); err == nil || !strings.Contains(err.Error(), "running on") {
		t.Fatalf("platform mismatch error = %v", err)
	}
}

func TestInstallPinnedRejectsMarketplaceManifestMismatch(t *testing.T) {
	source := t.TempDir()
	archive := makeArchive(t, "wuko-plugin-acme", 0o755, []byte("binary"))
	if err := os.WriteFile(filepath.Join(source, "plugin.tar.gz"), archive, 0o600); err != nil {
		t.Fatal(err)
	}
	manifest, err := json.Marshal(Manifest{Version: 1, Namespace: "acme", PluginVersion: "1.0.0", Protocol: Protocol, Artifacts: []Artifact{{OS: runtime.GOOS, Arch: runtime.GOARCH, Path: "plugin.tar.gz", Format: "tar.gz", Entry: "wuko-plugin-acme", SHA256: digest(archive)}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "plugin.json"), manifest, 0o600); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if _, err := InstallPinned(t.Context(), source, strings.Repeat("a", 64), root, false, nil, io.Discard); err == nil || !strings.Contains(err.Error(), "manifest sha256 mismatch") {
		t.Fatalf("error = %v", err)
	}
	if _, err := InstallMarketplace(t.Context(), source, digest(manifest), "other", "1.0.0", root, false, nil, io.Discard); err == nil || !strings.Contains(err.Error(), "namespace") {
		t.Fatalf("namespace error = %v", err)
	}
	if _, err := InstallMarketplace(t.Context(), source, digest(manifest), "acme", "2.0.0", root, false, nil, io.Discard); err == nil || !strings.Contains(err.Error(), "version") {
		t.Fatalf("version error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "acme")); !os.IsNotExist(err) {
		t.Fatalf("mismatched manifest installed plugin: %v", err)
	}
}
