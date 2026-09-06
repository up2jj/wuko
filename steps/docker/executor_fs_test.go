package docker

import (
	"archive/tar"
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"

	"github.com/up2jj/wuko/executor"
)

type filesystemClient struct {
	*fakeClient
	stat      map[string]container.PathStat
	content   map[string][]byte
	copied    []client.CopyToContainerOptions
	statPaths []string
	stdin     *recordingConn
}

func newFilesystemClient() *filesystemClient {
	return &filesystemClient{fakeClient: &fakeClient{}, stat: map[string]container.PathStat{}, content: map[string][]byte{}}
}

func (c *filesystemClient) ContainerStatPath(_ context.Context, _ string, options client.ContainerStatPathOptions) (client.ContainerStatPathResult, error) {
	c.statPaths = append(c.statPaths, options.Path)
	stat, ok := c.stat[options.Path]
	if !ok {
		return client.ContainerStatPathResult{}, errdefs.ErrNotFound
	}
	return client.ContainerStatPathResult{Stat: stat}, nil
}

func (c *filesystemClient) CopyFromContainer(_ context.Context, _ string, options client.CopyFromContainerOptions) (client.CopyFromContainerResult, error) {
	data, ok := c.content[options.SourcePath]
	if !ok {
		return client.CopyFromContainerResult{}, errdefs.ErrNotFound
	}
	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	if err := writer.WriteHeader(&tar.Header{Name: "file", Mode: 0o644, Size: int64(len(data)), Typeflag: tar.TypeReg}); err != nil {
		return client.CopyFromContainerResult{}, err
	}
	if _, err := writer.Write(data); err != nil {
		return client.CopyFromContainerResult{}, err
	}
	if err := writer.Close(); err != nil {
		return client.CopyFromContainerResult{}, err
	}
	return client.CopyFromContainerResult{Content: io.NopCloser(&buffer)}, nil
}

func (c *filesystemClient) CopyToContainer(_ context.Context, _ string, options client.CopyToContainerOptions) (client.CopyToContainerResult, error) {
	archived, err := io.ReadAll(options.Content)
	if err != nil {
		return client.CopyToContainerResult{}, err
	}
	options.Content = bytes.NewReader(archived)
	c.copied = append(c.copied, options)
	return client.CopyToContainerResult{}, nil
}

func (c *filesystemClient) ExecAttach(context.Context, string, client.ExecAttachOptions) (client.ExecAttachResult, error) {
	c.stdin = &recordingConn{}
	return client.ExecAttachResult{HijackedResponse: client.HijackedResponse{
		Conn: c.stdin, Reader: bufio.NewReader(bytes.NewReader(nil)),
	}}, nil
}

type recordingConn struct{ bytes.Buffer }

func (*recordingConn) Read([]byte) (int, error)         { return 0, io.EOF }
func (*recordingConn) Close() error                     { return nil }
func (*recordingConn) LocalAddr() net.Addr              { return testAddr("local") }
func (*recordingConn) RemoteAddr() net.Addr             { return testAddr("remote") }
func (*recordingConn) SetDeadline(time.Time) error      { return nil }
func (*recordingConn) SetReadDeadline(time.Time) error  { return nil }
func (*recordingConn) SetWriteDeadline(time.Time) error { return nil }

type testAddr string

func (address testAddr) Network() string { return string(address) }
func (address testAddr) String() string  { return string(address) }

func filesystemSession(client dockerClient, runDir string, mappings ...pathMapping) *dockerExecutorSession {
	session := &dockerExecutorSession{client: client, containerID: "container-id", mappings: mappings}
	session.request.RunDir = runDir
	session.config.Workspace = &WorkspaceConfig{Enabled: true, Target: "/workspace"}
	return session
}

func archiveNames(t *testing.T, options client.CopyToContainerOptions) []string {
	t.Helper()
	reader := tar.NewReader(options.Content)
	var names []string
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return names
		}
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, header.Name)
	}
}

func TestDockerFileSystemTranslatesHostPathsIntoTheContainer(t *testing.T) {
	client := newFilesystemClient()
	session := filesystemSession(client, "/host/run", pathMapping{source: "/host/run", target: "/workspace"})
	client.content["/workspace/config.yaml"] = []byte("version: 1\n")

	data, err := session.ReadFile(t.Context(), "/host/run/config.yaml", 0)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "version: 1\n" {
		t.Fatalf("content = %q", data)
	}
}

func TestDockerFileSystemRejectsRunDirectoryPathsThatAreNotMounted(t *testing.T) {
	session := filesystemSession(newFilesystemClient(), "/host/run")
	session.config.Workspace = &WorkspaceConfig{Enabled: false}
	_, err := session.ReadFile(t.Context(), "/host/run/config.yaml", 0)
	if err == nil || !strings.Contains(err.Error(), "no location inside the container") {
		t.Fatalf("ReadFile() error = %v, want a bind-mount rejection rather than a host read", err)
	}
}

func TestDockerFileSystemReportsMissingPathsAsNotExist(t *testing.T) {
	session := filesystemSession(newFilesystemClient(), "/host/run", pathMapping{source: "/host/run", target: "/workspace"})
	if _, err := session.Stat(t.Context(), "/host/run/absent"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Stat() error = %v, want fs.ErrNotExist", err)
	}
	if _, err := session.ReadFile(t.Context(), "/host/run/absent", 0); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("ReadFile() error = %v, want fs.ErrNotExist", err)
	}
}

