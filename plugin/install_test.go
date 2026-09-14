package plugin

import (
	"context"
	"encoding/json"
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
	if marker.Protocol != ProtocolV1 {
		t.Fatalf("marker protocol = %q", marker.Protocol)
	}
	directory := filepath.Join(root, "acme")
	// A v1 marker leaves the field out so an older wuko, which rejects unknown marker fields,
	// can still read installations written by this build.
	stored, err := os.ReadFile(filepath.Join(directory, MarkerName))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(stored), "protocol") {
		t.Fatalf("v1 marker records a protocol: %s", stored)
	}
	if _, err := ValidateInstallation(directory, "acme"); err != nil {
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
