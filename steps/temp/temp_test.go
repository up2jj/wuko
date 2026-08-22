package temp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/up2jj/wuko/step"
)

func TestTempCreatesAndCleansManagedResources(t *testing.T) {
	tests := []struct {
		kind     string
		mode     os.FileMode
		modeType os.FileMode
	}{
		{kind: kindFile, mode: 0o600},
		{kind: kindDirectory, mode: 0o700, modeType: os.ModeDir},
		{kind: kindFIFO, mode: 0o600, modeType: os.ModeNamedPipe},
	}
	for _, test := range tests {
		t.Run(test.kind, func(t *testing.T) {
			runner := newRunner(t, map[string]any{"kind": test.kind, "pattern": "wuko-temp-test-*"})
			result, err := runner.Run(t.Context(), step.Request{})
			if err != nil {
				t.Fatal(err)
			}
			path := result.Outputs["path"].(string)
			if !filepath.IsAbs(path) || !strings.HasPrefix(filepath.Base(path), "wuko-temp-test-") {
				t.Fatalf("path = %q", path)
			}
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != test.mode {
				t.Fatalf("mode = %04o, want %04o", info.Mode().Perm(), test.mode)
			}
			if info.Mode().Type() != test.modeType {
				t.Fatalf("mode type = %v, want %v", info.Mode().Type(), test.modeType)
			}
			var fifoRoot string
			if test.kind == kindFIFO {
				fifoRoot = filepath.Dir(path)
				rootInfo, err := os.Stat(fifoRoot)
				if err != nil {
					t.Fatal(err)
				}
				if rootInfo.Mode().Perm() != 0o700 {
					t.Fatalf("FIFO directory mode = %04o, want 0700", rootInfo.Mode().Perm())
				}
			}
			if test.kind == kindDirectory {
				if err := os.WriteFile(filepath.Join(path, "nested.txt"), []byte("content"), 0o600); err != nil {
					t.Fatal(err)
				}
			} else {
				moved := path + ".moved"
				if err := os.Rename(path, moved); err != nil {
					t.Fatalf("temporary file was not closed: %v", err)
				}
				if err := os.Rename(moved, path); err != nil {
					t.Fatal(err)
				}
			}
			if result.Outputs["kind"] != test.kind {
				t.Fatalf("outputs = %#v", result.Outputs)
			}
			if err := runner.Cleanup(result); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("managed path remains: %v", err)
			}
			if fifoRoot != "" {
				if _, err := os.Lstat(fifoRoot); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("managed FIFO directory remains: %v", err)
				}
			}
			if err := runner.Cleanup(result); err != nil {
				t.Fatalf("repeated cleanup failed: %v", err)
			}
		})
	}
}

func TestTempUsesDefaultPattern(t *testing.T) {
	for _, kind := range []string{kindFile, kindFIFO} {
		t.Run(kind, func(t *testing.T) {
			runner := newRunner(t, map[string]any{"kind": kind})
			result, err := runner.Run(t.Context(), step.Request{})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = runner.Cleanup(result) })
			if base := filepath.Base(result.Outputs["path"].(string)); !strings.HasPrefix(base, "wuko-") {
				t.Fatalf("temporary name = %q", base)
			}
		})
	}
}

