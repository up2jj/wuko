package file

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/up2jj/wuko/internal/steptest"
	"github.com/up2jj/wuko/internal/testfs"
	"github.com/up2jj/wuko/step"
)

func TestEditOperationsChangeFiles(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		initial *string
		config  map[string]any
		want    string
		action  string
	}{
		{"append", new("a\n"), map[string]any{"operation": "append", "content": "b\n"}, "a\nb\n", "appended"},
		{"append without newline", new("a"), map[string]any{"operation": "append", "content": "b\n"}, "ab\n", "appended"},
		{"append ensures newline", new("a"), map[string]any{"operation": "append", "content": "b\n", "ensure_newline": true}, "a\nb\n", "appended"},
		{"append ensures newline on empty file", new(""), map[string]any{"operation": "append", "content": "b\n", "ensure_newline": true}, "b\n", "appended"},
		{"append creates", nil, map[string]any{"operation": "append", "content": "b\n", "create": true}, "b\n", "created"},
		{"append unless contains", new("x hook y\n"), map[string]any{"operation": "append", "content": "b\n", "unless_contains": "hook"}, "x hook y\n", "none"},
		{"append unless matches per line", new("one\n  hook\n"), map[string]any{"operation": "append", "content": "b\n", "unless_matches": `^\s+hook$`}, "one\n  hook\n", "none"},
		{"append unless matches misses", new("one\n"), map[string]any{"operation": "append", "content": "b\n", "unless_matches": "^hook"}, "one\nb\n", "appended"},
		{"line present", new("a\nline\n"), map[string]any{"operation": "ensure_line", "line": "line"}, "a\nline\n", "none"},
		{"line inserted", new("a"), map[string]any{"operation": "ensure_line", "line": "line"}, "a\nline\n", "inserted"},
		{"line created", nil, map[string]any{"operation": "ensure_line", "line": "line", "create": true}, "line\n", "created"},
		{"line present_if", new("plugins=(git direnv)\n"), map[string]any{"operation": "ensure_line", "line": "line", "present_if": `^[^#]*plugins=\([^)]*\bdirenv\b`}, "plugins=(git direnv)\n", "none"},
		{"line present_if ignores comment", new("# plugins=(direnv)\n"), map[string]any{"operation": "ensure_line", "line": "line", "present_if": `^[^#]*plugins=\([^)]*\bdirenv\b`}, "# plugins=(direnv)\nline\n", "inserted"},
		{"line replaces last match", new("v=1\nx\nv=2\n"), map[string]any{"operation": "ensure_line", "line": "v=3", "match": "^v="}, "v=1\nx\nv=3\n", "replaced"},
		{"line replace keeps crlf", new("v=1\r\nx\r\n"), map[string]any{"operation": "ensure_line", "line": "v=3", "match": "^v="}, "v=3\r\nx\r\n", "replaced"},
		{"line present with crlf", new("line\r\n"), map[string]any{"operation": "ensure_line", "line": "line"}, "line\r\n", "none"},
		{"block inserted", new("a\n"), map[string]any{"operation": "ensure_block", "content": "x\ny"}, "a\n# BEGIN WUKO MANAGED BLOCK\nx\ny\n# END WUKO MANAGED BLOCK\n", "inserted"},
		{"block unchanged", new("a\n# BEGIN WUKO MANAGED BLOCK\nx\n# END WUKO MANAGED BLOCK\nb\n"), map[string]any{"operation": "ensure_block", "content": "x\n"}, "a\n# BEGIN WUKO MANAGED BLOCK\nx\n# END WUKO MANAGED BLOCK\nb\n", "none"},
		{"block replaced", new("a\n# BEGIN WUKO MANAGED BLOCK\nold\n# END WUKO MANAGED BLOCK\nb\n"), map[string]any{"operation": "ensure_block", "content": "new\n"}, "a\n# BEGIN WUKO MANAGED BLOCK\nnew\n# END WUKO MANAGED BLOCK\nb\n", "replaced"},
		{"block custom marker", new(""), map[string]any{"operation": "ensure_block", "content": "x", "marker": "// {mark} tool"}, "// BEGIN tool\nx\n// END tool\n", "inserted"},
		{"block replaced before present_if", new("# BEGIN WUKO MANAGED BLOCK\nhook 1\n# END WUKO MANAGED BLOCK\n"), map[string]any{"operation": "ensure_block", "content": "hook 2", "present_if": "hook"}, "# BEGIN WUKO MANAGED BLOCK\nhook 2\n# END WUKO MANAGED BLOCK\n", "replaced"},
		{"block present_if", new("hook\n"), map[string]any{"operation": "ensure_block", "content": "x", "present_if": "hook"}, "hook\n", "none"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			path := filepath.Join(root, "rc")
			if test.initial != nil {
				testfs.Write(t, path, *test.initial)
			}
			test.config["path"] = "rc"
			result := run(t, root, test.config)
			if result.Outputs["action"] != test.action {
				t.Fatalf("action = %v, want %s", result.Outputs["action"], test.action)
			}
			if result.Outputs["changed"] != (test.action != "none") || result.Outputs["created"] != (test.action == "created") {
				t.Fatalf("outputs = %#v", result.Outputs)
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(test.want, string(got)); diff != "" {
				t.Fatalf("content mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestEditOperationsAreIdempotent(t *testing.T) {
	t.Parallel()
	for _, config := range []map[string]any{
		{"operation": "append", "path": "rc", "content": "hook\n", "unless_contains": "hook", "create": true},
		{"operation": "ensure_line", "path": "rc", "line": "hook", "create": true},
		{"operation": "ensure_block", "path": "rc", "content": "hook", "create": true},
	} {
		t.Run(config["operation"].(string), func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			if first := run(t, root, config); first.Outputs["changed"] != true {
				t.Fatalf("first outputs = %#v", first.Outputs)
			}
			second := run(t, root, config)
			if second.Outputs["changed"] != false || second.Outputs["matched_path"] != filepath.Join(root, "rc") {
				t.Fatalf("second outputs = %#v", second.Outputs)
			}
		})
	}
}

func TestEditAppendReportsBytesAndKeepsMode(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := filepath.Join(root, "rc")
	testfs.WriteFile(t, path, []byte("a"), 0o600)
	result := run(t, root, map[string]any{"operation": "append", "path": "rc", "content": "b\n", "ensure_newline": true, "mode": "0640"})
	want := map[string]any{
		"path": path, "matched_path": "", "exists": true, "action": "appended",
		"would_change": true, "changed": true, "created": false, "bytes": int64(3),
	}
	if diff := cmp.Diff(want, result.Outputs); diff != "" {
		t.Fatalf("outputs mismatch (-want +got):\n%s", diff)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, error = %v", info.Mode(), err)
	}
}

func TestEditCreatesWithMode(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	run(t, root, map[string]any{"operation": "ensure_line", "path": "rc", "line": "x", "create": true, "mode": "0600"})
	if info, err := os.Stat(filepath.Join(root, "rc")); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, error = %v", info.Mode(), err)
	}
}

func TestEditWritesThroughSymbolicLinks(t *testing.T) {
	t.Parallel()
	for _, config := range []map[string]any{
		{"operation": "append", "path": "link.rc", "content": "b\n"},
		{"operation": "ensure_line", "path": "link.rc", "line": "v=2", "match": "^v="},
		{"operation": "ensure_block", "path": "link.rc", "content": "b"},
	} {
		t.Run(config["operation"].(string), func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			real := filepath.Join(root, "real.rc")
			testfs.WriteFile(t, real, []byte("v=1\n"), 0o600)
			if err := os.Symlink("real.rc", filepath.Join(root, "link.rc")); err != nil {
				t.Fatal(err)
			}
			if result := run(t, root, config); result.Outputs["changed"] != true {
				t.Fatalf("outputs = %#v", result.Outputs)
			}
			info, err := os.Lstat(filepath.Join(root, "link.rc"))
			if err != nil || info.Mode()&os.ModeSymlink == 0 {
				t.Fatalf("link replaced: %v, error = %v", info.Mode(), err)
			}
			content, err := os.ReadFile(real)
			if err != nil || string(content) == "v=1\n" {
				t.Fatalf("target content = %q, error = %v", content, err)
			}
			if info, err := os.Stat(real); err != nil || info.Mode().Perm() != 0o600 {
				t.Fatalf("target mode = %v, error = %v", info.Mode(), err)
			}
		})
	}
}

func TestEditSearchesPaths(t *testing.T) {
	t.Parallel()
	paths := []any{"missing-dir/.zshrc", ".zshrc", ".zprofile", "file/child"}
	t.Run("match in a later file", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		testfs.Write(t, filepath.Join(root, ".zprofile"), "eval hook\n")
		testfs.Write(t, filepath.Join(root, "file"), "")
		result := run(t, root, map[string]any{"operation": "ensure_line", "paths": paths, "line": "x", "present_if": "hook"})
		if result.Outputs["changed"] != false || result.Outputs["matched_path"] != filepath.Join(root, ".zprofile") {
			t.Fatalf("outputs = %#v", result.Outputs)
		}
	})
	t.Run("insert into the first file", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		testfs.Write(t, filepath.Join(root, ".zprofile"), "other\n")
		config := map[string]any{"operation": "ensure_line", "paths": []any{".zshrc", ".zprofile"}, "line": "hook", "create": true}
		result := run(t, root, config)
		if result.Outputs["path"] != filepath.Join(root, ".zshrc") || result.Outputs["created"] != true {
			t.Fatalf("outputs = %#v", result.Outputs)
		}
		if content, _ := os.ReadFile(filepath.Join(root, ".zprofile")); string(content) != "other\n" {
			t.Fatalf(".zprofile = %q", content)
		}
	})
	t.Run("replace where the match is", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		testfs.Write(t, filepath.Join(root, ".zshrc"), "a\n")
		testfs.Write(t, filepath.Join(root, ".zprofile"), "v=1\n")
		result := run(t, root, map[string]any{"operation": "ensure_line", "paths": []any{".zshrc", ".zprofile"}, "line": "v=2", "match": "^v="})
		if result.Outputs["path"] != filepath.Join(root, ".zprofile") || result.Outputs["action"] != "replaced" {
			t.Fatalf("outputs = %#v", result.Outputs)
		}
	})
	t.Run("missing first file without create", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		runner := steptest.Build(t, New, map[string]any{"operation": "append", "paths": []any{".zshrc"}, "content": "x", "check": true})
		if _, err := runner.Run(t.Context(), step.Request{RunDir: root}); err == nil || !strings.Contains(err.Error(), "set create to true") {
			t.Fatalf("error = %v", err)
		}
	})
}

