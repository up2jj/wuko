package plugin

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// releaseCovEntry is one tar member written by releaseCovArchive.
type releaseCovEntry struct {
	name     string
	mode     int64
	typeflag byte
	body     []byte
	linkname string
}

// releaseCovArchive builds a gzip-compressed tar holding entries in order, so a test can
// describe archives with several, duplicate, or non-regular members.
func releaseCovArchive(t *testing.T, entries ...releaseCovEntry) []byte {
	t.Helper()
	var output bytes.Buffer
	gzipWriter := gzip.NewWriter(&output)
	writer := tar.NewWriter(gzipWriter)
	for _, entry := range entries {
		typeflag := entry.typeflag
		if typeflag == 0 {
			typeflag = tar.TypeReg
		}
		header := &tar.Header{Name: entry.name, Mode: entry.mode, Typeflag: typeflag, Linkname: entry.linkname}
		if typeflag == tar.TypeReg {
			header.Size = int64(len(entry.body))
		}
		if err := writer.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if typeflag == tar.TypeReg {
			if _, err := writer.Write(entry.body); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func releaseCovArtifact(data []byte) Artifact {
	return Artifact{OS: "linux", Arch: "amd64", Path: "plugin.tar.gz", Format: "tar.gz", Entry: "wuko-plugin-acme", SHA256: digest(data)}
}

func TestValidateArtifactAcceptsExecutableEntry(t *testing.T) {
	t.Parallel()
	tests := map[string][]releaseCovEntry{
		"entry only": {{name: "wuko-plugin-acme", mode: 0o755, body: []byte("binary")}},
		"entry with supporting files": {
			{name: "README.md", mode: 0o644, body: []byte("docs")},
			{name: "share/data.json", mode: 0o600, body: []byte("{}")},
			{name: "wuko-plugin-acme", mode: 0o700, body: []byte("binary")},
		},
	}
	for name, entries := range tests {
		t.Run(name, func(t *testing.T) {
			data := releaseCovArchive(t, entries...)
			if err := ValidateArtifact(releaseCovArtifact(data), data); err != nil {
				t.Fatalf("ValidateArtifact() error = %v", err)
			}
		})
	}
}

func TestValidateArtifactRejectsDigestMismatch(t *testing.T) {
	t.Parallel()
	data := releaseCovArchive(t, releaseCovEntry{name: "wuko-plugin-acme", mode: 0o755, body: []byte("binary")})
	tests := map[string]string{
		"different digest": strings.Repeat("a", 64),
		"uppercase digest": strings.ToUpper(digest(data)),
		"empty digest":     "",
	}
	for name, expected := range tests {
		t.Run(name, func(t *testing.T) {
			artifact := releaseCovArtifact(data)
			artifact.SHA256 = expected
			err := ValidateArtifact(artifact, data)
			if err == nil || !strings.Contains(err.Error(), "sha256 mismatch") {
				t.Fatalf("ValidateArtifact() error = %v, want sha256 mismatch", err)
			}
		})
	}
}

func TestValidateArtifactRejectsUnsafeArchives(t *testing.T) {
	t.Parallel()
	entry := releaseCovEntry{name: "wuko-plugin-acme", mode: 0o755, body: []byte("binary")}
	tests := []struct {
		name    string
		entries []releaseCovEntry
		raw     []byte
		want    string
	}{
		{name: "parent traversal", entries: []releaseCovEntry{entry, {name: "../escape", mode: 0o644, body: []byte("x")}}, want: "unsafe plugin path"},
		{name: "nested traversal", entries: []releaseCovEntry{entry, {name: "share/../../escape", mode: 0o644, body: []byte("x")}}, want: "unsafe plugin path"},
		{name: "absolute path", entries: []releaseCovEntry{entry, {name: "/tmp/escape", mode: 0o644, body: []byte("x")}}, want: "unsafe plugin path"},
		{name: "unclean path", entries: []releaseCovEntry{{name: "./wuko-plugin-acme", mode: 0o755, body: []byte("binary")}}, want: "unsafe plugin path"},
		{name: "backslash path", entries: []releaseCovEntry{entry, {name: `share\escape`, mode: 0o644, body: []byte("x")}}, want: "unsafe plugin path"},
		{name: "installation marker", entries: []releaseCovEntry{entry, {name: MarkerName, mode: 0o644, body: []byte("{}")}}, want: "reserved installation metadata"},
		{name: "release directory", entries: []releaseCovEntry{entry, {name: releaseDirectory + "/manifest", mode: 0o644, body: []byte("{}")}}, want: "reserved installation metadata"},
		{name: "duplicate entry", entries: []releaseCovEntry{entry, entry}, want: "duplicate plugin archive entry"},
		{name: "symlink entry", entries: []releaseCovEntry{{name: "wuko-plugin-acme", mode: 0o755, typeflag: tar.TypeSymlink, linkname: "/bin/sh"}}, want: "not a regular file"},
		{name: "hardlink entry", entries: []releaseCovEntry{entry, {name: "copy", mode: 0o755, typeflag: tar.TypeLink, linkname: "wuko-plugin-acme"}}, want: "not a regular file"},
		{name: "directory entry", entries: []releaseCovEntry{{name: "share", mode: 0o755, typeflag: tar.TypeDir}, entry}, want: "not a regular file"},
		{name: "entry not executable", entries: []releaseCovEntry{{name: "wuko-plugin-acme", mode: 0o644, body: []byte("binary")}}, want: "not executable"},
		{name: "entry missing", entries: []releaseCovEntry{{name: "wuko-plugin-other", mode: 0o755, body: []byte("binary")}}, want: "entry is missing"},
		{name: "empty archive", entries: nil, want: "entry is missing"},
		{name: "not gzip", raw: []byte("plain bytes"), want: "opening plugin archive"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			data := test.raw
			if data == nil {
				data = releaseCovArchive(t, test.entries...)
			}
			err := ValidateArtifact(releaseCovArtifact(data), data)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ValidateArtifact() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestValidateArtifactRejectsTooManyEntries(t *testing.T) {
	t.Parallel()
	entries := make([]releaseCovEntry, 0, maxArchiveEntries+1)
	entries = append(entries, releaseCovEntry{name: "wuko-plugin-acme", mode: 0o755, body: []byte("binary")})
	for index := range maxArchiveEntries {
		entries = append(entries, releaseCovEntry{name: "files/" + strconv.Itoa(index), mode: 0o644})
	}
	data := releaseCovArchive(t, entries...)
	err := ValidateArtifact(releaseCovArtifact(data), data)
	if err == nil || !strings.Contains(err.Error(), "too many entries") {
		t.Fatalf("ValidateArtifact() error = %v, want too many entries", err)
	}
}

// releaseCovBundle describes a two-platform release whose files a test server serves.
func releaseCovBundle(t *testing.T) (manifest []byte, files map[string][]byte) {
	t.Helper()
	linux := releaseCovArchive(t, releaseCovEntry{name: "wuko-plugin-acme", mode: 0o755, body: []byte("linux")})
	darwin := releaseCovArchive(t, releaseCovEntry{name: "wuko-plugin-acme", mode: 0o755, body: []byte("darwin")})
	manifest, err := json.Marshal(Manifest{
		Version: 1, Namespace: "acme", PluginVersion: "1.0.0", Protocol: Protocol,
		Artifacts: []Artifact{
			{OS: "linux", Arch: "amd64", Path: "dist/linux-amd64.tar.gz", Format: "tar.gz", Entry: "wuko-plugin-acme", SHA256: digest(linux)},
			{OS: "darwin", Arch: "arm64", Path: "dist/darwin-arm64.tar.gz", Format: "tar.gz", Entry: "wuko-plugin-acme", SHA256: digest(darwin)},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return manifest, map[string][]byte{
		"/releases/acme/plugin.json":              manifest,
		"/releases/acme/dist/linux-amd64.tar.gz":  linux,
		"/releases/acme/dist/darwin-arm64.tar.gz": darwin,
	}
}

func releaseCovServer(t *testing.T, files map[string][]byte) *httptest.Server {
	t.Helper()
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, ok := files[request.URL.Path]
		if !ok {
			http.NotFound(writer, request)
			return
		}
		_, _ = writer.Write(body)
	}))
	t.Cleanup(server.Close)
	return server
}

func TestFetchBundleDownloadsEveryPlatformArtifact(t *testing.T) {
	t.Parallel()
	manifest, files := releaseCovBundle(t)
	server := releaseCovServer(t, files)
	bundle, err := FetchBundle(t.Context(), server.URL+"/releases/acme/", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if bundle.CanonicalSource != server.URL+"/releases/acme/plugin.json" {
		t.Fatalf("canonical source = %q", bundle.CanonicalSource)
	}
	if !bytes.Equal(bundle.ManifestData, manifest) || bundle.ManifestDigest != digest(manifest) || bundle.Manifest.Namespace != "acme" {
		t.Fatalf("manifest = %#v digest %q", bundle.Manifest, bundle.ManifestDigest)
	}
	if len(bundle.ArtifactData) != 2 {
		t.Fatalf("artifact count = %d", len(bundle.ArtifactData))
	}
	for _, artifact := range bundle.Manifest.Artifacts {
		if !bytes.Equal(bundle.ArtifactData[artifact.Path], files["/releases/acme/"+artifact.Path]) {
			t.Fatalf("artifact %s data differs from the served archive", artifact.Path)
		}
	}
}

func TestFetchBundleReadsLocalRelease(t *testing.T) {
	t.Parallel()
	_, files := releaseCovBundle(t)
	directory := t.TempDir()
	for name, data := range files {
		target := filepath.Join(directory, filepath.FromSlash(strings.TrimPrefix(name, "/releases/acme/")))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	bundle, err := FetchBundle(t.Context(), directory, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(bundle.ArtifactData) != 2 || bundle.CanonicalSource != filepath.Join(directory, "plugin.json") {
		t.Fatalf("bundle = %d artifacts from %q", len(bundle.ArtifactData), bundle.CanonicalSource)
	}
}

func TestFetchBundleRejectsBadReleases(t *testing.T) {
	t.Parallel()
	manifest, files := releaseCovBundle(t)
	tamperedArchive := releaseCovArchive(t, releaseCovEntry{name: "wuko-plugin-acme", mode: 0o755, body: []byte("tampered")})
	unsafeArchive := releaseCovArchive(t, releaseCovEntry{name: "../wuko-plugin-acme", mode: 0o755, body: []byte("x")})
	var unsafeManifest Manifest
	if err := json.Unmarshal(manifest, &unsafeManifest); err != nil {
		t.Fatal(err)
	}
	unsafeManifest.Artifacts[0].SHA256 = digest(unsafeArchive)
	unsafeManifestData, err := json.Marshal(unsafeManifest)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		mutate func(map[string][]byte)
		want   string
	}{
		{name: "manifest not found", mutate: func(files map[string][]byte) { delete(files, "/releases/acme/plugin.json") }, want: "404"},
		{name: "invalid manifest", mutate: func(files map[string][]byte) { files["/releases/acme/plugin.json"] = []byte(`{"version":2}`) }, want: "unsupported plugin manifest"},
		{name: "artifact not found", mutate: func(files map[string][]byte) { delete(files, "/releases/acme/dist/darwin-arm64.tar.gz") }, want: "fetching plugin artifact darwin/arm64"},
		{name: "artifact digest mismatch", mutate: func(files map[string][]byte) { files["/releases/acme/dist/linux-amd64.tar.gz"] = tamperedArchive }, want: "sha256 mismatch"},
		{name: "artifact with traversal entry", mutate: func(files map[string][]byte) {
			files["/releases/acme/plugin.json"] = unsafeManifestData
			files["/releases/acme/dist/linux-amd64.tar.gz"] = unsafeArchive
		}, want: "validating plugin artifact linux/amd64"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			served := make(map[string][]byte, len(files))
			for name, data := range files {
				served[name] = data
			}
			test.mutate(served)
			server := releaseCovServer(t, served)
			_, err := FetchBundle(t.Context(), server.URL+"/releases/acme/plugin.json", server.Client())
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("FetchBundle() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestFetchBundleRejectsNonHTTPSSources(t *testing.T) {
	t.Parallel()
	for _, source := range []string{"http://plugins.test/plugin.json", "ftp://plugins.test/plugin.json"} {
		t.Run(source, func(t *testing.T) {
			_, err := FetchBundle(t.Context(), source, nil)
			if err == nil || !strings.Contains(err.Error(), "local, HTTPS, or github") {
				t.Fatalf("FetchBundle() error = %v", err)
			}
		})
	}
}

func TestFetchBundleRejectsRedirectToAnotherOrigin(t *testing.T) {
	t.Parallel()
	_, files := releaseCovBundle(t)
	other := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = writer.Write(files[request.URL.Path])
	}))
	t.Cleanup(other.Close)
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, other.URL+request.URL.Path, http.StatusFound)
	}))
	t.Cleanup(server.Close)
	_, err := FetchBundle(t.Context(), server.URL+"/releases/acme/plugin.json", server.Client())
	if err == nil || !strings.Contains(err.Error(), "left original origin") {
		t.Fatalf("FetchBundle() error = %v", err)
	}
}

func TestFetchBundleRejectsOversizedDownload(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("streams more than the archive byte limit")
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = io.CopyN(writer, releaseCovZeros{}, maxArchiveBytes+1)
	}))
	t.Cleanup(server.Close)
	_, err := FetchBundle(t.Context(), server.URL+"/plugin.json", server.Client())
	if err == nil || !strings.Contains(err.Error(), "exceeds byte limit") {
		t.Fatalf("FetchBundle() error = %v", err)
	}
}

type releaseCovZeros struct{}

func (releaseCovZeros) Read(buffer []byte) (int, error) {
	clear(buffer)
	return len(buffer), nil
}
