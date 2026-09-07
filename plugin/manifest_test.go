package plugin

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestParseManifestSelectsCurrentArtifact(t *testing.T) {
	artifactDigest := strings.Repeat("a", 64)
	data, err := json.Marshal(Manifest{Version: 1, Namespace: "acme", PluginVersion: "1.0.0", Protocol: Protocol, Artifacts: []Artifact{{OS: runtime.GOOS, Arch: runtime.GOARCH, Path: "dist/plugin.tar.gz", Format: "tar.gz", Entry: "wuko-plugin-acme", SHA256: artifactDigest}}})
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := ParseManifest(data)
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := manifest.CurrentArtifact()
	if err != nil {
		t.Fatal(err)
	}
	if artifact.Entry != "wuko-plugin-acme" {
		t.Fatalf("entry = %q", artifact.Entry)
	}
}

func TestParseManifestRejectsDuplicatePlatform(t *testing.T) {
	artifact := Artifact{OS: "linux", Arch: "arm64", Path: "plugin.tar.gz", Format: "tar.gz", Entry: "wuko-plugin-acme", SHA256: strings.Repeat("a", 64)}
	data, _ := json.Marshal(Manifest{Version: 1, Namespace: "acme", PluginVersion: "1", Protocol: Protocol, Artifacts: []Artifact{artifact, artifact}})
	if _, err := ParseManifest(data); err == nil {
		t.Fatal("expected duplicate platform error")
	}
}

func TestExtractRejectsTraversalAndAcceptsExecutable(t *testing.T) {
	archive := makeArchive(t, "wuko-plugin-acme", 0755, []byte("binary"))
	release := Release{Artifact: Artifact{Entry: "wuko-plugin-acme"}, ArtifactData: archive}
	entry, err := Extract(release, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(entry) != "wuko-plugin-acme" {
		t.Fatal(entry)
	}
	bad := Release{Artifact: Artifact{Entry: "wuko-plugin-acme"}, ArtifactData: makeArchive(t, "../wuko-plugin-acme", 0755, []byte("bad"))}
	if _, err := Extract(bad, t.TempDir()); err == nil {
		t.Fatal("expected traversal error")
	}
}

func makeArchive(t *testing.T, name string, mode int64, data []byte) []byte {
	t.Helper()
	var output bytes.Buffer
	gzipWriter := gzip.NewWriter(&output)
	writer := tar.NewWriter(gzipWriter)
	if err := writer.WriteHeader(&tar.Header{Name: name, Mode: mode, Size: int64(len(data)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func TestPluginCandidateDoesNotSkipInvalidHigherPrecedence(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "wuko-plugin-acme"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, found, err := pluginCandidate(directory, "wuko-plugin-acme"); !found || err == nil {
		t.Fatalf("found=%v err=%v", found, err)
	}
}

func TestParseManifestRejectsPaddedPluginVersionAndEmptyArtifacts(t *testing.T) {
	// A padded version parses but fails marketplace manifest validation, which would let
	// `wuko marketplace build` publish a manifest.json it can no longer read back.
	padded := []byte(`{"version":1,"namespace":"acme","plugin_version":" 1.0.0 ","protocol":"wuko.plugin/v1","artifacts":[{"os":"linux","arch":"amd64","path":"a.tar.gz","format":"tar.gz","entry":"wuko-plugin-acme","sha256":"` + strings.Repeat("a", 64) + `"}]}`)
	if _, err := ParseManifest(padded); err == nil || !strings.Contains(err.Error(), "namespace or version") {
		t.Fatalf("padded plugin_version error = %v", err)
	}
	empty := []byte(`{"version":1,"namespace":"acme","plugin_version":"1.0.0","protocol":"wuko.plugin/v1","artifacts":[]}`)
	if _, err := ParseManifest(empty); err == nil || !strings.Contains(err.Error(), "no artifacts") {
		t.Fatalf("empty artifacts error = %v", err)
	}
}
