package cache

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/up2jj/wuko/step"
)

func TestConfigValidation(t *testing.T) {
	tests := []struct {
		name string
		raw  map[string]any
		want string
	}{
		{name: "valid restore", raw: cacheConfig("restore"), want: ""},
		{name: "valid save", raw: cacheConfig("save"), want: ""},
		{name: "templated", raw: map[string]any{"operation": "{{ .vars.operation }}", "cache_dir": "{{ .vars.cache_dir }}", "key_files": []any{"{{ .vars.lockfile }}"}, "paths": []any{"{{ .vars.target }}"}}, want: ""},
		{name: "missing operation", raw: without(cacheConfig("save"), "operation"), want: "operation is required"},
		{name: "bad operation", raw: cacheConfig("clear"), want: "operation must be"},
		{name: "missing cache dir", raw: without(cacheConfig("save"), "cache_dir"), want: "cache_dir is required"},
		{name: "missing key files", raw: without(cacheConfig("save"), "key_files"), want: "key_files must contain"},
		{name: "empty key file", raw: map[string]any{"operation": "save", "cache_dir": "store", "key_files": []any{""}, "paths": []any{"target"}}, want: "key_files[0]"},
		{name: "missing paths", raw: without(cacheConfig("save"), "paths"), want: "paths must contain"},
		{name: "empty path", raw: map[string]any{"operation": "save", "cache_dir": "store", "key_files": []any{"go.sum"}, "paths": []any{""}}, want: "paths[0]"},
		{name: "unknown field", raw: map[string]any{"operation": "save", "cache_dir": "store", "key_files": []any{"go.sum"}, "paths": []any{"target"}, "extra": true}, want: "field extra not found"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := New(test.raw)
			if test.want == "" && err != nil {
				t.Fatal(err)
			}
			if test.want != "" && (err == nil || !strings.Contains(err.Error(), test.want)) {
				t.Fatalf("error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestContentDerivedKeyIsStableAndInvalidates(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "go.mod"), "module example\n", 0o644)
	writeFile(t, filepath.Join(root, "go.sum"), "checksum\n", 0o644)

	first := keyFor(t, root, []string{"go.sum", "go.mod"}, []string{"vendor", ".cache"})
	reordered := keyFor(t, root, []string{"go.mod", "go.sum"}, []string{".cache", "vendor"})
	if first != reordered {
		t.Fatalf("keys differ after declaration reordering: %s != %s", first, reordered)
	}
	writeFile(t, filepath.Join(root, "go.sum"), "changed\n", 0o644)
	changedContent := keyFor(t, root, []string{"go.mod", "go.sum"}, []string{".cache", "vendor"})
	if changedContent == first {
		t.Fatal("key did not change with key-file content")
	}
	changedTarget := keyFor(t, root, []string{"go.mod", "go.sum"}, []string{".cache", "build"})
	if changedTarget == changedContent {
		t.Fatal("key did not change with target paths")
	}
}

func TestSaveAndRestoreMultipleDirectoriesExactly(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "go.sum"), "checksum\n", 0o644)
	first := filepath.Join(root, "vendor")
	second := filepath.Join(root, "build")
	writeFile(t, filepath.Join(first, "module.txt"), "dependency", 0o640)
	if err := os.MkdirAll(filepath.Join(first, "empty"), 0o750); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(second, "nested", "artifact"), "binary", 0o750)
	if err := os.Symlink("artifact", filepath.Join(second, "nested", "artifact-link")); err != nil {
		t.Fatal(err)
	}
	modTime := time.Unix(1_700_000_000, 0)
	if err := os.Chtimes(filepath.Join(first, "module.txt"), modTime, modTime); err != nil {
		t.Fatal(err)
	}

	runner := newRunner(t, root, "save")
	saved, err := runner.Run(t.Context(), step.Request{RunDir: root})
	if err != nil {
		t.Fatal(err)
	}
	if saved.Outputs["stored"] != true || saved.Outputs["size"].(int64) <= 0 {
		t.Fatalf("save outputs = %#v", saved.Outputs)
	}
	key := saved.Outputs["key"]

	if err := os.RemoveAll(first); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(first, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(first, "stale.txt"), "stale", 0o644)
	if err := os.RemoveAll(second); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(second, 0o755); err != nil {
		t.Fatal(err)
	}

	restoreRunner := newRunner(t, root, "restore")
	restored, err := restoreRunner.Run(t.Context(), step.Request{RunDir: root})
	if err != nil {
		t.Fatal(err)
	}
	if restored.Outputs["hit"] != true || restored.Outputs["key"] != key {
		t.Fatalf("restore outputs = %#v", restored.Outputs)
	}
	if _, err := os.Stat(filepath.Join(first, "stale.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale file survived restore: %v", err)
	}
	assertFile(t, filepath.Join(first, "module.txt"), "dependency", 0o640)
	assertFile(t, filepath.Join(second, "nested", "artifact"), "binary", 0o750)
	if info, err := os.Stat(filepath.Join(first, "module.txt")); err != nil || !info.ModTime().Equal(modTime) {
		t.Fatalf("restored modtime = %v, error = %v", info.ModTime(), err)
	}
	if info, err := os.Stat(filepath.Join(first, "empty")); err != nil || !info.IsDir() || info.Mode().Perm() != 0o750 {
		t.Fatalf("empty directory metadata = %v, error = %v", info, err)
	}
	if target, err := os.Readlink(filepath.Join(second, "nested", "artifact-link")); err != nil || target != "artifact" {
		t.Fatalf("restored symlink = %q, error = %v", target, err)
	}
}

func TestRestoreMissAndImmutableSave(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "go.sum"), "checksum\n", 0o644)
	writeFile(t, filepath.Join(root, "vendor", "value"), "first", 0o644)
	if err := os.MkdirAll(filepath.Join(root, "build"), 0o755); err != nil {
		t.Fatal(err)
	}

	miss, err := newRunner(t, root, "restore").Run(t.Context(), step.Request{RunDir: root})
	if err != nil || miss.Outputs["hit"] != false {
		t.Fatalf("miss outputs = %#v, error = %v", miss.Outputs, err)
	}
	first, err := newRunner(t, root, "save").Run(t.Context(), step.Request{RunDir: root})
	if err != nil || first.Outputs["stored"] != true {
		t.Fatalf("first save outputs = %#v, error = %v", first.Outputs, err)
	}
	writeFile(t, filepath.Join(root, "vendor", "value"), "second", 0o644)
	second, err := newRunner(t, root, "save").Run(t.Context(), step.Request{RunDir: root})
	if err != nil || second.Outputs["stored"] != false {
		t.Fatalf("second save outputs = %#v, error = %v", second.Outputs, err)
	}
	if _, err := newRunner(t, root, "restore").Run(t.Context(), step.Request{RunDir: root}); err != nil {
		t.Fatal(err)
	}
	assertFile(t, filepath.Join(root, "vendor", "value"), "first", 0o644)
}

