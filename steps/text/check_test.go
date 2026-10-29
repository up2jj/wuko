package text

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/up2jj/wuko/internal/steptest"
	"github.com/up2jj/wuko/internal/testfs"
	"github.com/up2jj/wuko/step"
)

func TestTextCheckReportsForbiddenTextWithoutItsContent(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	secret := "BEGIN " + "RSA PRIVATE KEY"
	testfs.WriteTree(t, dir, map[string]string{
		"key.pem": "header\n-----" + secret + "-----\nprivate-material\n", "merge.txt": "ok\n<<<<<<< ours\n", "clean.txt": "fine\n",
	})
	runner := steptest.Build(t, NewCheck, map[string]any{
		"files": []any{"clean.txt", "key.pem", "merge.txt"},
		"forbid": []any{
			map[string]any{"pattern": `BEGIN (RSA |EC )?PRIVATE KEY`, "message": "private key"},
			`(?m)^<<<<<<< `,
		},
	})
	result, err := runner.Run(t.Context(), step.Request{RunDir: dir})
	if err == nil {
		t.Fatal("forbidden text accepted")
	}
	want := "2 problem(s) found:\nkey.pem:2: private key\nmerge.txt:2: matches forbidden pattern \"(?m)^<<<<<<< \""
	if err.Error() != want {
		t.Fatalf("error = %q, want %q", err, want)
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "private-material") {
		t.Fatalf("error disclosed file content: %v", err)
	}
	wantFailures := []any{
		map[string]any{"path": "key.pem", "line": 2, "message": "private key"},
		map[string]any{"path": "merge.txt", "line": 2, "message": "matches forbidden pattern \"(?m)^<<<<<<< \""},
	}
	if diff := cmp.Diff(map[string]any{"checked": 3, "count": 2, "failures": wantFailures}, result.Outputs); diff != "" {
		t.Fatalf("outputs mismatch (-want +got):\n%s", diff)
	}
}

func TestTextCheckSizeLimitHonoursExemptions(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	testfs.WriteFile(t, filepath.Join(dir, "large.dat"), bytes.Repeat([]byte{'x'}, 2049), 0o644)
	testfs.WriteFile(t, filepath.Join(dir, "lfs.dat"), bytes.Repeat([]byte{'x'}, 4096), 0o644)
	testfs.WriteFile(t, filepath.Join(dir, "small.dat"), bytes.Repeat([]byte{'x'}, 2048), 0o644)
	if err := os.Symlink("large.dat", filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	runner := steptest.Build(t, NewCheck, map[string]any{
		"files": []any{"large.dat", "lfs.dat", "small.dat", "link"}, "max_size": "2KiB",
		"size_exempt": map[string]any{"expr": "vars.lfs"},
	})
	_, err := runner.Run(t.Context(), step.Request{RunDir: dir, Vars: map[string]any{"lfs": []any{"lfs.dat"}}})
	if err == nil || err.Error() != "1 problem(s) found:\nlarge.dat: 2049 bytes exceeds max_size 2KiB" {
		t.Fatalf("error = %v", err)
	}
}

func TestTextCheckParsesDocumentsWithLocations(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	testfs.WriteTree(t, dir, map[string]string{
		"good.json": `{"a": 1}`, "bad.json": "{\n\"a\": }", "two.json": "{} {}",
		"good.yaml": "a: 1\n", "bad.yaml": "a: !custom b\n", "good.toml": "a = 1\n", "bad.toml": "a = 1\na = 2\n",
	})
	for _, test := range []struct {
		format string
		files  []any
		want   []string
	}{
		{"json", []any{"good.json", "bad.json", "two.json"}, []string{"bad.json: invalid JSON: line 2, column 6", "two.json: invalid JSON: line 1, column 4: multiple JSON values"}},
		{"yaml", []any{"good.yaml", "bad.yaml"}, []string{`bad.yaml: invalid YAML: line 1: unsupported YAML tag "!custom"`}},
		{"toml", []any{"good.toml", "bad.toml"}, []string{"bad.toml: invalid TOML: line 2, column 1"}},
	} {
		_, err := steptest.Build(t, NewCheck, map[string]any{"files": test.files, "parse": test.format}).Run(t.Context(), step.Request{RunDir: dir})
		if err == nil {
			t.Fatalf("%s: invalid documents accepted", test.format)
		}
		for _, want := range test.want {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%s: error %q does not contain %q", test.format, err, want)
			}
		}
	}
	if _, err := steptest.Build(t, NewCheck, map[string]any{"files": []any{"good.json"}, "parse": "json"}).Run(t.Context(), step.Request{RunDir: dir}); err != nil {
		t.Fatalf("valid JSON rejected: %v", err)
	}
}

func TestNewCheckValidatesConfiguration(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		raw  map[string]any
		want string
	}{
		{"no check", map[string]any{"files": []any{"a"}}, "at least one of forbid, max_size, and parse"},
		{"blank pattern", map[string]any{"files": []any{"a"}, "forbid": []any{" "}}, "forbid[0] pattern must not be blank"},
		{"invalid pattern", map[string]any{"files": []any{"a"}, "forbid": []any{"("}}, "forbid[0]: invalid pattern"},
		{"bad size", map[string]any{"files": []any{"a"}, "max_size": "big"}, "max_size"},
		{"exempt without size", map[string]any{"files": []any{"a"}, "parse": "json", "size_exempt": []any{"a"}}, "size_exempt requires max_size"},
		{"blank exempt", map[string]any{"files": []any{"a"}, "max_size": "1KiB", "size_exempt": []any{""}}, "size_exempt[0]"},
		{"bad format", map[string]any{"files": []any{"a"}, "parse": "xml"}, "parse must be json, yaml, or toml"},
		{"unknown forbid field", map[string]any{"files": []any{"a"}, "forbid": []any{map[string]any{"pattern": "x", "flags": "i"}}}, "flags"},
	} {
		if _, err := NewCheck(test.raw); err == nil || !strings.Contains(err.Error(), test.want) {
			t.Errorf("%s: error = %v, want %q", test.name, err, test.want)
		}
	}
}
