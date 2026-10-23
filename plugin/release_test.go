package plugin

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/up2jj/wuko/internal/archivetest"
	"github.com/up2jj/wuko/internal/transporttest"
)

func TestHTTPSPluginSourceUsesQueryEphemerally(t *testing.T) {
	t.Parallel()
	script := []byte("#!/bin/sh\nread line\nprintf '{\"id\":\"1\",\"result\":{\"protocol\":\"" + Protocol + "\",\"namespace\":\"acme\",\"steps\":[],\"executors\":[],\"helpers\":[]}}\\n'\nread line\nprintf '{\"id\":\"2\",\"result\":{}}\\n'\n")
	archive := archivetest.TarGz(t, archivetest.Entry{Name: "wuko-plugin-acme", Mode: 0o755, Data: script})
	manifestData, err := json.Marshal(Manifest{
		Version: 1, Namespace: "acme", PluginVersion: "1.0.0", Protocol: Protocol,
		Artifacts: []Artifact{{OS: runtime.GOOS, Arch: runtime.GOARCH, Path: "plugin.tar.gz", Format: "tar.gz", Entry: "wuko-plugin-acme", SHA256: digest(archive)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	client := &http.Client{Transport: transporttest.RoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Query().Get("token") != "secret-value" {
			t.Errorf("request %s omitted query credential", request.URL.Path)
		}
		paths = append(paths, request.URL.Path)
		body := manifestData
		if strings.HasSuffix(request.URL.Path, "plugin.tar.gz") {
			body = archive
		}
		return httpResponse(request, http.StatusOK, body), nil
	})}
	release, err := FetchRelease(t.Context(), "https://plugins.test/releases/acme?token=secret-value", digest(manifestData), client)
	if err != nil {
		t.Fatal(err)
	}
	if release.CanonicalSource != "https://plugins.test/releases/acme/plugin.json" || strings.Contains(release.CanonicalSource, "secret-value") {
		t.Fatalf("canonical source = %q", release.CanonicalSource)
	}
	if len(paths) != 2 || paths[0] != "/releases/acme/plugin.json" || paths[1] != "/releases/acme/plugin.tar.gz" {
		t.Fatalf("requested paths = %#v", paths)
	}
	root := t.TempDir()
	markers, err := installReleases(t.Context(), []Release{release}, root, false, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	markerData, err := os.ReadFile(filepath.Join(root, "acme", MarkerName))
	if err != nil {
		t.Fatal(err)
	}
	if markers[0].Source != release.CanonicalSource || strings.Contains(string(markerData), "secret-value") || strings.Contains(string(markerData), "token=") {
		t.Fatalf("stored marker leaked source credentials: %s", markerData)
	}
}

func TestHTTPSPluginDownloadErrorsRedactQuery(t *testing.T) {
	t.Parallel()
	client := &http.Client{Transport: transporttest.RoundTripFunc(func(request *http.Request) (*http.Response, error) {
		return httpResponse(request, http.StatusForbidden, []byte("denied")), nil
	})}
	_, err := FetchRelease(t.Context(), "https://plugins.test/acme?token=secret-value", "", client)
	if err == nil || strings.Contains(err.Error(), "secret-value") || strings.Contains(err.Error(), "token=") {
		t.Fatalf("error = %v", err)
	}
}

func httpResponse(request *http.Request, status int, body []byte) *http.Response {
	response := transporttest.Response(status, body)
	response.Request = request
	return response
}
