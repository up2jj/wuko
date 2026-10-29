package text

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

func TestTrimTrailingWhitespace(t *testing.T) {
	t.Parallel()
	for _, test := range []struct{ name, input, want string }{
		{"spaces and tabs", "hello \t\nworld\t", "hello\nworld"},
		{"keeps CRLF", "hello \t\r\nworld\r\n", "hello\r\nworld\r\n"},
		{"ASCII whitespace", "line\v\f \t\r\n", "line\r\n"},
		{"stray carriage return", "line\r\r\n", "line\r\n"},
		{"final carriage return", "line\r", "line"},
		{"unicode spaces stay", "line \n", "line \n"},
		{"clean", "a\nb\n", "a\nb\n"},
		{"empty", "", ""},
	} {
		if got := string(trimTrailingWhitespace([]byte(test.input), false)); got != test.want {
			t.Errorf("%s: got %q, want %q", test.name, got, test.want)
		}
	}
}

func TestNormalizeFinalNewline(t *testing.T) {
	t.Parallel()
	for _, test := range []struct{ name, input, want string }{
		{"missing", "hello", "hello\n"},
		{"multiple", "hello\r\n\n\r\n", "hello\r\n"},
		{"only newlines", "\r\n\n", ""},
		{"empty", "", ""},
		{"lone carriage return", "hello\r", "hello\r"},
		{"one newline", "hello\n", "hello\n"},
	} {
		if got := string(normalizeFinalNewline([]byte(test.input))); got != test.want {
			t.Errorf("%s: got %q, want %q", test.name, got, test.want)
		}
	}
}

func TestTextFixRewritesChangedFilesAndFails(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	testfs.WriteTree(t, dir, map[string]string{"dirty.txt": "a \nb", "clean.txt": "ok\n", "binary.dat": "a \x00b"})
	if err := os.Chmod(filepath.Join(dir, "dirty.txt"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("dirty.txt", filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	runner := steptest.Build(t, NewFix, map[string]any{
		"files": []any{"dirty.txt", "clean.txt", "binary.dat", "link"}, "trailing_whitespace": true, "final_newline": true,
	})
	result, err := runner.Run(t.Context(), step.Request{RunDir: dir})
	if err == nil || !strings.Contains(err.Error(), "fixed 1 file(s); review the changes: dirty.txt") {
		t.Fatalf("error = %v", err)
	}
	if diff := cmp.Diff(map[string]any{"changed": []any{"dirty.txt"}, "count": 1, "checked": 4}, result.Outputs); diff != "" {
		t.Fatalf("outputs mismatch (-want +got):\n%s", diff)
	}
	for name, want := range map[string]string{"dirty.txt": "a\nb\n", "binary.dat": "a \x00b"} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || string(data) != want {
			t.Fatalf("%s = %q, %v; want %q", name, data, err, want)
		}
	}
	if info, err := os.Lstat(filepath.Join(dir, "dirty.txt")); err != nil || info.Mode().Perm() != 0o750 {
		t.Fatalf("mode = %v, %v; want 0750 kept", info.Mode(), err)
	}
	if info, err := os.Lstat(filepath.Join(dir, "link")); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("symlink was replaced: %v, %v", info.Mode(), err)
	}
}

func TestTextFixCheckAndReportModes(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	testfs.Write(t, filepath.Join(dir, "dirty.txt"), "a \n")
	check := steptest.Build(t, NewFix, map[string]any{"files": []any{"dirty.txt"}, "trailing_whitespace": true, "check": true})
	if _, err := check.Run(t.Context(), step.Request{RunDir: dir}); err == nil || !strings.Contains(err.Error(), "1 file(s) need fixing: dirty.txt") {
		t.Fatalf("check error = %v", err)
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "dirty.txt")); string(data) != "a \n" {
		t.Fatalf("check mode wrote %q", data)
	}
	report := steptest.Build(t, NewFix, map[string]any{
		"files": map[string]any{"expr": "vars.files"}, "final_newline": true, "trailing_whitespace": true, "fail_on_change": false,
	})
	result, err := report.Run(t.Context(), step.Request{RunDir: dir, Vars: map[string]any{"files": []any{"dirty.txt"}}})
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([]any{"dirty.txt"}, result.Outputs["changed"]); diff != "" {
		t.Fatalf("changed mismatch (-want +got):\n%s", diff)
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "dirty.txt")); string(data) != "a\n" {
		t.Fatalf("report mode left %q", data)
	}
}

func TestNewFixValidatesConfiguration(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		raw  map[string]any
		want string
	}{
		{"no rule", map[string]any{"files": []any{"a"}}, "enable at least one"},
		{"missing files", map[string]any{"final_newline": true}, "files is required"},
		{"blank path", map[string]any{"files": []any{" "}, "final_newline": true}, "files[0]"},
		{"bad expr", map[string]any{"files": map[string]any{"expr": "("}, "final_newline": true}, "compiling files expr"},
		{"extra expr field", map[string]any{"files": map[string]any{"expr": "vars.a", "x": 1}, "final_newline": true}, "exactly a non-empty expr"},
		{"unknown field", map[string]any{"files": []any{"a"}, "final_newline": true, "mode": "x"}, "field mode"},
	} {
		if _, err := NewFix(test.raw); err == nil || !strings.Contains(err.Error(), test.want) {
			t.Errorf("%s: error = %v, want %q", test.name, err, test.want)
		}
	}
	runner := steptest.Build(t, NewFix, map[string]any{"files": map[string]any{"expr": "vars.files"}, "final_newline": true})
	if _, err := runner.Run(t.Context(), step.Request{Vars: map[string]any{"files": "a.txt"}}); err == nil || !strings.Contains(err.Error(), "must return a list") {
		t.Fatalf("non-list expr error = %v", err)
	}
}

