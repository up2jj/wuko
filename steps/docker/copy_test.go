package docker

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"

	"github.com/up2jj/wuko/step"
)

func TestCopyToContainerMatchesDockerCopySemantics(t *testing.T) {
	runDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(runDir, "artifact.txt"), []byte("artifact"), 0o640); err != nil {
		t.Fatal(err)
	}

	t.Run("relative file into existing directory", func(t *testing.T) {
		client := newCopyClient()
		client.stats["/workspace"] = container.PathStat{Mode: os.ModeDir | 0o755}
		runner := copyRunner(Config{Operation: operationCopyTo, Container: "build", Source: "artifact.txt", Target: "/workspace"}, client)

		result, err := runner.Run(t.Context(), step.Request{RunDir: runDir})
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
		if client.copyToContainer != "build" || client.copyToOptions.DestinationPath != "/workspace" {
			t.Fatalf("copy request = container:%q destination:%q", client.copyToContainer, client.copyToOptions.DestinationPath)
		}
		entries := readTarEntries(t, client.copyToContent)
		if got := string(entries["artifact.txt"].body); got != "artifact" {
			t.Fatalf("archive artifact = %q", got)
		}
		if client.copyToOptions.CopyUIDGID || client.copyToOptions.AllowOverwriteDirWithFile {
			t.Fatalf("copy options = %#v, want Docker defaults", client.copyToOptions)
		}
		assertCopyOutputs(t, result, "build", "artifact.txt", "/workspace")
	})

	t.Run("rename into missing destination", func(t *testing.T) {
		client := newCopyClient()
		runner := copyRunner(Config{Operation: operationCopyTo, Container: "build", Source: "artifact.txt", Target: "/renamed.txt"}, client)

		if _, err := runner.Run(t.Context(), step.Request{RunDir: runDir}); err != nil {
			t.Fatalf("Run() error = %v", err)
		}
		if client.copyToOptions.DestinationPath != "/" {
			t.Fatalf("destination = %q, want /", client.copyToOptions.DestinationPath)
		}
		entries := readTarEntries(t, client.copyToContent)
		if got := string(entries["renamed.txt"].body); got != "artifact" {
			t.Fatalf("renamed archive artifact = %q", got)
		}
	})

	t.Run("absolute host source", func(t *testing.T) {
		client := newCopyClient()
		client.stats["/workspace"] = container.PathStat{Mode: os.ModeDir | 0o755}
		absoluteSource := filepath.Join(runDir, "artifact.txt")
		runner := copyRunner(Config{Operation: operationCopyTo, Container: "build", Source: absoluteSource, Target: "/workspace"}, client)

		if _, err := runner.Run(t.Context(), step.Request{RunDir: t.TempDir()}); err != nil {
			t.Fatalf("Run() error = %v", err)
		}
		if got := string(readTarEntries(t, client.copyToContent)["artifact.txt"].body); got != "artifact" {
			t.Fatalf("archive artifact = %q", got)
		}
	})

	t.Run("directory contents with trailing dot", func(t *testing.T) {
		if err := os.MkdirAll(filepath.Join(runDir, "tree"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(runDir, "tree", "nested.txt"), []byte("nested"), 0o600); err != nil {
			t.Fatal(err)
		}
		client := newCopyClient()
		client.stats["/workspace"] = container.PathStat{Mode: os.ModeDir | 0o755}
		runner := copyRunner(Config{Operation: operationCopyTo, Container: "build", Source: "tree/.", Target: "/workspace"}, client)

		if _, err := runner.Run(t.Context(), step.Request{RunDir: runDir}); err != nil {
			t.Fatalf("Run() error = %v", err)
		}
		entries := readTarEntries(t, client.copyToContent)
		if got := string(entries["./nested.txt"].body); got != "nested" {
			t.Fatalf("nested archive artifact = %q; entries = %#v", got, entries)
		}
	})

	t.Run("source symlink is not followed", func(t *testing.T) {
		link := filepath.Join(runDir, "artifact-link")
		if err := os.Symlink("artifact.txt", link); err != nil {
			t.Fatal(err)
		}
		client := newCopyClient()
		client.stats["/workspace"] = container.PathStat{Mode: os.ModeDir | 0o755}
		runner := copyRunner(Config{Operation: operationCopyTo, Container: "build", Source: "artifact-link", Target: "/workspace"}, client)

		if _, err := runner.Run(t.Context(), step.Request{RunDir: runDir}); err != nil {
			t.Fatalf("Run() error = %v", err)
		}
		entry := readTarEntries(t, client.copyToContent)["artifact-link"]
		if entry.header == nil || entry.header.Typeflag != tar.TypeSymlink || entry.header.Linkname != "artifact.txt" {
			t.Fatalf("symlink entry = %#v", entry.header)
		}
	})

	t.Run("container destination symlink is followed", func(t *testing.T) {
		client := newCopyClient()
		client.stats["/current"] = container.PathStat{Mode: os.ModeSymlink | 0o777, LinkTarget: "/workspace"}
		client.stats["/workspace"] = container.PathStat{Mode: os.ModeDir | 0o755}
		runner := copyRunner(Config{Operation: operationCopyTo, Container: "build", Source: "artifact.txt", Target: "/current"}, client)

		if _, err := runner.Run(t.Context(), step.Request{RunDir: runDir}); err != nil {
			t.Fatalf("Run() error = %v", err)
		}
		if client.copyToOptions.DestinationPath != "/workspace" {
			t.Fatalf("destination = %q, want followed symlink target", client.copyToOptions.DestinationPath)
		}
	})

	t.Run("directory cannot replace container file", func(t *testing.T) {
		client := newCopyClient()
		client.stats["/artifact"] = container.PathStat{Mode: 0o600}
		runner := copyRunner(Config{Operation: operationCopyTo, Container: "build", Source: "tree", Target: "/artifact"}, client)

		if _, err := runner.Run(t.Context(), step.Request{RunDir: runDir}); err == nil || !strings.Contains(err.Error(), "cannot copy directory") {
			t.Fatalf("Run() error = %v", err)
		}
		if client.copyToCalled {
			t.Fatal("copy API called despite directory/file conflict")
		}
	})

	t.Run("missing source", func(t *testing.T) {
		client := newCopyClient()
		runner := copyRunner(Config{Operation: operationCopyTo, Container: "build", Source: "missing", Target: "/workspace"}, client)
		if _, err := runner.Run(t.Context(), step.Request{RunDir: runDir}); err == nil || !strings.Contains(err.Error(), "inspecting Docker copy_to source") {
			t.Fatalf("Run() error = %v", err)
		}
		if client.copyToCalled {
			t.Fatal("copy API called for missing source")
		}
	})

	t.Run("API failure retains paths", func(t *testing.T) {
		client := newCopyClient()
		client.copyToErr = errors.New("daemon rejected archive")
		runner := copyRunner(Config{Operation: operationCopyTo, Container: "build", Source: "artifact.txt", Target: "/workspace"}, client)
		_, err := runner.Run(t.Context(), step.Request{RunDir: runDir})
		if err == nil || !strings.Contains(err.Error(), "artifact.txt") || !strings.Contains(err.Error(), "build") || !errors.Is(err, client.copyToErr) {
			t.Fatalf("Run() error = %v", err)
		}
	})
}

