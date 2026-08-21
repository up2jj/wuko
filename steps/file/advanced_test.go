package file

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/up2jj/wuko/step"
	"gopkg.in/yaml.v3"
)

func TestFindFiltersAndSortsEntries(t *testing.T) {
	root := t.TempDir()
	old := filepath.Join(root, "logs", "old.log")
	newer := filepath.Join(root, "logs", "new.log")
	if err := os.MkdirAll(filepath.Dir(old), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(old, []byte("old-data"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(old, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newer, []byte("new-data"), 0o600); err != nil {
		t.Fatal(err)
	}
	oldTime := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(old, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("old.log", filepath.Join(root, "logs", "old-link")); err != nil {
		t.Fatal(err)
	}

	result := run(t, root, map[string]any{
		"operation": "find", "path": ".", "patterns": []any{"**"}, "types": []any{"file"},
		"min_size": "8B", "min_age": "24h", "mode": "0040",
	})
	entries := result.Outputs["entries"].([]any)
	if result.Outputs["count"] != 1 || entries[0].(map[string]any)["path"] != "logs/old.log" {
		t.Fatalf("outputs = %#v", result.Outputs)
	}

	all := run(t, root, map[string]any{
		"operation": "find", "path": ".", "patterns": []any{"logs", "logs/*"},
	})
	entries = all.Outputs["entries"].([]any)
	want := []string{"logs", "logs/new.log", "logs/old-link", "logs/old.log"}
	for index, expected := range want {
		if entries[index].(map[string]any)["path"] != expected {
			t.Fatalf("entries[%d] = %#v", index, entries[index])
		}
	}
}

func TestLinkReplacementPolicies(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.WriteFile(target, []byte("content"), 0o600); err != nil {
		t.Fatal(err)
	}
	symbolic := run(t, root, map[string]any{
		"operation": "link", "path": "missing-target", "destination": "symbolic", "link_type": "symbolic",
	})
	if symbolic.Outputs["replaced"] != false {
		t.Fatalf("outputs = %#v", symbolic.Outputs)
	}
	if linkTarget, err := os.Readlink(filepath.Join(root, "symbolic")); err != nil || linkTarget != filepath.Join(root, "missing-target") {
		t.Fatalf("link target = %q, error = %v", linkTarget, err)
	}

	if err := os.WriteFile(filepath.Join(root, "hard"), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	hard := run(t, root, map[string]any{
		"operation": "link", "path": "target", "destination": "hard", "link_type": "hard", "replace": "file",
	})
	if hard.Outputs["replaced"] != true {
		t.Fatalf("outputs = %#v", hard.Outputs)
	}
	if err := os.WriteFile(target, []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(root, "hard"))
	if err != nil || string(content) != "changed" {
		t.Fatalf("hard-link content = %q, error = %v", content, err)
	}
	runner, err := New(map[string]any{
		"operation": "link", "path": "symbolic", "destination": "hard-link-to-symlink", "link_type": "hard",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(t.Context(), step.Request{RunDir: root}); err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("hard link to symlink error = %v", err)
	}
}

func TestLinkReplacementRejectsRunDirectoryThroughSymlinkedParent(t *testing.T) {
	root := t.TempDir()
	runDir := filepath.Join(root, "run")
	if err := os.Mkdir(runDir, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(runDir, "marker")
	if err := os.WriteFile(marker, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Fatal(err)
	}
	runner, err := New(map[string]any{
		"operation": "link", "path": "target", "destination": filepath.Join(alias, "run"),
		"link_type": "symbolic", "replace": "any",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(t.Context(), step.Request{RunDir: runDir}); err == nil || !strings.Contains(err.Error(), "run directory") {
		t.Fatalf("error = %v", err)
	}
	if content, err := os.ReadFile(marker); err != nil || string(content) != "keep" {
		t.Fatalf("marker content = %q, error = %v", content, err)
	}
}

func TestTruncateAndTail(t *testing.T) {
	root := t.TempDir()
	filePath := filepath.Join(root, "log")
	if err := os.WriteFile(filePath, []byte("one\ntwo\nthree\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tailed := run(t, root, map[string]any{"operation": "tail", "path": "log", "lines": 2})
	if tailed.Outputs["content"] != "two\nthree\n" || tailed.Outputs["truncated"] != true {
		t.Fatalf("tail outputs = %#v", tailed.Outputs)
	}
	bytesResult := run(t, root, map[string]any{"operation": "tail", "path": "log", "bytes": "6B"})
	if bytesResult.Outputs["content"] != "three\n" || bytesResult.Outputs["size"] != int64(6) {
		t.Fatalf("byte tail outputs = %#v", bytesResult.Outputs)
	}
	truncated := run(t, root, map[string]any{"operation": "truncate", "path": "log", "size": "4B"})
	if truncated.Outputs["previous_size"] != int64(14) || truncated.Outputs["size"] != int64(4) {
		t.Fatalf("truncate outputs = %#v", truncated.Outputs)
	}
	content, err := os.ReadFile(filePath)
	if err != nil || string(content) != "one\n" {
		t.Fatalf("content = %q, error = %v", content, err)
	}
}

func TestTailBoundsLineMemory(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "large"), []byte(strings.Repeat("x", 32)), 0o600); err != nil {
		t.Fatal(err)
	}
	result := run(t, root, map[string]any{
		"operation": "tail", "path": "large", "lines": 1, "max_bytes": "8B",
	})
	if result.Outputs["content"] != "xxxxxxxx" || result.Outputs["truncated"] != true {
		t.Fatalf("outputs = %#v", result.Outputs)
	}
}

func TestDiskUsageCountsAndLargestEntries(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "small"), []byte("123"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "nested", "large"), []byte("1234567"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("small", filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	result := run(t, root, map[string]any{"operation": "disk_usage", "path": ".", "largest": 2})
	if result.Outputs["size"] != int64(10) || result.Outputs["file_count"] != 2 || result.Outputs["directory_count"] != 2 || result.Outputs["symlink_count"] != 1 {
		t.Fatalf("outputs = %#v", result.Outputs)
	}
	largest := result.Outputs["largest_entries"].([]any)
	if len(largest) != 2 || largest[0].(map[string]any)["path"] != "nested" || largest[1].(map[string]any)["path"] != "nested/large" {
		t.Fatalf("largest = %#v", largest)
	}
}

func TestAtomicSwapInstallsAndConsumesStaging(t *testing.T) {
	root := t.TempDir()
	staging := filepath.Join(root, "staging")
	destination := filepath.Join(root, "current")
	for directory, content := range map[string]string{staging: "new", destination: "old"} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(directory, "value"), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	result := run(t, root, map[string]any{
		"operation": "atomic_swap", "path": "staging", "destination": "current", "replace": "any",
	})
	if result.Outputs["replaced"] != true {
		t.Fatalf("outputs = %#v", result.Outputs)
	}
	if _, err := os.Stat(staging); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("staging error = %v", err)
	}
	content, err := os.ReadFile(filepath.Join(destination, "value"))
	if err != nil || string(content) != "new" {
		t.Fatalf("destination content = %q, error = %v", content, err)
	}

	missingDestination := filepath.Join(root, "installed")
	if err := os.Mkdir(staging, 0o700); err != nil {
		t.Fatal(err)
	}
	result = run(t, root, map[string]any{
		"operation": "atomic_swap", "path": "staging", "destination": "installed",
	})
	if result.Outputs["replaced"] != false {
		t.Fatalf("outputs = %#v", result.Outputs)
	}
	if info, err := os.Stat(missingDestination); err != nil || !info.IsDir() {
		t.Fatalf("installed info = %#v, error = %v", info, err)
	}
}

func TestAtomicSwapRejectsRunDirectoryThroughSymlinkedParent(t *testing.T) {
	root := t.TempDir()
	runDir := filepath.Join(root, "run")
	staging := filepath.Join(root, "staging")
	for _, directory := range []string{runDir, staging} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	marker := filepath.Join(runDir, "marker")
	if err := os.WriteFile(marker, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Fatal(err)
	}
	runner, err := New(map[string]any{
		"operation": "atomic_swap", "path": staging,
		"destination": filepath.Join(alias, "run"), "replace": "any",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(t.Context(), step.Request{RunDir: runDir}); err == nil || !strings.Contains(err.Error(), "run directory") {
		t.Fatalf("error = %v", err)
	}
	if content, err := os.ReadFile(marker); err != nil || string(content) != "keep" {
		t.Fatalf("marker content = %q, error = %v", content, err)
	}
}

func TestRecursivePermissionsSkipSymlinks(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "tree")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	filePath := filepath.Join(directory, "file")
	if err := os.WriteFile(filePath, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(root, "outside")
	if err := os.WriteFile(outside, []byte("x"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(directory, "link")); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(directory, 0o700)
	result := run(t, root, map[string]any{
		"operation": "permissions", "path": "tree", "mode": "0600", "recursive": true,
	})
	if result.Outputs["changed"] != 1 || result.Outputs["skipped_links"] != 1 {
		t.Fatalf("outputs = %#v", result.Outputs)
	}
	if info, err := os.Stat(outside); err != nil || info.Mode().Perm() != 0o640 {
		t.Fatalf("outside mode = %#v, error = %v", info, err)
	}
}

func TestTouchCreatesAndPreservesUnspecifiedTimestamp(t *testing.T) {
	root := t.TempDir()
	created := run(t, root, map[string]any{
		"operation": "touch", "path": "stamp", "mode": "0600",
	})
	if created.Outputs["created"] != true || created.Outputs["mode"] != "0600" {
		t.Fatalf("outputs = %#v", created.Outputs)
	}
	filePath := filepath.Join(root, "stamp")
	accessed := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
	modified := time.Date(2025, 2, 3, 4, 5, 6, 0, time.UTC)
	if err := os.Chtimes(filePath, accessed, modified); err != nil {
		t.Fatal(err)
	}
	result := run(t, root, map[string]any{
		"operation": "touch", "path": "stamp", "create": false, "modified_at": "2026-08-21T12:00:00Z",
	})
	if result.Outputs["created"] != false || result.Outputs["modified_at"] != "2026-08-21T12:00:00Z" {
		t.Fatalf("outputs = %#v", result.Outputs)
	}
	info, err := os.Stat(filePath)
	if err != nil {
		t.Fatal(err)
	}
	if delta := accessTime(info).Sub(accessed); delta < -time.Second || delta > time.Second {
		t.Fatalf("access time = %s, want %s", accessTime(info), accessed)
	}
}

func TestAdvancedOperationsRejectInvalidConfiguration(t *testing.T) {
	tests := []struct {
		name string
		raw  map[string]any
		want string
	}{
		{"find patterns", map[string]any{"operation": "find", "path": "."}, "patterns"},
		{"find type", map[string]any{"operation": "find", "path": ".", "patterns": []any{"**"}, "types": []any{"socket"}}, "types"},
		{"find range", map[string]any{"operation": "find", "path": ".", "patterns": []any{"**"}, "min_size": "2MiB", "max_size": "1MiB"}, "min_size"},
		{"link type", map[string]any{"operation": "link", "path": "x", "destination": "y", "link_type": "soft"}, "link_type"},
		{"replace", map[string]any{"operation": "link", "path": "x", "destination": "y", "link_type": "hard", "replace": "yes"}, "replace"},
		{"size unit", map[string]any{"operation": "truncate", "path": "x", "size": "1MB"}, "size"},
		{"tail selection", map[string]any{"operation": "tail", "path": "x", "lines": 1, "bytes": "1B"}, "mutually exclusive"},
		{"tail cap", map[string]any{"operation": "tail", "path": "x", "bytes": "1B", "max_bytes": "1MiB"}, "max_bytes"},
		{"largest", map[string]any{"operation": "disk_usage", "path": ".", "largest": -1}, "largest"},
		{"timestamp", map[string]any{"operation": "touch", "path": "x", "modified_at": "yesterday"}, "modified_at"},
		{"irrelevant", map[string]any{"operation": "touch", "path": "x", "recursive": true}, "not allowed"},
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

func TestDiskUsageHonorsCancellation(t *testing.T) {
	runner, err := New(map[string]any{"operation": "disk_usage", "path": "."})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := runner.Run(ctx, step.Request{RunDir: t.TempDir()}); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
}

func TestFilesystemDocumentationExamplesDecode(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "docs", "filesystem-operations.md"))
	if err != nil {
		t.Fatal(err)
	}
	blocks := regexp.MustCompile("(?s)```yaml\\n(.*?)```").FindAllSubmatch(data, -1)
	if len(blocks) == 0 {
		t.Fatal("documentation contains no YAML examples")
	}
	checked := 0
	for blockIndex, block := range blocks {
		var steps []struct {
			Type string         `yaml:"type"`
			With map[string]any `yaml:"with"`
		}
		if err := yaml.Unmarshal(block[1], &steps); err != nil {
			t.Fatalf("YAML block %d: %v", blockIndex, err)
		}
		for stepIndex, documentedStep := range steps {
			if documentedStep.Type != "file" {
				continue
			}
			checked++
			if _, err := New(documentedStep.With); err != nil {
				t.Fatalf("YAML block %d step %d: %v", blockIndex, stepIndex, err)
			}
		}
	}
	if checked < 17 {
		t.Fatalf("checked %d documented file examples, want at least 17", checked)
	}
}
