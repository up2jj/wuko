package file

import (
	"context"
	"io/fs"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/up2jj/wuko/executor"
	"github.com/up2jj/wuko/process"
	"github.com/up2jj/wuko/step"
)

// targetSession stands in for an executor session whose filesystem is not the host's.
type targetSession struct {
	files       map[string][]byte
	modes       map[string]fs.FileMode
	directories map[string]bool
}

func newTargetSession() *targetSession {
	return &targetSession{files: map[string][]byte{}, modes: map[string]fs.FileMode{}, directories: map[string]bool{}}
}

func (*targetSession) Run(context.Context, process.Options) (process.Result, error) {
	return process.Result{}, nil
}

func (s *targetSession) Stat(_ context.Context, path string) (executor.FileInfo, error) {
	if s.directories[path] {
		return executor.FileInfo{Mode: fs.ModeDir | s.modes[path], ModTime: time.Unix(0, 0)}, nil
	}
	data, ok := s.files[path]
	if !ok {
		return executor.FileInfo{}, &fs.PathError{Op: "stat", Path: path, Err: fs.ErrNotExist}
	}
	return executor.FileInfo{Size: int64(len(data)), Mode: s.modes[path], ModTime: time.Unix(0, 0)}, nil
}

func (s *targetSession) ReadFile(_ context.Context, path string, maximum int64) ([]byte, error) {
	data, ok := s.files[path]
	if !ok {
		return nil, &fs.PathError{Op: "open", Path: path, Err: fs.ErrNotExist}
	}
	if maximum > 0 && int64(len(data)) > maximum {
		return data[:maximum+1], nil
	}
	return data, nil
}

func (s *targetSession) WriteFile(_ context.Context, path string, data []byte, options executor.WriteOptions) error {
	s.files[path] = data
	s.modes[path] = options.Mode
	return nil
}

func (s *targetSession) MkdirAll(_ context.Context, path string, mode fs.FileMode) error {
	s.directories[path] = true
	s.modes[path] = mode
	return nil
}

// commandOnlySession runs commands but exposes no filesystem, as a plugin executor does.
type commandOnlySession struct{}

func (commandOnlySession) Run(context.Context, process.Options) (process.Result, error) {
	return process.Result{}, nil
}

func remoteRunner(t *testing.T, config map[string]any) step.Runner {
	t.Helper()
	runner, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := runner.(step.ExecutorAware); !ok {
		t.Fatalf("file %v is not usable inside executor blocks", config["operation"])
	}
	return runner
}

func TestFileMarksOnlyRemoteCapableOperationsExecutorAware(t *testing.T) {
	aware := map[string]map[string]any{
		"read":  {"operation": "read", "path": "artifact.txt"},
		"write": {"operation": "write", "path": "artifact.txt", "content": "data"},
		"mkdir": {"operation": "mkdir", "path": "build"},
		"stat":  {"operation": "stat", "path": "artifact.txt"},
	}
	for name, config := range aware {
		t.Run(name, func(t *testing.T) { remoteRunner(t, config) })
	}
	hostOnly := map[string]map[string]any{
		"link":       {"operation": "link", "path": "artifact.txt", "destination": "link.txt", "link_type": "symbolic"},
		"remove":     {"operation": "remove", "path": "artifact.txt"},
		"list":       {"operation": "list", "path": "."},
		"disk_usage": {"operation": "disk_usage", "path": "."},
		"templated":  {"operation": "{{ .vars.operation }}", "path": "artifact.txt"},
	}
	for name, config := range hostOnly {
		t.Run(name, func(t *testing.T) {
			runner, err := New(config)
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := runner.(step.ExecutorAware); ok {
				t.Fatalf("file %v claims executor support it cannot honor", config["operation"])
			}
		})
	}
}