func TestCopyFromContainerMatchesDockerCopySemantics(t *testing.T) {
	t.Run("renames file into relative host target", func(t *testing.T) {
		runDir := t.TempDir()
		if err := os.Mkdir(filepath.Join(runDir, "out"), 0o755); err != nil {
			t.Fatal(err)
		}
		stream := &trackedReadCloser{Reader: bytes.NewReader(tarBytes(t, tarEntry{name: "result.txt", body: "result", mode: 0o640}))}
		client := newCopyClient()
		client.copyFrom = clientpkgCopyFromResult(stream, container.PathStat{Name: "result.txt", Size: 6, Mode: 0o640})
		runner := copyRunner(Config{Operation: operationCopyFrom, Container: "build", Source: "/tmp/result.txt", Target: "out/renamed.txt"}, client)

		result, err := runner.Run(t.Context(), step.Request{RunDir: runDir})
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
		data, err := os.ReadFile(filepath.Join(runDir, "out", "renamed.txt"))
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != "result" || client.copyFromContainer != "build" || client.copyFromPath != "/tmp/result.txt" {
			t.Fatalf("copied data/request = %q, %q:%q", data, client.copyFromContainer, client.copyFromPath)
		}
		if stream.closeCalls == 0 {
			t.Fatal("container archive was not closed")
		}
		assertCopyOutputs(t, result, "build", "/tmp/result.txt", "out/renamed.txt")
	})

	t.Run("renames directory tree", func(t *testing.T) {
		runDir := t.TempDir()
		stream := &trackedReadCloser{Reader: bytes.NewReader(tarBytes(t,
			tarEntry{name: "tree/", mode: 0o755, typeflag: tar.TypeDir},
			tarEntry{name: "tree/nested.txt", body: "nested", mode: 0o600},
		))}
		client := newCopyClient()
		client.copyFrom = clientpkgCopyFromResult(stream, container.PathStat{Name: "tree", Mode: os.ModeDir | 0o755})
		runner := copyRunner(Config{Operation: operationCopyFrom, Container: "build", Source: "/tmp/tree", Target: "download"}, client)

		if _, err := runner.Run(t.Context(), step.Request{RunDir: runDir}); err != nil {
			t.Fatalf("Run() error = %v", err)
		}
		data, err := os.ReadFile(filepath.Join(runDir, "download", "nested.txt"))
		if err != nil || string(data) != "nested" {
			t.Fatalf("downloaded nested file = %q, %v", data, err)
		}
	})

	t.Run("merges directory into existing host directory", func(t *testing.T) {
		runDir := t.TempDir()
		target := filepath.Join(runDir, "existing")
		if err := os.Mkdir(target, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(target, "keep.txt"), []byte("keep"), 0o600); err != nil {
			t.Fatal(err)
		}
		stream := &trackedReadCloser{Reader: bytes.NewReader(tarBytes(t,
			tarEntry{name: "tree/", mode: 0o755, typeflag: tar.TypeDir},
			tarEntry{name: "tree/nested.txt", body: "nested", mode: 0o600},
		))}
		client := newCopyClient()
		client.copyFrom = clientpkgCopyFromResult(stream, container.PathStat{Name: "tree", Mode: os.ModeDir | 0o755})
		runner := copyRunner(Config{Operation: operationCopyFrom, Container: "build", Source: "/tmp/tree", Target: target}, client)

		if _, err := runner.Run(t.Context(), step.Request{RunDir: t.TempDir()}); err != nil {
			t.Fatalf("Run() error = %v", err)
		}
		for name, want := range map[string]string{"keep.txt": "keep", filepath.Join("tree", "nested.txt"): "nested"} {
			data, err := os.ReadFile(filepath.Join(target, name))
			if err != nil || string(data) != want {
				t.Fatalf("merged file %q = %q, %v", name, data, err)
			}
		}
	})

	t.Run("preserves source symlink", func(t *testing.T) {
		runDir := t.TempDir()
		stream := &trackedReadCloser{Reader: bytes.NewReader(tarBytes(t, tarEntry{name: "latest", mode: 0o777, typeflag: tar.TypeSymlink, linkname: "result.txt"}))}
		client := newCopyClient()
		client.copyFrom = clientpkgCopyFromResult(stream, container.PathStat{Name: "latest", Mode: os.ModeSymlink | 0o777, LinkTarget: "result.txt"})
		runner := copyRunner(Config{Operation: operationCopyFrom, Container: "build", Source: "/tmp/latest", Target: "latest"}, client)

		if _, err := runner.Run(t.Context(), step.Request{RunDir: runDir}); err != nil {
			t.Fatalf("Run() error = %v", err)
		}
		link, err := os.Readlink(filepath.Join(runDir, "latest"))
		if err != nil || link != "result.txt" {
			t.Fatalf("downloaded symlink = %q, %v", link, err)
		}
	})

	t.Run("requires target parent", func(t *testing.T) {
		runDir := t.TempDir()
		stream := &trackedReadCloser{Reader: bytes.NewReader(tarBytes(t, tarEntry{name: "result.txt", body: "result", mode: 0o600}))}
		client := newCopyClient()
		client.copyFrom = clientpkgCopyFromResult(stream, container.PathStat{Name: "result.txt", Mode: 0o600})
		runner := copyRunner(Config{Operation: operationCopyFrom, Container: "build", Source: "/tmp/result.txt", Target: "missing/result.txt"}, client)

		if _, err := runner.Run(t.Context(), step.Request{RunDir: runDir}); err == nil || !strings.Contains(err.Error(), "extracting Docker container") {
			t.Fatalf("Run() error = %v", err)
		}
		if stream.closeCalls == 0 {
			t.Fatal("container archive was not closed after extraction failure")
		}
	})

	t.Run("rejects path traversal archive", func(t *testing.T) {
		runDir := t.TempDir()
		stream := &trackedReadCloser{Reader: bytes.NewReader(tarBytes(t, tarEntry{name: "../escape", body: "unsafe", mode: 0o600}))}
		client := newCopyClient()
		client.copyFrom = clientpkgCopyFromResult(stream, container.PathStat{Name: "result", Mode: os.ModeDir | 0o755})
		runner := copyRunner(Config{Operation: operationCopyFrom, Container: "build", Source: "/tmp/result", Target: "result"}, client)

		if _, err := runner.Run(t.Context(), step.Request{RunDir: runDir}); err == nil {
			t.Fatal("Run() unexpectedly accepted traversal archive")
		}
		if _, err := os.Stat(filepath.Join(filepath.Dir(runDir), "escape")); !os.IsNotExist(err) {
			t.Fatalf("escape path exists or stat failed unexpectedly: %v", err)
		}
	})

	t.Run("rejects malformed archive", func(t *testing.T) {
		stream := &trackedReadCloser{Reader: strings.NewReader("not a tar archive")}
		client := newCopyClient()
		client.copyFrom = clientpkgCopyFromResult(stream, container.PathStat{Name: "result", Mode: 0o600})
		runner := copyRunner(Config{Operation: operationCopyFrom, Container: "build", Source: "/tmp/result", Target: "result"}, client)

		if _, err := runner.Run(t.Context(), step.Request{RunDir: t.TempDir()}); err == nil || !strings.Contains(err.Error(), "extracting Docker container") {
			t.Fatalf("Run() error = %v", err)
		}
		if stream.closeCalls == 0 {
			t.Fatal("malformed container archive was not closed")
		}
	})

	t.Run("API and close failures are retained", func(t *testing.T) {
		apiClient := newCopyClient()
		apiClient.copyFromErr = errors.New("container path missing")
		runner := copyRunner(Config{Operation: operationCopyFrom, Container: "build", Source: "/tmp/result", Target: "result"}, apiClient)
		if _, err := runner.Run(t.Context(), step.Request{RunDir: t.TempDir()}); err == nil || !errors.Is(err, apiClient.copyFromErr) {
			t.Fatalf("API error = %v", err)
		}

		closeErr := errors.New("close failed")
		stream := &trackedReadCloser{Reader: bytes.NewReader(tarBytes(t, tarEntry{name: "result", body: "ok", mode: 0o600})), closeErr: closeErr}
		closeClient := newCopyClient()
		closeClient.copyFrom = clientpkgCopyFromResult(stream, container.PathStat{Name: "result", Mode: 0o600})
		runner = copyRunner(Config{Operation: operationCopyFrom, Container: "build", Source: "/tmp/result", Target: "result"}, closeClient)
		if _, err := runner.Run(t.Context(), step.Request{RunDir: t.TempDir()}); err == nil || !errors.Is(err, closeErr) {
			t.Fatalf("close error = %v", err)
		}
	})
}