func TestDockerFileSystemStatReportsContainerMetadata(t *testing.T) {
	client := newFilesystemClient()
	modified := time.Date(2026, time.March, 2, 10, 30, 0, 0, time.UTC)
	client.stat["/workspace/config.yaml"] = container.PathStat{Size: 11, Mode: 0o640, Mtime: modified}
	session := filesystemSession(client, "/host/run", pathMapping{source: "/host/run", target: "/workspace"})

	info, err := session.Stat(t.Context(), "/host/run/config.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if info.Size != 11 || info.Mode.Perm() != 0o640 || !info.ModTime.Equal(modified) {
		t.Fatalf("info = %+v", info)
	}
}

func TestDockerFileSystemWritesAtomicallyAsTheExecutorUser(t *testing.T) {
	client := newFilesystemClient()
	session := filesystemSession(client, "/host/run", pathMapping{source: "/host/run", target: "/workspace"})
	session.config.User = "1000:1000"

	if err := session.WriteFile(t.Context(), "/host/run/nested/config.yaml", []byte("version: 2\n"), executor.WriteOptions{Mode: 0o600, Replace: true}); err != nil {
		t.Fatal(err)
	}
	if len(client.execCreated) != 1 {
		t.Fatalf("ExecCreate calls = %d, want one", len(client.execCreated))
	}
	created := client.execCreated[0]
	if created.User != "1000:1000" || len(created.Cmd) < 8 {
		t.Fatalf("exec options = %+v", created)
	}
	if created.Cmd[len(created.Cmd)-3] != "/workspace/nested/config.yaml" {
		t.Fatalf("command = %q", created.Cmd)
	}
	if !strings.Contains(created.Cmd[2], "mv -fT") || created.Cmd[len(created.Cmd)-1] != "true" {
		t.Fatalf("install command = %q", created.Cmd)
	}
	if client.stdin.String() != "version: 2\n" {
		t.Fatalf("stdin = %q", client.stdin.String())
	}
}

func TestDockerFileSystemNoReplaceReportsConcurrentDestination(t *testing.T) {
	client := newFilesystemClient()
	client.exitCode = fileExistsExitCode
	session := filesystemSession(client, "/host/run", pathMapping{source: "/host/run", target: "/workspace"})
	err := session.WriteFile(t.Context(), "/host/run/config.yaml", []byte("new"), executor.WriteOptions{Mode: 0o600})
	if !errors.Is(err, fs.ErrExist) {
		t.Fatalf("WriteFile() error = %v, want fs.ErrExist", err)
	}
	if !strings.Contains(client.execCreated[0].Cmd[2], "ln -T") {
		t.Fatalf("install command = %q", client.execCreated[0].Cmd)
	}
}

func TestDockerFileSystemBoundsReads(t *testing.T) {
	client := newFilesystemClient()
	client.content["/workspace/config.yaml"] = []byte("123456789")
	session := filesystemSession(client, "/host/run", pathMapping{source: "/host/run", target: "/workspace"})
	data, err := session.ReadFile(t.Context(), "/host/run/config.yaml", 4)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "12345" {
		t.Fatalf("ReadFile() = %q, want maximum+1 bytes", data)
	}
}

func TestDockerFileSystemMkdirAllCreatesOnlyMissingComponents(t *testing.T) {
	client := newFilesystemClient()
	client.stat["/workspace"] = container.PathStat{Mode: os.ModeDir | 0o755}
	session := filesystemSession(client, "/host/run", pathMapping{source: "/host/run", target: "/workspace"})

	if err := session.MkdirAll(t.Context(), "/host/run/build/reports", 0o750); err != nil {
		t.Fatal(err)
	}
	if len(client.copied) != 1 {
		t.Fatalf("CopyToContainer calls = %d, want one", len(client.copied))
	}
	if client.copied[0].DestinationPath != "/workspace" {
		t.Fatalf("destination = %q, want the deepest existing directory", client.copied[0].DestinationPath)
	}
	if !client.copied[0].CopyUIDGID {
		t.Fatal("MkdirAll did not request executor-user ownership")
	}
	names := archiveNames(t, client.copied[0])
	if len(names) != 2 || names[0] != "build/" || names[1] != "build/reports/" {
		t.Fatalf("archive entries = %v, want the missing chain in order", names)
	}
}

func TestDockerFileSystemMkdirAllAcceptsAnExistingDirectory(t *testing.T) {
	client := newFilesystemClient()
	client.stat["/workspace/build"] = container.PathStat{Mode: os.ModeDir | 0o755}
	session := filesystemSession(client, "/host/run", pathMapping{source: "/host/run", target: "/workspace"})

	if err := session.MkdirAll(t.Context(), "/host/run/build", 0o755); err != nil {
		t.Fatal(err)
	}
	if len(client.copied) != 0 {
		t.Fatalf("MkdirAll archived %d times for a directory that already exists", len(client.copied))
	}
}

func TestDockerFileSystemMkdirAllRejectsAFileInThePath(t *testing.T) {
	client := newFilesystemClient()
	client.stat["/workspace/build"] = container.PathStat{Mode: 0o644}
	session := filesystemSession(client, "/host/run", pathMapping{source: "/host/run", target: "/workspace"})

	err := session.MkdirAll(t.Context(), "/host/run/build", 0o755)
	if err == nil || !strings.Contains(err.Error(), "is not a directory") {
		t.Fatalf("MkdirAll() error = %v", err)
	}
}

func TestDockerFileSystemRefusesAClosedSession(t *testing.T) {
	session := filesystemSession(newFilesystemClient(), "/host/run", pathMapping{source: "/host/run", target: "/workspace"})
	session.closed = true
	if _, err := session.Stat(t.Context(), "/host/run/config.yaml"); err == nil || !strings.Contains(err.Error(), "session is closed") {
		t.Fatalf("Stat() error = %v", err)
	}
}

var _ executor.FileSystem = (*dockerExecutorSession)(nil)
