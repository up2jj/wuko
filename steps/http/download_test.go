package http

import (
	"context"
	"errors"
	"fmt"
	"io"
	nethttp "net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/up2jj/wuko/step"
)

func TestLargeResponseStreamsToDownload(t *testing.T) {
	const downloadSize = int64(12 << 20)
	chunk := make([]byte, 64<<10)
	chunk[0] = 'w'
	chunk[len(chunk)-1] = 'o'
	server := httptest.NewServer(nethttp.HandlerFunc(func(writer nethttp.ResponseWriter, _ *nethttp.Request) {
		writer.Header().Set("Content-Length", fmt.Sprint(downloadSize))
		for remaining := downloadSize; remaining > 0; remaining -= int64(len(chunk)) {
			if _, err := writer.Write(chunk); err != nil {
				return
			}
		}
	}))
	defer server.Close()

	runDir := t.TempDir()
	runner, err := New(map[string]any{
		"url":      server.URL,
		"download": map[string]any{"path": "downloads/release.bin"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(runDir, "downloads"), 0o755); err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(t.Context(), step.Request{RunDir: runDir})
	if err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(runDir, "downloads", "release.bin")
	if result.Outputs["path"] != destination || result.Outputs["size"] != downloadSize || result.Outputs["status"] != nethttp.StatusOK {
		t.Fatalf("outputs = %#v", result.Outputs)
	}
	if _, exists := result.Outputs["body"]; exists {
		t.Fatalf("download unexpectedly buffered a body: %#v", result.Outputs)
	}
	info, err := os.Stat(destination)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != downloadSize {
		t.Fatalf("download size = %d, want %d", info.Size(), downloadSize)
	}
	file, err := os.Open(destination)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	first := make([]byte, 1)
	if _, err := file.Read(first); err != nil || first[0] != 'w' {
		t.Fatalf("first byte = %q, error = %v", first, err)
	}
	if _, err := file.Seek(-1, io.SeekEnd); err != nil {
		t.Fatal(err)
	}
	last := make([]byte, 1)
	if _, err := file.Read(last); err != nil || last[0] != 'o' {
		t.Fatalf("last byte = %q, error = %v", last, err)
	}
}

func TestDownloadOverwritePolicy(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(nethttp.HandlerFunc(func(writer nethttp.ResponseWriter, _ *nethttp.Request) {
		requests.Add(1)
		fmt.Fprint(writer, "replacement")
	}))
	defer server.Close()

	destination := filepath.Join(t.TempDir(), "artifact.txt")
	if err := os.WriteFile(destination, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	runner, err := New(map[string]any{
		"url": server.URL, "download": map[string]any{"path": destination},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(t.Context(), step.Request{}); err == nil || !strings.Contains(err.Error(), "overwrite") {
		t.Fatalf("error = %v", err)
	}
	if requests.Load() != 0 {
		t.Fatalf("server received %d requests before destination validation", requests.Load())
	}

	runner, err = New(map[string]any{
		"url": server.URL, "download": map[string]any{"path": destination, "overwrite": true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(t.Context(), step.Request{}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "replacement" || requests.Load() != 1 {
		t.Fatalf("content = %q, requests = %d", data, requests.Load())
	}
	if info, err := os.Stat(destination); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("destination info = %#v, error = %v", info, err)
	}
}

func TestFailedResponseDoesNotInstallDownload(t *testing.T) {
	server := httptest.NewServer(nethttp.HandlerFunc(func(writer nethttp.ResponseWriter, _ *nethttp.Request) {
		writer.WriteHeader(nethttp.StatusServiceUnavailable)
		fmt.Fprint(writer, "retry later")
	}))
	defer server.Close()

	dir := t.TempDir()
	runner, err := New(map[string]any{
		"url": server.URL, "download": map[string]any{"path": "artifact.bin"},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(t.Context(), step.Request{RunDir: dir})
	if err == nil || result.Outputs["status"] != nethttp.StatusServiceUnavailable || result.Outputs["body"] != "retry later" {
		t.Fatalf("result = %#v, error = %v", result, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "artifact.bin")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("download destination exists or stat failed unexpectedly: %v", err)
	}
	assertNoDownloadTemporaryFiles(t, dir)
}

func TestPartialDownloadIsRemoved(t *testing.T) {
	dir := t.TempDir()
	target, err := prepareDownload(t.Context(), dir, DownloadConfig{Path: "artifact.bin"})
	if err != nil {
		t.Fatal(err)
	}
	reader := io.MultiReader(strings.NewReader("partial"), errorReader{err: io.ErrUnexpectedEOF})
	if _, err := target.Write(t.Context(), reader); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("error = %v, want unexpected EOF", err)
	}
	if err := target.Cleanup(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "artifact.bin")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("download destination exists or stat failed unexpectedly: %v", err)
	}
	assertNoDownloadTemporaryFiles(t, dir)
}

func TestCanceledDownloadIsRemoved(t *testing.T) {
	dir := t.TempDir()
	target, err := prepareDownload(t.Context(), dir, DownloadConfig{Path: "artifact.bin"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := target.Write(ctx, strings.NewReader("content")); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context canceled", err)
	}
	if err := target.Cleanup(); err != nil {
		t.Fatal(err)
	}
	assertNoDownloadTemporaryFiles(t, dir)
}

func TestRejectsInvalidDownloadConfiguration(t *testing.T) {
	tests := []struct {
		name string
		raw  map[string]any
		want string
	}{
		{name: "null", raw: map[string]any{"download": nil}, want: "configure a path"},
		{name: "empty path", raw: map[string]any{"download": map[string]any{}}, want: "path"},
		{name: "response conflict", raw: map[string]any{"response": "text", "download": map[string]any{"path": "file"}}, want: "mutually exclusive"},
		{name: "unknown option", raw: map[string]any{"download": map[string]any{"path": "file", "mode": "0600"}}, want: "field mode"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			test.raw["url"] = "https://example.com"
			_, err := New(test.raw)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want containing %q", err, test.want)
			}
		})
	}
}

type errorReader struct{ err error }

func (reader errorReader) Read([]byte) (int, error) { return 0, reader.err }

func assertNoDownloadTemporaryFiles(t *testing.T, dir string) {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, ".wuko-download-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("temporary downloads remain: %#v", matches)
	}
}