func TestConvertLineEndings(t *testing.T) {
	t.Parallel()
	for _, test := range []struct{ name, mode, input, want string }{
		{"to lf", "lf", "a\r\nb\rc\nd", "a\nb\nc\nd"},
		{"to crlf", "crlf", "a\nb\r\nc\r", "a\r\nb\r\nc\r\n"},
		{"auto majority crlf", "auto", "a\r\nb\r\nc\n", "a\r\nb\r\nc\r\n"},
		{"auto tie prefers lf", "auto", "a\r\nb\n", "a\nb\n"},
		{"auto lone cr majority", "auto", "a\rb\rc\n", "a\rb\rc\r"},
		{"already lf", "lf", "a\nb\n", "a\nb\n"},
		{"no endings", "crlf", "abc", "abc"},
	} {
		if got := string(convertLineEndings([]byte(test.input), test.mode)); got != test.want {
			t.Errorf("%s: got %q, want %q", test.name, got, test.want)
		}
	}
}

func TestCollapseBlankLines(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name  string
		limit int
		input string
		want  string
	}{
		{"collapses runs", 1, "a\n\n\n\nb\n", "a\n\nb\n"},
		{"whitespace-only lines are blank", 1, "a\n \n\t\r\n\nb", "a\n \nb"},
		{"leading run", 1, "\n\n\na\n", "\na\n"},
		{"zero removes blank lines", 0, "a\n\nb\n\n", "a\nb\n"},
		{"within limit", 2, "a\n\n\nb\n", "a\n\n\nb\n"},
	} {
		if got := string(collapseBlankLines([]byte(test.input), test.limit)); got != test.want {
			t.Errorf("%s: got %q, want %q", test.name, got, test.want)
		}
	}
}

func TestTrimTrailingWhitespaceKeepsMarkdownLineBreaks(t *testing.T) {
	t.Parallel()
	for _, test := range []struct{ name, input, want string }{
		{"two spaces kept", "line  \nnext\n", "line  \nnext\n"},
		{"extra spaces trimmed to two", "line    \r\n", "line  \r\n"},
		{"one space removed", "line \n", "line\n"},
		{"tab breaks the rule", "line  \t\n", "line\n"},
		{"blank line emptied", "    \n", "\n"},
	} {
		if got := string(trimTrailingWhitespace([]byte(test.input), true)); got != test.want {
			t.Errorf("%s: got %q, want %q", test.name, got, test.want)
		}
	}
}

func TestTextFixAppliesEveryRuleInOrder(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	testfs.WriteTree(t, dir, map[string]string{
		"notes.md":  "\xef\xbb\xbfTitle  \r\nbreak   \r\n\r\n\r\n\r\nend \r\n\r\n",
		"plain.txt": "\xef\xbb\xbfTitle  \r\n\r\n\r\n\r\nend",
	})
	runner := steptest.Build(t, NewFix, map[string]any{
		"files": []any{"notes.md", "plain.txt"}, "remove_byte_order_mark": true, "line_endings": "lf",
		"trailing_whitespace": true, "keep_markdown_line_breaks": true, "max_blank_lines": 1, "final_newline": true,
		"fail_on_change": false,
	})
	if _, err := runner.Run(t.Context(), step.Request{RunDir: dir}); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"notes.md": "Title  \nbreak  \n\nend\n", "plain.txt": "Title\n\nend\n"} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || string(data) != want {
			t.Errorf("%s = %q, %v; want %q", name, data, err, want)
		}
	}
}

func TestNewFixValidatesNewRules(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		raw  map[string]any
		want string
	}{
		{"bad line endings", map[string]any{"files": []any{"a"}, "line_endings": "cr"}, "line_endings must be lf, crlf, or auto"},
		{"markdown without whitespace", map[string]any{"files": []any{"a"}, "final_newline": true, "keep_markdown_line_breaks": true}, "requires trailing_whitespace"},
		{"negative blank lines", map[string]any{"files": []any{"a"}, "max_blank_lines": -1}, "must not be negative"},
	} {
		if _, err := NewFix(test.raw); err == nil || !strings.Contains(err.Error(), test.want) {
			t.Errorf("%s: error = %v, want %q", test.name, err, test.want)
		}
	}
	for _, raw := range []map[string]any{
		{"files": []any{"a"}, "line_endings": "auto"}, {"files": []any{"a"}, "remove_byte_order_mark": true}, {"files": []any{"a"}, "max_blank_lines": 0},
	} {
		if _, err := NewFix(raw); err != nil {
			t.Errorf("NewFix(%v): %v", raw, err)
		}
	}
}