func TestTempRejectsInvalidConfiguration(t *testing.T) {
	tests := []struct {
		name string
		raw  map[string]any
		want string
	}{
		{name: "missing kind", raw: map[string]any{}, want: "kind is required"},
		{name: "invalid kind", raw: map[string]any{"kind": "socket"}, want: "kind must be file, directory, or fifo"},
		{name: "empty pattern", raw: map[string]any{"kind": kindFile, "pattern": ""}, want: "pattern must not be empty"},
		{name: "slash", raw: map[string]any{"kind": kindFile, "pattern": "nested/name-*"}, want: "path separators"},
		{name: "backslash", raw: map[string]any{"kind": kindFile, "pattern": `nested\name-*`}, want: "path separators"},
		{name: "unknown field", raw: map[string]any{"kind": kindFile, "mode": "0600"}, want: "field mode"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := New(test.raw)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestTempValidatesRenderedConfiguration(t *testing.T) {
	runner := newRunner(t, map[string]any{"kind": "{{ .vars.kind }}", "pattern": "{{ .vars.pattern }}"})
	if _, err := runner.Run(t.Context(), step.Request{}); err == nil || !strings.Contains(err.Error(), "kind must be") {
		t.Fatalf("unresolved configuration error = %v", err)
	}
}

func TestTempHonorsCancellation(t *testing.T) {
	for _, kind := range []string{kindDirectory, kindFIFO} {
		t.Run(kind, func(t *testing.T) {
			runner := newRunner(t, map[string]any{"kind": kind})
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			if _, err := runner.Run(ctx, step.Request{}); !errors.Is(err, context.Canceled) {
				t.Fatalf("error = %v, want context.Canceled", err)
			}
		})
	}
}

func TestTempFileCleanupRefusesReplacementDirectory(t *testing.T) {
	runner := newRunner(t, map[string]any{"kind": kindFile})
	result, err := runner.Run(t.Context(), step.Request{})
	if err != nil {
		t.Fatal(err)
	}
	path := result.Outputs["path"].(string)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(path) })
	if err := runner.Cleanup(result); err == nil || !strings.Contains(err.Error(), "removing temporary file") {
		t.Fatalf("cleanup error = %v", err)
	}
	if info, err := os.Stat(path); err != nil || !info.IsDir() {
		t.Fatalf("replacement directory was removed: info=%v error=%v", info, err)
	}
}

func TestTempFIFOCleanupRefusesReplacement(t *testing.T) {
	tests := []struct {
		name    string
		replace func(*testing.T, string)
	}{
		{name: "file", replace: func(t *testing.T, path string) {
			if err := os.WriteFile(path, []byte("replacement"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "directory", replace: func(t *testing.T, path string) {
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "symlink", replace: func(t *testing.T, path string) {
			target := filepath.Join(filepath.Dir(path), "target")
			if err := os.WriteFile(target, []byte("target"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, path); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner := newRunner(t, map[string]any{"kind": kindFIFO})
			result, err := runner.Run(t.Context(), step.Request{})
			if err != nil {
				t.Fatal(err)
			}
			path := result.Outputs["path"].(string)
			root := filepath.Dir(path)
			t.Cleanup(func() { _ = os.RemoveAll(root) })
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			test.replace(t, path)

			if err := runner.Cleanup(result); err == nil || !strings.Contains(err.Error(), "path is no longer a FIFO") {
				t.Fatalf("cleanup error = %v", err)
			}
			if _, err := os.Lstat(path); err != nil {
				t.Fatalf("replacement was removed: %v", err)
			}
		})
	}
}

func TestTempFIFOCleanupRemovesEmptyDirectoryWhenPipeIsMissing(t *testing.T) {
	runner := newRunner(t, map[string]any{"kind": kindFIFO})
	result, err := runner.Run(t.Context(), step.Request{})
	if err != nil {
		t.Fatal(err)
	}
	path := result.Outputs["path"].(string)
	root := filepath.Dir(path)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := runner.Cleanup(result); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("managed FIFO directory remains: %v", err)
	}
}

func TestCreateFIFORollsBackPartialCreation(t *testing.T) {
	tempRoot := t.TempDir()
	if _, err := createFIFO(tempRoot, "missing/name-*"); err == nil {
		t.Fatal("createFIFO succeeded with a missing nested directory")
	}
	matches, err := filepath.Glob(filepath.Join(tempRoot, fifoRootPattern))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("partial FIFO directories remain: %#v", matches)
	}
}

func TestTempCleanupRejectsMalformedResult(t *testing.T) {
	runner := newRunner(t, map[string]any{"kind": kindFile})
	for _, result := range []step.Result{
		{},
		{Outputs: map[string]any{"path": "/tmp/example"}},
		{Outputs: map[string]any{"path": "/tmp/example", "kind": "socket"}},
		{Outputs: map[string]any{"path": "/tmp/example", "kind": kindFIFO}},
	} {
		if err := runner.Cleanup(result); err == nil {
			t.Fatalf("Cleanup(%#v) succeeded", result)
		}
	}
}

func newRunner(t *testing.T, raw map[string]any) *Runner {
	t.Helper()
	runner, err := New(raw)
	if err != nil {
		t.Fatal(err)
	}
	return runner.(*Runner)
}

var _ step.Cleaner = (*Runner)(nil)