type copyClient struct {
	*fakeClient
	stats             map[string]container.PathStat
	statPaths         []string
	copyToContainer   string
	copyToOptions     client.CopyToContainerOptions
	copyToContent     []byte
	copyToCalled      bool
	copyToErr         error
	copyFromContainer string
	copyFromPath      string
	copyFrom          client.CopyFromContainerResult
	copyFromErr       error
}

func newCopyClient() *copyClient {
	return &copyClient{fakeClient: &fakeClient{}, stats: make(map[string]container.PathStat)}
}

func (f *copyClient) ContainerStatPath(_ context.Context, _ string, options client.ContainerStatPathOptions) (client.ContainerStatPathResult, error) {
	f.statPaths = append(f.statPaths, options.Path)
	stat, ok := f.stats[options.Path]
	if !ok {
		return client.ContainerStatPathResult{}, errdefs.ErrNotFound
	}
	return client.ContainerStatPathResult{Stat: stat}, nil
}

func (f *copyClient) CopyToContainer(_ context.Context, container string, options client.CopyToContainerOptions) (client.CopyToContainerResult, error) {
	f.copyToCalled = true
	f.copyToContainer = container
	f.copyToOptions = options
	content, err := io.ReadAll(options.Content)
	if err != nil {
		return client.CopyToContainerResult{}, err
	}
	f.copyToContent = content
	return client.CopyToContainerResult{}, f.copyToErr
}

