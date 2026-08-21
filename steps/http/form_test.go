package http

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	nethttp "net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/up2jj/wuko/step"
)

func TestURLEncodedForm(t *testing.T) {
	server := httptest.NewServer(nethttp.HandlerFunc(func(writer nethttp.ResponseWriter, request *nethttp.Request) {
		if request.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
			t.Errorf("Content-Type = %q", request.Header.Get("Content-Type"))
		}
		data, err := io.ReadAll(request.Body)
		if err != nil {
			t.Error(err)
		}
		if got, want := string(data), "channel=stable&title=release+candidate"; got != want {
			t.Errorf("body = %q, want %q", got, want)
		}
		writer.WriteHeader(nethttp.StatusNoContent)
	}))
	defer server.Close()

	runner, err := New(map[string]any{
		"url": server.URL, "method": "post",
		"form": map[string]any{"title": "release candidate", "channel": "stable"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(t.Context(), step.Request{}); err != nil {
		t.Fatal(err)
	}
}

func TestMultipartFormAndFiles(t *testing.T) {
	workflowDir := t.TempDir()
	textPath := filepath.Join(workflowDir, "release notes.txt")
	if err := os.WriteFile(textPath, []byte("release notes"), 0o600); err != nil {
		t.Fatal(err)
	}
	absoluteDir := t.TempDir()
	binaryPath := filepath.Join(absoluteDir, "artifact.unknown-extension")
	if err := os.WriteFile(binaryPath, []byte{0, 1, 2, 3}, 0o600); err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(nethttp.HandlerFunc(func(writer nethttp.ResponseWriter, request *nethttp.Request) {
		data, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatal(err)
		}
		if request.ContentLength != int64(len(data)) {
			t.Errorf("ContentLength = %d, body bytes = %d", request.ContentLength, len(data))
		}
		if len(request.TransferEncoding) != 0 {
			t.Errorf("Transfer-Encoding = %#v", request.TransferEncoding)
		}
		mediaType, parameters, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
		if err != nil {
			t.Fatal(err)
		}
		if mediaType != "multipart/form-data" || parameters["boundary"] == "" {
			t.Fatalf("Content-Type = %q", request.Header.Get("Content-Type"))
		}
		parts := readMultipartParts(t, data, parameters["boundary"])
		if got := parts["metadata"][0]; got.filename != "" || string(got.data) != "stable" {
			t.Errorf("metadata part = %#v", got)
		}
		uploads := parts["attachment"]
		if len(uploads) != 2 {
			t.Fatalf("attachment parts = %#v", uploads)
		}
		if uploads[0].filename != "release notes.txt" || !strings.HasPrefix(uploads[0].contentType, "text/plain") || string(uploads[0].data) != "release notes" {
			t.Errorf("text upload = %#v", uploads[0])
		}
		if uploads[1].filename != "artifact.unknown-extension" || uploads[1].contentType != "application/octet-stream" || !bytes.Equal(uploads[1].data, []byte{0, 1, 2, 3}) {
			t.Errorf("binary upload = %#v", uploads[1])
		}
		writer.WriteHeader(nethttp.StatusNoContent)
	}))
	defer server.Close()

	runner, err := New(map[string]any{
		"url": server.URL, "method": "post", "form": map[string]any{"metadata": "stable"},
		"files": []any{
			map[string]any{"field": "attachment", "path": filepath.Base(textPath)},
			map[string]any{"field": "attachment", "path": binaryPath},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(t.Context(), step.Request{WorkflowDir: workflowDir}); err != nil {
		t.Fatal(err)
	}
}

func TestLargeMultipartUploadHasLengthAndStreams(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "large.bin")
	const fileSize = int64(12 << 20)
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(fileSize); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(nethttp.HandlerFunc(func(writer nethttp.ResponseWriter, request *nethttp.Request) {
		if request.ContentLength <= fileSize {
			t.Errorf("ContentLength = %d, want multipart overhead above %d", request.ContentLength, fileSize)
		}
		if len(request.TransferEncoding) != 0 {
			t.Errorf("Transfer-Encoding = %#v", request.TransferEncoding)
		}
		count, err := io.Copy(io.Discard, request.Body)
		if err != nil {
			t.Error(err)
		}
		if count != request.ContentLength {
			t.Errorf("read %d bytes, ContentLength = %d", count, request.ContentLength)
		}
		writer.WriteHeader(nethttp.StatusNoContent)
	}))
	defer server.Close()

	runner, err := New(map[string]any{
		"url": server.URL, "method": "post",
		"files": []any{map[string]any{"field": "payload", "path": path}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(t.Context(), step.Request{}); err != nil {
		t.Fatal(err)
	}
}

func TestMultipartBodyReplaysForRedirect(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "payload.txt")
	if err := os.WriteFile(path, []byte("replayed"), 0o600); err != nil {
		t.Fatal(err)
	}
	requests := 0
	server := httptest.NewServer(nethttp.HandlerFunc(func(writer nethttp.ResponseWriter, request *nethttp.Request) {
		requests++
		if request.URL.Path == "/start" {
			writer.Header().Set("Location", "/finish")
			writer.WriteHeader(nethttp.StatusTemporaryRedirect)
			return
		}
		if request.URL.Path != "/finish" {
			t.Errorf("path = %q", request.URL.Path)
		}
		if err := request.ParseMultipartForm(1 << 20); err != nil {
			t.Fatal(err)
		}
		upload, _, err := request.FormFile("payload")
		if err != nil {
			t.Fatal(err)
		}
		defer upload.Close()
		data, err := io.ReadAll(upload)
		if err != nil || string(data) != "replayed" {
			t.Errorf("upload = %q, error = %v", data, err)
		}
		writer.WriteHeader(nethttp.StatusNoContent)
	}))
	defer server.Close()

	runner, err := New(map[string]any{
		"url": server.URL + "/start", "method": "post",
		"files": []any{map[string]any{"field": "payload", "path": path}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(t.Context(), step.Request{}); err != nil {
		t.Fatal(err)
	}
	if requests != 2 {
		t.Fatalf("requests = %d, want 2", requests)
	}
}

func TestMultipartBodyCancellation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "payload.bin")
	if err := os.WriteFile(path, bytes.Repeat([]byte("x"), 1<<20), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	body, err := prepareMultipartBody(ctx, dir, nil, []FileConfig{{Field: "payload", Path: path}})
	if err != nil {
		t.Fatal(err)
	}
	reader, err := body.Open()
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	_, err = io.Copy(io.Discard, reader)
	reader.Close()
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context canceled", err)
	}
}

func TestMultipartOpenDoesNotRetainAllUploadFiles(t *testing.T) {
	dir := t.TempDir()
	files := make([]FileConfig, 32)
	for i := range files {
		name := fmt.Sprintf("upload-%02d.txt", i)
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
		files[i] = FileConfig{Field: "attachment", Path: name}
	}
	body, err := prepareMultipartBody(t.Context(), dir, nil, files)
	if err != nil {
		t.Fatal(err)
	}
	before := countOpenFileDescriptors(t)
	reader, err := body.Open()
	if err != nil {
		t.Fatal(err)
	}
	afterOpen := countOpenFileDescriptors(t)
	if afterOpen > before+4 {
		reader.Close()
		t.Fatalf("Open retained %d additional file descriptors for %d uploads", afterOpen-before, len(files))
	}
	if _, err := io.Copy(io.Discard, reader); err != nil {
		reader.Close()
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	afterRead := countOpenFileDescriptors(t)
	if afterRead > before+2 {
		t.Fatalf("multipart stream leaked file descriptors: before=%d after=%d", before, afterRead)
	}
}

func TestRejectsInvalidFormAndFileConfiguration(t *testing.T) {
	tests := []struct {
		name string
		raw  map[string]any
		want string
	}{
		{name: "body and form", raw: map[string]any{"body": "raw", "form": map[string]any{"a": "b"}}, want: "mutually exclusive"},
		{name: "json and files", raw: map[string]any{"json": map[string]any{}, "files": []any{map[string]any{"field": "f", "path": "a"}}}, want: "mutually exclusive"},
		{name: "empty files", raw: map[string]any{"files": []any{}}, want: "at least one"},
		{name: "missing field", raw: map[string]any{"files": []any{map[string]any{"path": "a"}}}, want: "field"},
		{name: "missing path", raw: map[string]any{"files": []any{map[string]any{"field": "f"}}}, want: "path"},
		{name: "content type", raw: map[string]any{"headers": map[string]any{"content-type": "multipart/form-data"}, "files": []any{map[string]any{"field": "f", "path": "a"}}}, want: "Content-Type"},
		{name: "file field newline", raw: map[string]any{"files": []any{map[string]any{"field": "bad\nfield", "path": "a"}}}, want: "newlines"},
		{name: "form field newline", raw: map[string]any{"form": map[string]any{"bad\nfield": "value"}, "files": []any{map[string]any{"field": "f", "path": "a"}}}, want: "newlines"},
		{name: "unknown file option", raw: map[string]any{"files": []any{map[string]any{"field": "f", "path": "a", "filename": "override"}}}, want: "field filename"},
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

func TestRejectsInvalidUploadPaths(t *testing.T) {
	dir := t.TempDir()
	tests := []struct {
		name string
		path string
		want string
	}{
		{name: "missing", path: "missing.txt", want: "inspecting upload file"},
		{name: "directory", path: ".", want: "not a regular file"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner, err := New(map[string]any{
				"url":   "http://127.0.0.1:1",
				"files": []any{map[string]any{"field": "upload", "path": test.path}},
			})
			if err != nil {
				t.Fatal(err)
			}
			_, err = runner.Run(t.Context(), step.Request{WorkflowDir: dir})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want containing %q", err, test.want)
			}
		})
	}
}

type multipartPart struct {
	filename    string
	contentType string
	data        []byte
}

func readMultipartParts(t *testing.T, data []byte, boundary string) map[string][]multipartPart {
	t.Helper()
	parts := make(map[string][]multipartPart)
	reader := multipart.NewReader(bytes.NewReader(data), boundary)
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			return parts
		}
		if err != nil {
			t.Fatal(err)
		}
		contents, err := io.ReadAll(part)
		if err != nil {
			t.Fatal(err)
		}
		name := part.FormName()
		parts[name] = append(parts[name], multipartPart{
			filename: part.FileName(), contentType: part.Header.Get("Content-Type"), data: contents,
		})
	}
}

func countOpenFileDescriptors(t *testing.T) int {
	t.Helper()
	for _, path := range []string{"/proc/self/fd", "/dev/fd"} {
		entries, err := os.ReadDir(path)
		if err == nil {
			return len(entries)
		}
	}
	t.Skip("open file descriptor directory is unavailable")
	return 0
}
