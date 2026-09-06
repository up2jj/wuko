package edit

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/up2jj/wuko/executor"
	"github.com/up2jj/wuko/process"
	"github.com/up2jj/wuko/step"
)

// targetSession stands in for an executor session whose filesystem is not the host's.
type targetSession struct {
	files        map[string][]byte
	modes        map[string]fs.FileMode
	readMaximum  int64
	replace      bool
	reportedSize *int64
}

func (*targetSession) Run(context.Context, process.Options) (process.Result, error) {
	return process.Result{}, nil
}

func (s *targetSession) Stat(_ context.Context, path string) (executor.FileInfo, error) {
	data, ok := s.files[path]
	if !ok {
		return executor.FileInfo{}, &fs.PathError{Op: "stat", Path: path, Err: fs.ErrNotExist}
	}
	size := int64(len(data))
	if s.reportedSize != nil {
		size = *s.reportedSize
	}
	return executor.FileInfo{Size: size, Mode: s.modes[path], ModTime: time.Unix(0, 0)}, nil
}

func (s *targetSession) ReadFile(_ context.Context, path string, maximum int64) ([]byte, error) {
	s.readMaximum = maximum
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
	s.replace = options.Replace
	return nil
}

func (s *targetSession) MkdirAll(context.Context, string, fs.FileMode) error { return nil }

type commandOnlySession struct{}

func (commandOnlySession) Run(context.Context, process.Options) (process.Result, error) {
	return process.Result{}, nil
}

func TestEditIsUsableInsideExecutorBlocks(t *testing.T) {
	runner, err := New(map[string]any{"operation": "set", "from": map[string]any{"file": "config.json"}, "path": "$.version", "value": "2"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := runner.(step.ExecutorAware); !ok {
		t.Fatal("edit is not usable inside executor blocks")
	}
}

func TestEditPatchesTheDocumentOnTheExecutorTarget(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "config.json")
	session := &targetSession{
		files: map[string][]byte{target: []byte("{\n  \"version\": \"1\"\n}\n")},
		modes: map[string]fs.FileMode{target: 0o640},
	}
	runner, err := New(map[string]any{"operation": "set", "from": map[string]any{"file": "config.json"}, "path": "$.version", "value": "2"})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := runner.Run(t.Context(), step.Request{RunDir: root, Executor: session}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(session.files[target]), `"version": "2"`) {
		t.Fatalf("target document = %q", session.files[target])
	}
	if session.modes[target] != 0o640 {
		t.Fatalf("mode = %v, want the document's own mode preserved", session.modes[target])
	}
	if !session.replace {
		t.Fatal("edit did not request replacement of its source document")
	}
	if _, err := os.Lstat(target); err == nil {
		t.Fatalf("edit inside an executor block also wrote to the host at %s", target)
	}
}

func TestEditBoundsTargetReadsByMaxBytes(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "config.json")
	reportedSize := int64(4)
	session := &targetSession{
		files:        map[string][]byte{target: []byte(`{"version":"an unexpectedly large value"}`)},
		modes:        map[string]fs.FileMode{target: 0o640},
		reportedSize: &reportedSize,
	}
	runner, err := New(map[string]any{
		"operation": "set", "from": map[string]any{"file": "config.json"},
		"path": "$.version", "value": "2", "max_bytes": "4B",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.Run(t.Context(), step.Request{RunDir: root, Executor: session})
	if err == nil || !strings.Contains(err.Error(), "exceeds max_bytes") {
		t.Fatalf("Run() error = %v", err)
	}
	if session.readMaximum != 4 {
		t.Fatalf("ReadFile maximum = %d, want 4", session.readMaximum)
	}
}

func TestEditRefusesATargetWithoutAFileSystem(t *testing.T) {
	runner, err := New(map[string]any{"operation": "set", "from": map[string]any{"file": "config.json"}, "path": "$.version", "value": "2"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.Run(t.Context(), step.Request{RunDir: t.TempDir(), Executor: commandOnlySession{}})
	if err == nil || !strings.Contains(err.Error(), "does not expose a filesystem") {
		t.Fatalf("Run() error = %v, want a refusal rather than a host edit", err)
	}
}

func TestEditFromAVariableIgnoresTheTargetFileSystem(t *testing.T) {
	runner, err := New(map[string]any{"operation": "set", "from": map[string]any{"var": "release"}, "path": "$.version", "value": "2"})
	if err != nil {
		t.Fatal(err)
	}
	// No document is read or written, so a session without a filesystem is no obstacle.
	result, err := runner.Run(t.Context(), step.Request{
		RunDir: t.TempDir(), Executor: commandOnlySession{},
		Vars: map[string]any{"release": map[string]any{"version": "1"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	value, ok := result.Outputs["value"].(map[string]any)
	if !ok || value["version"] != "2" {
		t.Fatalf("outputs = %#v", result.Outputs)
	}
}