func (f *copyClient) CopyFromContainer(_ context.Context, container string, options client.CopyFromContainerOptions) (client.CopyFromContainerResult, error) {
	f.copyFromContainer = container
	f.copyFromPath = options.SourcePath
	return f.copyFrom, f.copyFromErr
}

func copyRunner(config Config, fake dockerClient) *Runner {
	return &Runner{config: config, newClient: func() (dockerClient, error) { return fake, nil }}
}

func clientpkgCopyFromResult(content io.ReadCloser, stat container.PathStat) client.CopyFromContainerResult {
	return client.CopyFromContainerResult{Content: content, Stat: stat}
}

type trackedReadCloser struct {
	io.Reader
	closeCalls int
	closeErr   error
}

func (r *trackedReadCloser) Close() error {
	r.closeCalls++
	return r.closeErr
}

type tarEntry struct {
	name     string
	body     string
	mode     int64
	typeflag byte
	linkname string
}

func tarBytes(t *testing.T, entries ...tarEntry) []byte {
	t.Helper()
	var content bytes.Buffer
	writer := tar.NewWriter(&content)
	for _, entry := range entries {
		typeflag := entry.typeflag
		if typeflag == 0 {
			typeflag = tar.TypeReg
		}
		header := &tar.Header{
			Name: entry.name, Mode: entry.mode, Size: int64(len(entry.body)),
			Typeflag: typeflag, Linkname: entry.linkname,
		}
		if typeflag != tar.TypeReg {
			header.Size = 0
		}
		if err := writer.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if header.Size > 0 {
			if _, err := io.WriteString(writer, entry.body); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return content.Bytes()
}

type archivedEntry struct {
	header *tar.Header
	body   []byte
}

func readTarEntries(t *testing.T, content []byte) map[string]archivedEntry {
	t.Helper()
	entries := make(map[string]archivedEntry)
	reader := tar.NewReader(bytes.NewReader(content))
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return entries
		}
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(reader)
		if err != nil {
			t.Fatal(err)
		}
		cloned := *header
		entries[header.Name] = archivedEntry{header: &cloned, body: body}
	}
}

func assertCopyOutputs(t *testing.T, result step.Result, container, source, target string) {
	t.Helper()
	if result.Outputs["container"] != container || result.Outputs["source"] != source || result.Outputs["target"] != target {
		t.Fatalf("outputs = %#v", result.Outputs)
	}
}