func TestConcurrentSaveInstallsOneImmutableEntry(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "go.sum"), "checksum\n", 0o644)
	writeFile(t, filepath.Join(root, "vendor", "value"), "content", 0o644)
	if err := os.MkdirAll(filepath.Join(root, "build"), 0o755); err != nil {
		t.Fatal(err)
	}

	var wait sync.WaitGroup
	results := make(chan step.Result, 2)
	errorsChannel := make(chan error, 2)
	for range 2 {
		wait.Go(func() {
			result, err := newRunner(t, root, "save").Run(t.Context(), step.Request{RunDir: root})
			results <- result
			errorsChannel <- err
		})
	}
	wait.Wait()
	close(results)
	close(errorsChannel)
	for err := range errorsChannel {
		if err != nil {
			t.Fatal(err)
		}
	}
	stored := 0
	for result := range results {
		if result.Outputs["stored"] == true {
			stored++
		}
	}
	if stored != 1 {
		t.Fatalf("stored count = %d, want 1", stored)
	}
}

func TestRejectsMissingAndConflictingPaths(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "go.sum"), "checksum\n", 0o644)
	writeFile(t, filepath.Join(root, "not-directory"), "file", 0o644)
	if err := os.MkdirAll(filepath.Join(root, "target"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "target"), filepath.Join(root, "target-alias")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(root, filepath.Join(root, "root-alias")); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		raw  map[string]any
		want string
	}{
		{name: "missing key file", raw: map[string]any{"operation": "save", "cache_dir": "store", "key_files": []any{"missing"}, "paths": []any{"not-directory"}}, want: "inspecting key file"},
		{name: "target is file", raw: map[string]any{"operation": "save", "cache_dir": "store", "key_files": []any{"go.sum"}, "paths": []any{"not-directory"}}, want: "must be a directory"},
		{name: "missing restore target", raw: map[string]any{"operation": "restore", "cache_dir": "store", "key_files": []any{"go.sum"}, "paths": []any{"missing-target"}}, want: "inspecting restore path"},
		{name: "duplicate key file", raw: map[string]any{"operation": "save", "cache_dir": "store", "key_files": []any{"go.sum", "./go.sum"}, "paths": []any{"not-directory"}}, want: "duplicate path"},
		{name: "overlapping targets", raw: map[string]any{"operation": "save", "cache_dir": "store", "key_files": []any{"go.sum"}, "paths": []any{"target", "target/nested"}}, want: "overlap"},
		{name: "cache within target", raw: map[string]any{"operation": "save", "cache_dir": "target/store", "key_files": []any{"go.sum"}, "paths": []any{"target"}}, want: "overlap"},
		{name: "cache aliases target", raw: map[string]any{"operation": "save", "cache_dir": "target-alias/store", "key_files": []any{"go.sum"}, "paths": []any{"target"}}, want: "overlap"},
		{name: "targets alias through parent", raw: map[string]any{"operation": "save", "cache_dir": "store", "key_files": []any{"go.sum"}, "paths": []any{"target", "root-alias/target"}}, want: "overlap"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner, err := New(test.raw)
			if err == nil {
				_, err = runner.Run(t.Context(), step.Request{RunDir: root})
			}
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestRejectsCorruptAndHostileArchivesWithoutReplacingTargets(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "go.sum"), "checksum\n", 0o644)
	writeFile(t, filepath.Join(root, "vendor", "sentinel"), "original", 0o644)
	if err := os.MkdirAll(filepath.Join(root, "build"), 0o755); err != nil {
		t.Fatal(err)
	}
	runner := newRunner(t, root, "restore")
	config, err := resolveConfig(root, runner.(*Runner).config)
	if err != nil {
		t.Fatal(err)
	}
	key, err := deriveKey(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	archivePath := filepath.Join(root, "store", key+".tar.gz")
	if err := os.MkdirAll(filepath.Dir(archivePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(archivePath, []byte("not gzip"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(t.Context(), step.Request{RunDir: root}); err == nil || !strings.Contains(err.Error(), "compressed") {
		t.Fatalf("corrupt archive error = %v", err)
	}
	assertFile(t, filepath.Join(root, "vendor", "sentinel"), "original", 0o644)

	writeArchive(t, archivePath, []*tar.Header{{Name: "../escape", Typeflag: tar.TypeDir, Mode: 0o755}})
	if _, err := runner.Run(t.Context(), step.Request{RunDir: root}); err == nil || !strings.Contains(err.Error(), "invalid path") {
		t.Fatalf("hostile archive error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "escape")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("archive escaped staging: %v", err)
	}
	assertFile(t, filepath.Join(root, "vendor", "sentinel"), "original", 0o644)

	writeArchive(t, archivePath, []*tar.Header{
		{Name: "0", Typeflag: tar.TypeDir, Mode: 0o755},
		{Name: "0/link", Typeflag: tar.TypeSymlink, Linkname: "../../escape", Mode: 0o777},
	})
	if _, err := runner.Run(t.Context(), step.Request{RunDir: root}); err == nil || !strings.Contains(err.Error(), "escapes") {
		t.Fatalf("escaping symlink error = %v", err)
	}
	assertFile(t, filepath.Join(root, "vendor", "sentinel"), "original", 0o644)
}

func TestCommitRestoreRollsBackEarlierTargets(t *testing.T) {
	root := t.TempDir()
	firstTarget := filepath.Join(root, "first")
	secondTarget := filepath.Join(root, "second")
	firstStage := filepath.Join(root, "first-stage")
	writeFile(t, filepath.Join(firstTarget, "value"), "old-first", 0o644)
	writeFile(t, filepath.Join(secondTarget, "value"), "old-second", 0o644)
	writeFile(t, filepath.Join(firstStage, "value"), "new-first", 0o644)
	missingStage := filepath.Join(root, "missing-stage")

	err := commitRestore([]stagedTarget{
		{target: firstTarget, stage: firstStage},
		{target: secondTarget, stage: missingStage},
	})
	if err == nil || !strings.Contains(err.Error(), "installing restored path") {
		t.Fatalf("commit error = %v", err)
	}
	assertFile(t, filepath.Join(firstTarget, "value"), "old-first", 0o644)
	assertFile(t, filepath.Join(secondTarget, "value"), "old-second", 0o644)
}

func TestCleanupStagedPreservesRollbackBackup(t *testing.T) {
	root := t.TempDir()
	stage := filepath.Join(root, "stage")
	backup := filepath.Join(root, "backup")
	writeFile(t, filepath.Join(stage, "new"), "new", 0o644)
	writeFile(t, filepath.Join(backup, "original"), "original", 0o644)

	cleanupStaged([]stagedTarget{{stage: stage, backup: backup}})

	if _, err := os.Stat(stage); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("staging directory survived cleanup: %v", err)
	}
	assertFile(t, filepath.Join(backup, "original"), "original", 0o644)
}

func TestCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	runner, err := New(cacheConfig("save"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(ctx, step.Request{RunDir: t.TempDir()}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error = %v", err)
	}
}

func newRunner(t *testing.T, root, operation string) step.Runner {
	t.Helper()
	runner, err := New(map[string]any{
		"operation": operation,
		"cache_dir": "store",
		"key_files": []any{"go.sum"},
		"paths":     []any{"vendor", "build"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return runner
}

func keyFor(t *testing.T, root string, keyFiles, paths []string) string {
	t.Helper()
	config, err := resolveConfig(root, Config{Operation: "save", CacheDir: "store", KeyFiles: keyFiles, Paths: paths})
	if err != nil {
		t.Fatal(err)
	}
	key, err := deriveKey(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func cacheConfig(operation string) map[string]any {
	return map[string]any{"operation": operation, "cache_dir": "store", "key_files": []any{"go.sum"}, "paths": []any{"vendor"}}
}

func without(raw map[string]any, key string) map[string]any {
	delete(raw, key)
	return raw
}

func writeFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func assertFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != content {
		t.Fatalf("%s content = %q, want %q", path, data, content)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != mode {
		t.Fatalf("%s mode = %04o, want %04o", path, info.Mode().Perm(), mode)
	}
}

func writeArchive(t *testing.T, archivePath string, headers []*tar.Header) {
	t.Helper()
	output, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	gzipWriter := gzip.NewWriter(output)
	tarWriter := tar.NewWriter(gzipWriter)
	for _, header := range headers {
		if err := tarWriter.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := output.Close(); err != nil {
		t.Fatal(err)
	}
}
