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
		if got := string(trimTrailingWhitespace([]byte(test.input))); got != test.want {
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
