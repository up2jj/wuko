package executor_test

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/up2jj/wuko/executor"
	"github.com/up2jj/wuko/process"
)

type sessionWithoutFileSystem struct{}

func (sessionWithoutFileSystem) Run(context.Context, process.Options) (process.Result, error) {
	return process.Result{}, nil
}

type sessionWithFileSystem struct {
	sessionWithoutFileSystem
	executor.LocalFileSystem
}

func TestFileSystemForRecognizesHostTargets(t *testing.T) {
	for _, test := range []struct {
		name   string
		target process.Executor
	}{
		{name: "nil target", target: nil},
		{name: "local executor", target: process.LocalExecutor{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			filesystem, err := executor.FileSystemFor(test.target)
			if err != nil {
				t.Fatal(err)
			}
			if !executor.IsLocal(filesystem) {
				t.Fatalf("FileSystemFor(%s) = %T, want the host filesystem", test.name, filesystem)
			}
		})
	}
}

func TestFileSystemForRejectsTargetWithoutFileSystem(t *testing.T) {
	if _, err := executor.FileSystemFor(sessionWithoutFileSystem{}); err == nil {
		t.Fatal("FileSystemFor() accepted a target that cannot reach a filesystem")
	}
	filesystem, err := executor.FileSystemFor(sessionWithFileSystem{})
	if err != nil {
		t.Fatal(err)
	}
	if executor.IsLocal(filesystem) {
		t.Fatal("a session that implements FileSystem must not be treated as the host filesystem")
	}
}

func TestLocalFileSystemWritesDurablyWithoutLeavingTemporaryFiles(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "config.yaml")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := (executor.LocalFileSystem{}).WriteFile(t.Context(), path, []byte("new"), executor.WriteOptions{Mode: 0o640, Replace: true}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "new" {
		t.Fatalf("content = %q", data)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("mode = %v", info.Mode().Perm())
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("directory retains %d entries, want only the destination", len(entries))
	}
}

func TestLocalFileSystemReportsMissingPathsAsNotExist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "absent")
	if _, err := (executor.LocalFileSystem{}).Stat(t.Context(), path); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Stat() error = %v, want fs.ErrNotExist", err)
	}
	if _, err := (executor.LocalFileSystem{}).ReadFile(t.Context(), path, 0); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("ReadFile() error = %v, want fs.ErrNotExist", err)
	}
}

func TestLocalFileSystemBoundsReads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "document")
	if err := os.WriteFile(path, []byte("123456789"), 0o600); err != nil {
		t.Fatal(err)
	}
	data, err := (executor.LocalFileSystem{}).ReadFile(t.Context(), path, 4)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "12345" {
		t.Fatalf("ReadFile() = %q, want maximum+1 bytes", data)
	}
}

func TestLocalFileSystemMkdirAllAppliesModeDespiteUmask(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "target")
	if err := (executor.LocalFileSystem{}).MkdirAll(t.Context(), path, 0o777); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o777 {
		t.Fatalf("mode = %v, want the requested mode rather than the umasked one", info.Mode().Perm())
	}
}

func TestLocalFileSystemHonorsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	path := filepath.Join(t.TempDir(), "file")
	if err := (executor.LocalFileSystem{}).WriteFile(ctx, path, []byte("data"), executor.WriteOptions{Mode: 0o644}); !errors.Is(err, context.Canceled) {
		t.Fatalf("WriteFile() error = %v, want context.Canceled", err)
	}
	if _, err := os.Lstat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("canceled write created %s", path)
	}
}

func TestLocalFileSystemDoesNotReplaceWithoutPermission(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "config.yaml")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := (executor.LocalFileSystem{}).WriteFile(t.Context(), path, []byte("new"), executor.WriteOptions{Mode: 0o640})
	if !errors.Is(err, fs.ErrExist) {
		t.Fatalf("WriteFile() error = %v, want fs.ErrExist", err)
	}
	data, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(data) != "old" {
		t.Fatalf("destination = %q, want original content", data)
	}
}
