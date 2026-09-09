package markdownedit

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

type targetSession struct {
	files       map[string][]byte
	modes       map[string]fs.FileMode
	readMaximum int64
	replace     bool
}

func (*targetSession) Run(context.Context, process.Options) (process.Result, error) {
	return process.Result{}, nil
}

func (s *targetSession) Stat(_ context.Context, path string) (executor.FileInfo, error) {
	data, ok := s.files[path]
	if !ok {
		return executor.FileInfo{}, &fs.PathError{Op: "stat", Path: path, Err: fs.ErrNotExist}
	}
	return executor.FileInfo{Size: int64(len(data)), Mode: s.modes[path], ModTime: time.Unix(0, 0)}, nil
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

func (*targetSession) MkdirAll(context.Context, string, fs.FileMode) error { return nil }

type commandOnlySession struct{}

func (commandOnlySession) Run(context.Context, process.Options) (process.Result, error) {
	return process.Result{}, nil
}

func TestMarkdownEditUsesExecutorTargetFileSystem(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	target := filepath.Join(root, "README.md")
	session := &targetSession{
		files: map[string][]byte{target: []byte("# Before\n")},
		modes: map[string]fs.FileMode{target: 0o640},
	}
	runner := newRunner(t, map[string]any{
		"from": map[string]any{"file": "README.md"},
		"edits": []any{map[string]any{
			"operation": "set", "select": map[string]any{"kind": "heading", "text": "Before"},
			"field": "text", "value": "After",
		}},
	})
	if _, err := runner.Run(t.Context(), step.Request{RunDir: root, Executor: session}); err != nil {
		t.Fatal(err)
	}
	if got := string(session.files[target]); got != "# After\n" {
		t.Fatalf("target document = %q", got)
	}
	if session.modes[target] != 0o640 || !session.replace || session.readMaximum != 1<<20 {
		t.Fatalf("mode = %o, replace = %t, maximum = %d", session.modes[target], session.replace, session.readMaximum)
	}
	if _, err := os.Lstat(target); err == nil {
		t.Fatalf("executor edit also wrote to host at %s", target)
	}
}

func TestMarkdownEditRefusesExecutorWithoutFileSystem(t *testing.T) {
	t.Parallel()
	runner := newRunner(t, map[string]any{
		"from":  map[string]any{"file": "README.md"},
		"edits": []any{map[string]any{"operation": "delete", "select": map[string]any{"kind": "heading"}}},
	})
	_, err := runner.Run(t.Context(), step.Request{RunDir: t.TempDir(), Executor: commandOnlySession{}})
	if err == nil || !strings.Contains(err.Error(), "does not expose a filesystem") {
		t.Fatalf("error = %v", err)
	}
}

func TestMarkdownVariableSourceDoesNotNeedExecutorFileSystem(t *testing.T) {
	t.Parallel()
	runner := newRunner(t, map[string]any{
		"from": map[string]any{"var": "document"},
		"edits": []any{map[string]any{
			"operation": "set", "select": map[string]any{"kind": "heading", "text": "Before"},
			"field": "text", "value": "After",
		}},
	})
	result, err := runner.Run(t.Context(), step.Request{
		Executor: commandOnlySession{}, Vars: map[string]any{"document": "# Before\n"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outputs["value"] != "# After\n" {
		t.Fatalf("outputs = %#v", result.Outputs)
	}
}