func TestEditCheckWritesNothing(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	config := map[string]any{"operation": "ensure_line", "paths": []any{"a.rc", "b.rc"}, "line": "hook", "create": true, "check": true}
	result := run(t, root, config)
	want := map[string]any{
		"path": filepath.Join(root, "a.rc"), "matched_path": "", "exists": false, "action": "created",
		"would_change": true, "changed": false, "created": false, "bytes": int64(0),
	}
	if diff := cmp.Diff(want, result.Outputs); diff != "" {
		t.Fatalf("check outputs mismatch (-want +got):\n%s", diff)
	}
	if _, err := os.Stat(filepath.Join(root, "a.rc")); !os.IsNotExist(err) {
		t.Fatalf("check created a file: %v", err)
	}
	config["check"] = false
	if result := run(t, root, config); result.Outputs["path"] != want["path"] || result.Outputs["changed"] != true {
		t.Fatalf("write outputs = %#v", result.Outputs)
	}
}

func TestEditRejectsAHalfBlock(t *testing.T) {
	t.Parallel()
	for name, content := range map[string]string{
		"begin only": "# BEGIN WUKO MANAGED BLOCK\nx\n",
		"end first":  "# END WUKO MANAGED BLOCK\n# BEGIN WUKO MANAGED BLOCK\n",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			testfs.Write(t, filepath.Join(root, "rc"), content)
			runner := steptest.Build(t, New, map[string]any{"operation": "ensure_block", "path": "rc", "content": "x"})
			if _, err := runner.Run(t.Context(), step.Request{RunDir: root}); err == nil || !strings.Contains(err.Error(), "marker") {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestEditRejectsADirectory(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	runner := steptest.Build(t, New, map[string]any{"operation": "append", "path": ".", "content": "x"})
	if _, err := runner.Run(t.Context(), step.Request{RunDir: root}); err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("error = %v", err)
	}
}

func TestEditValidation(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		config map[string]any
		want   string
	}{
		"no path":             {map[string]any{"operation": "append", "content": "x"}, "path is required"},
		"path and paths":      {map[string]any{"operation": "append", "path": "a", "paths": []any{"b"}, "content": "x"}, "mutually exclusive"},
		"empty paths":         {map[string]any{"operation": "append", "paths": []any{}, "content": "x"}, "at least one"},
		"empty paths entry":   {map[string]any{"operation": "append", "paths": []any{""}, "content": "x"}, "paths[0]"},
		"paths elsewhere":     {map[string]any{"operation": "read", "paths": []any{"a"}}, "paths is not allowed"},
		"append no content":   {map[string]any{"operation": "append", "path": "a"}, "content is required"},
		"line missing":        {map[string]any{"operation": "ensure_line", "path": "a"}, "line is required"},
		"line break":          {map[string]any{"operation": "ensure_line", "path": "a", "line": "x\ny"}, "line break"},
		"bad regex":           {map[string]any{"operation": "ensure_line", "path": "a", "line": "x", "match": "("}, "match"},
		"bad unless_matches":  {map[string]any{"operation": "append", "path": "a", "content": "x", "unless_matches": "["}, "unless_matches"},
		"marker without mark": {map[string]any{"operation": "ensure_block", "path": "a", "content": "x", "marker": "# managed"}, "{mark}"},
		"append-only field":   {map[string]any{"operation": "ensure_line", "path": "a", "line": "x", "unless_contains": "x"}, "unless_contains is not allowed"},
		"block-only field":    {map[string]any{"operation": "append", "path": "a", "content": "x", "marker": "{mark}"}, "marker is not allowed"},
		"bad mode":            {map[string]any{"operation": "append", "path": "a", "content": "x", "mode": "644"}, "mode"},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := New(test.config); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("New error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestEditAcceptsTemplatedFields(t *testing.T) {
	t.Parallel()
	steptest.Build(t, New, map[string]any{
		"operation": "ensure_block", "paths": []any{"{{ .vars.rc }}"}, "content": "x",
		"marker": "{{ .vars.marker }}", "present_if": "{{ .vars.pattern }}",
	})
}
