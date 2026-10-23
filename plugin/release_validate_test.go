package plugin

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/up2jj/wuko/internal/archivetest"
)

func releaseCovArtifact(data []byte) Artifact {
	return Artifact{OS: "linux", Arch: "amd64", Path: "plugin.tar.gz", Format: "tar.gz", Entry: "wuko-plugin-acme", SHA256: digest(data)}
}

func TestValidateArtifactAcceptsExecutableEntry(t *testing.T) {
	t.Parallel()
	tests := map[string][]archivetest.Entry{
		"entry only": {{Name: "wuko-plugin-acme", Mode: 0o755, Data: []byte("binary")}},
		"entry with supporting files": {
			{Name: "README.md", Mode: 0o644, Data: []byte("docs")},
			{Name: "share/data.json", Mode: 0o600, Data: []byte("{}")},
			{Name: "wuko-plugin-acme", Mode: 0o700, Data: []byte("binary")},
		},
	}
	for name, entries := range tests {
		t.Run(name, func(t *testing.T) {
			data := archivetest.TarGz(t, entries...)
			if err := ValidateArtifact(releaseCovArtifact(data), data); err != nil {
				t.Fatalf("ValidateArtifact() error = %v", err)
			}
		})
	}
}

func TestValidateArtifactRejectsDigestMismatch(t *testing.T) {
	t.Parallel()
	data := archivetest.TarGz(t, archivetest.Entry{Name: "wuko-plugin-acme", Mode: 0o755, Data: []byte("binary")})
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
	entry := archivetest.Entry{Name: "wuko-plugin-acme", Mode: 0o755, Data: []byte("binary")}
	tests := []struct {
		name    string
		entries []archivetest.Entry
		raw     []byte
		want    string
	}{
		{name: "parent traversal", entries: []archivetest.Entry{entry, {Name: "../escape", Mode: 0o644, Data: []byte("x")}}, want: "unsafe plugin path"},
		{name: "nested traversal", entries: []archivetest.Entry{entry, {Name: "share/../../escape", Mode: 0o644, Data: []byte("x")}}, want: "unsafe plugin path"},
		{name: "absolute path", entries: []archivetest.Entry{entry, {Name: "/tmp/escape", Mode: 0o644, Data: []byte("x")}}, want: "unsafe plugin path"},
		{name: "unclean path", entries: []archivetest.Entry{{Name: "./wuko-plugin-acme", Mode: 0o755, Data: []byte("binary")}}, want: "unsafe plugin path"},
		{name: "backslash path", entries: []archivetest.Entry{entry, {Name: `share\escape`, Mode: 0o644, Data: []byte("x")}}, want: "unsafe plugin path"},
		{name: "installation marker", entries: []archivetest.Entry{entry, {Name: MarkerName, Mode: 0o644, Data: []byte("{}")}}, want: "reserved installation metadata"},
		{name: "release directory", entries: []archivetest.Entry{entry, {Name: releaseDirectory + "/manifest", Mode: 0o644, Data: []byte("{}")}}, want: "reserved installation metadata"},
		{name: "duplicate entry", entries: []archivetest.Entry{entry, entry}, want: "duplicate plugin archive entry"},
		{name: "symlink entry", entries: []archivetest.Entry{{Name: "wuko-plugin-acme", Mode: 0o755, Type: tar.TypeSymlink, Link: "/bin/sh"}}, want: "not a regular file"},
		{name: "hardlink entry", entries: []archivetest.Entry{entry, {Name: "copy", Mode: 0o755, Type: tar.TypeLink, Link: "wuko-plugin-acme"}}, want: "not a regular file"},
		{name: "directory entry", entries: []archivetest.Entry{{Name: "share", Mode: 0o755, Type: tar.TypeDir}, entry}, want: "not a regular file"},
		{name: "entry not executable", entries: []archivetest.Entry{{Name: "wuko-plugin-acme", Mode: 0o644, Data: []byte("binary")}}, want: "not executable"},
		{name: "entry missing", entries: []archivetest.Entry{{Name: "wuko-plugin-other", Mode: 0o755, Data: []byte("binary")}}, want: "entry is missing"},
		{name: "empty archive", entries: nil, want: "entry is missing"},
		{name: "not gzip", raw: []byte("plain bytes"), want: "opening plugin archive"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			data := test.raw
			if data == nil {
				data = archivetest.TarGz(t, test.entries...)
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
	entries := make([]archivetest.Entry, 0, maxArchiveEntries+1)
	entries = append(entries, archivetest.Entry{Name: "wuko-plugin-acme", Mode: 0o755, Data: []byte("binary")})
	for index := range maxArchiveEntries {
		entries = append(entries, archivetest.Entry{Name: "files/" + strconv.Itoa(index), Mode: 0o644})
	}
	data := archivetest.TarGz(t, entries...)
	err := ValidateArtifact(releaseCovArtifact(data), data)
	if err == nil || !strings.Contains(err.Error(), "too many entries") {
		t.Fatalf("ValidateArtifact() error = %v, want too many entries", err)
	}
}

// releaseCovBundle describes a two-platform release whose files a test server serves.
func releaseCovBundle(t *testing.T) (manifest []byte, files map[string][]byte) {
	t.Helper()
	linux := archivetest.TarGz(t, archivetest.Entry{Name: "wuko-plugin-acme", Mode: 0o755, Data: []byte("linux")})
	darwin := archivetest.TarGz(t, archivetest.Entry{Name: "wuko-plugin-acme", Mode: 0o755, Data: []byte("darwin")})
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
	tamperedArchive := archivetest.TarGz(t, archivetest.Entry{Name: "wuko-plugin-acme", Mode: 0o755, Data: []byte("tampered")})
	unsafeArchive := archivetest.TarGz(t, archivetest.Entry{Name: "../wuko-plugin-acme", Mode: 0o755, Data: []byte("x")})
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