func TestFileWritesThroughTheExecutorFileSystem(t *testing.T) {
	session := newTargetSession()
	runner := remoteRunner(t, map[string]any{"operation": "write", "path": "artifact.txt", "content": "v1", "mode": "0600"})
	root := t.TempDir()

	result, err := runner.Run(t.Context(), step.Request{RunDir: root, Executor: session})
	if err != nil {
		t.Fatal(err)
	}
	target := root + "/artifact.txt"
	if string(session.files[target]) != "v1" || session.modes[target] != 0o600 {
		t.Fatalf("target files = %v modes = %v", session.files, session.modes)
	}
	if result.Outputs["created"] != true || result.Outputs["mode"] != "0600" {
		t.Fatalf("outputs = %#v", result.Outputs)
	}
	if _, err := os.Lstat(target); err == nil {
		t.Fatalf("write inside an executor block also touched the host at %s", target)
	}
}

func TestFileReadsAndStatsThroughTheExecutorFileSystem(t *testing.T) {
	root := t.TempDir()
	session := newTargetSession()
	session.files[root+"/artifact.txt"] = []byte("v1-linux")
	session.modes[root+"/artifact.txt"] = 0o640

	read := remoteRunner(t, map[string]any{"operation": "read", "path": "artifact.txt"})
	result, err := read.Run(t.Context(), step.Request{RunDir: root, Executor: session})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outputs["content"] != "v1-linux" || result.Outputs["size"] != int64(8) {
		t.Fatalf("outputs = %#v", result.Outputs)
	}

	stat := remoteRunner(t, map[string]any{"operation": "stat", "path": "artifact.txt"})
	result, err = stat.Run(t.Context(), step.Request{RunDir: root, Executor: session})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outputs["exists"] != true || result.Outputs["mode"] != "0640" || result.Outputs["type"] != "file" {
		t.Fatalf("outputs = %#v", result.Outputs)
	}

	missing := remoteRunner(t, map[string]any{"operation": "stat", "path": "absent.txt"})
	result, err = missing.Run(t.Context(), step.Request{RunDir: root, Executor: session})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outputs["exists"] != false {
		t.Fatalf("outputs = %#v", result.Outputs)
	}
}

func TestFileRefusesAnOverwriteOnTheTargetWithoutPermission(t *testing.T) {
	root := t.TempDir()
	session := newTargetSession()
	session.files[root+"/artifact.txt"] = []byte("v1")

	runner := remoteRunner(t, map[string]any{"operation": "write", "path": "artifact.txt", "content": "v2"})
	_, err := runner.Run(t.Context(), step.Request{RunDir: root, Executor: session})
	if err == nil || !strings.Contains(err.Error(), "set overwrite to true") {
		t.Fatalf("Run() error = %v", err)
	}
	if string(session.files[root+"/artifact.txt"]) != "v1" {
		t.Fatal("refused write changed the target anyway")
	}
}

func TestFileMkdirRequiresAnExistingParentWhenNotRecursive(t *testing.T) {
	root := t.TempDir()
	session := newTargetSession()

	runner := remoteRunner(t, map[string]any{"operation": "mkdir", "path": "build/reports"})
	if _, err := runner.Run(t.Context(), step.Request{RunDir: root, Executor: session}); err == nil {
		t.Fatal("mkdir created a nested directory without recursive")
	}

	recursive := remoteRunner(t, map[string]any{"operation": "mkdir", "path": "build/reports", "recursive": true, "mode": "0750"})
	result, err := recursive.Run(t.Context(), step.Request{RunDir: root, Executor: session})
	if err != nil {
		t.Fatal(err)
	}
	if !session.directories[root+"/build/reports"] || result.Outputs["mode"] != "0750" {
		t.Fatalf("directories = %v outputs = %#v", session.directories, result.Outputs)
	}
}

func TestFileRefusesATargetWithoutAFileSystem(t *testing.T) {
	runner := remoteRunner(t, map[string]any{"operation": "read", "path": "artifact.txt"})
	_, err := runner.Run(t.Context(), step.Request{RunDir: t.TempDir(), Executor: commandOnlySession{}})
	if err == nil || !strings.Contains(err.Error(), "does not expose a filesystem") {
		t.Fatalf("Run() error = %v, want a refusal rather than a host read", err)
	}
}
