package plugin

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestHTTPSPluginSourceUsesQueryEphemerally(t *testing.T) {
	script := []byte("#!/bin/sh\nread line\nprintf '{\"id\":\"1\",\"result\":{\"protocol\":\"" + Protocol + "\",\"namespace\":\"acme\",\"steps\":[],\"executors\":[],\"helpers\":[]}}\\n'\nread line\nprintf '{\"id\":\"2\",\"result\":{}}\\n'\n")
	archive := makeArchive(t, "wuko-plugin-acme", 0o755, script)
	manifestData, err := json.Marshal(Manifest{
		Version: 1, Namespace: "acme", PluginVersion: "1.0.0", Protocol: Protocol,
		Artifacts: []Artifact{{OS: runtime.GOOS, Arch: runtime.GOARCH, Path: "plugin.tar.gz", Format: "tar.gz", Entry: "wuko-plugin-acme", SHA256: digest(archive)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
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
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return httpResponse(request, http.StatusForbidden, []byte("denied")), nil
	})}
	_, err := FetchRelease(t.Context(), "https://plugins.test/acme?token=secret-value", "", client)
	if err == nil || strings.Contains(err.Error(), "secret-value") || strings.Contains(err.Error(), "token=") {
		t.Fatalf("error = %v", err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (roundTrip roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTrip(request)
}

func httpResponse(request *http.Request, status int, body []byte) *http.Response {
	return &http.Response{
		StatusCode: status, Status: http.StatusText(status), Request: request,
		Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(body)),
	}
}
