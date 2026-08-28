package edit

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/up2jj/wuko/step"
)

func TestVariableSourceReturnsEditedClone(t *testing.T) {
	t.Parallel()
	runner := newRunner(t, map[string]any{
		"operation": "set",
		"from":      map[string]any{"var": "deployment"},
		"path":      "$.spec.replicas",
		"expr":      "current + 1",
	})
	source := map[string]any{"spec": map[string]any{"replicas": 2}}
	result, err := runner.Run(t.Context(), step.Request{Vars: map[string]any{"deployment": source}})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"spec": map[string]any{"replicas": 3}}
	if !reflect.DeepEqual(result.Outputs["value"], want) {
		t.Fatalf("value = %#v, want %#v", result.Outputs["value"], want)
	}
	if got := source["spec"].(map[string]any)["replicas"]; got != 2 {
		t.Fatalf("source variable was mutated: replicas = %#v", got)
	}
	if result.Variables != nil {
		t.Fatalf("edit unexpectedly wrote variables: %#v", result.Variables)
	}
}

func TestAllExpressionsUseOriginalSnapshot(t *testing.T) {
	t.Parallel()
	runner := newRunner(t, map[string]any{
		"operation": "set",
		"from":      map[string]any{"expr": "vars.document"},
		"path":      "$[*]",
		"expr":      "current + index + vars.increment",
		"result":    "all",
	})
	result, err := runner.Run(t.Context(), step.Request{Vars: map[string]any{
		"document": []any{1, 10}, "increment": 2,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outputs["count"] != 2 || result.Outputs["changed_count"] != 2 {
		t.Fatalf("outputs = %#v", result.Outputs)
	}
	if got := result.Outputs["replacements"]; !reflect.DeepEqual(got, []any{3, 13}) {
		t.Fatalf("replacements = %#v", got)
	}
}

func TestMissingPolicy(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name    string
		missing string
		wantErr bool
	}{
		{name: "default error", wantErr: true},
		{name: "ignore", missing: "ignore"},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw := map[string]any{
				"operation": "set", "from": map[string]any{"var": "document"},
				"path": "$.missing", "expr": `required(current, "must not run")`,
			}
			if test.missing != "" {
				raw["missing"] = test.missing
			}
			runner := newRunner(t, raw)
			result, err := runner.Run(t.Context(), step.Request{Vars: map[string]any{"document": map[string]any{"present": true}}})
			if (err != nil) != test.wantErr {
				t.Fatalf("error = %v, wantErr %t", err, test.wantErr)
			}
			if !test.wantErr && (result.Outputs["count"] != 0 || result.Outputs["changed"] != false) {
				t.Fatalf("outputs = %#v", result.Outputs)
			}
		})
	}
}

func TestFileEditsPreserveUnrelatedBytes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		file  string
		input string
		path  string
		value any
		want  string
	}{
		{
			name: "json", file: "package.json",
			input: "{\n  \"name\": \"demo\",\n  \"version\": \"1.0.0\",  \n  \"private\": true\n}\n",
			path:  "$.version", value: "1.1.0",
			want: "{\n  \"name\": \"demo\",\n  \"version\": \"1.1.0\",  \n  \"private\": true\n}\n",
		},
		{
			name: "yaml", file: "config.yaml",
			input: "# deployment\nimage: 'api:v1' # keep this comment\nreplicas: 2\n",
			path:  "$.image", value: "api:v2",
			want: "# deployment\nimage: 'api:v2' # keep this comment\nreplicas: 2\n",
		},
		{
			name: "toml", file: "config.toml",
			input: "# package metadata\nname   = \"demo\"\nversion = \"1.0.0\" # release\n",
			path:  "$.version", value: "1.1.0",
			want: "# package metadata\nname   = \"demo\"\nversion = \"1.1.0\" # release\n",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			path := filepath.Join(directory, test.file)
			if err := os.WriteFile(path, []byte(test.input), 0o640); err != nil {
				t.Fatal(err)
			}
			runner := newRunner(t, map[string]any{
				"operation": "set", "from": map[string]any{"file": test.file},
				"path": test.path, "value": test.value,
			})
			result, err := runner.Run(t.Context(), step.Request{RunDir: directory})
			if err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(data) != test.want {
				t.Fatalf("file:\n%s\nwant:\n%s", data, test.want)
			}
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != 0o640 {
				t.Fatalf("mode = %o, want 640", info.Mode().Perm())
			}
			if result.Outputs["changed"] != true || result.Outputs["changed_count"] != 1 {
				t.Fatalf("outputs = %#v", result.Outputs)
			}
		})
	}
}

func TestFileMissingIgnoreDoesNotRewrite(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	path := filepath.Join(directory, "package.json")
	input := []byte("{\"version\":\"1.0.0\"}\n")
	if err := os.WriteFile(path, input, 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	runner := newRunner(t, map[string]any{
		"operation": "set", "from": map[string]any{"file": "package.json"},
		"path": "$.missing", "expr": "current + 1", "missing": "ignore",
	})
	result, err := runner.Run(t.Context(), step.Request{RunDir: directory})
	if err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytesEqual(data, input) || !before.ModTime().Equal(after.ModTime()) || result.Outputs["changed"] != false {
		t.Fatalf("ignored edit changed file or outputs: %#v", result.Outputs)
	}
}

func TestAllFileEditIsAtomicWhenReplacementFails(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	path := filepath.Join(directory, "values.json")
	input := []byte("[1, 2]\n")
	if err := os.WriteFile(path, input, 0o600); err != nil {
		t.Fatal(err)
	}
	runner := newRunner(t, map[string]any{
		"operation": "set", "from": map[string]any{"file": "values.json"},
		"path": "$[*]", "expr": "index == 0 ? current + 1 : current.missing", "result": "all",
	})
	if _, err := runner.Run(t.Context(), step.Request{RunDir: directory}); err == nil {
		t.Fatal("edit succeeded")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytesEqual(data, input) {
		t.Fatalf("file changed after replacement failure: %q", data)
	}
}

func TestAllFileEditDoesNotReencodeUnchangedMatches(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	path := filepath.Join(directory, "values.toml")
	input := []byte("first = 'keep-style'\nsecond = 1\n")
	if err := os.WriteFile(path, input, 0o600); err != nil {
		t.Fatal(err)
	}
	runner := newRunner(t, map[string]any{
		"operation": "set", "from": map[string]any{"file": "values.toml"},
		"path": "$.*", "expr": `path == "$['second']" ? current + 1 : current`, "result": "all",
	})
	if _, err := runner.Run(t.Context(), step.Request{RunDir: directory}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := "first = 'keep-style'\nsecond = 2\n"
	if string(data) != want {
		t.Fatalf("file = %q, want %q", data, want)
	}
}

func TestNewRejectsInvalidConfiguration(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, want string
		raw        map[string]any
	}{
		{name: "source union", want: "exactly one", raw: map[string]any{"operation": "set", "from": map[string]any{"file": "a.json", "var": "a"}, "path": "$", "value": 1}},
		{name: "replacement union", want: "exactly one", raw: map[string]any{"operation": "set", "from": map[string]any{"var": "a"}, "path": "$", "value": 1, "expr": "2"}},
		{name: "result", want: "one or all", raw: map[string]any{"operation": "set", "from": map[string]any{"var": "a"}, "path": "$", "value": 1, "result": "first"}},
		{name: "missing", want: "error, ignore, or create", raw: map[string]any{"operation": "set", "from": map[string]any{"var": "a"}, "path": "$", "value": 1, "missing": "skip"}},
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

func newRunner(t *testing.T, raw map[string]any) step.Runner {
	t.Helper()
	runner, err := New(raw)
	if err != nil {
		t.Fatal(err)
	}
	return runner
}

func bytesEqual(left, right []byte) bool { return string(left) == string(right) }

func TestFileSourceRejectsSymlink(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	target := filepath.Join(directory, "real.json")
	input := []byte("{\"a\": 1}\n")
	if err := os.WriteFile(target, input, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real.json", filepath.Join(directory, "link.json")); err != nil {
		t.Fatal(err)
	}
	runner := newRunner(t, map[string]any{
		"operation": "set", "from": map[string]any{"file": "link.json"},
		"path": "$.a", "value": 2,
	})
	_, err := runner.Run(t.Context(), step.Request{RunDir: directory})
	if err == nil || !strings.Contains(err.Error(), "must be a regular file") {
		t.Fatalf("error = %v, want regular-file rejection", err)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if !bytesEqual(data, input) {
		t.Fatalf("symlink target changed: %q", data)
	}
	info, err := os.Lstat(filepath.Join(directory, "link.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("link was detached: mode = %v", info.Mode())
	}
}

func TestJSONNumbersCompareByValue(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	path := filepath.Join(directory, "values.json")
	input := []byte("{\"replicas\": 2}\n")
	if err := os.WriteFile(path, input, 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	runner := newRunner(t, map[string]any{
		"operation": "set", "from": map[string]any{"file": "values.json"},
		"path": "$.replicas", "value": 2,
	})
	result, err := runner.Run(t.Context(), step.Request{RunDir: directory})
	if err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outputs["changed"] != false || result.Outputs["changed_count"] != 0 {
		t.Fatalf("outputs = %#v, want unchanged", result.Outputs)
	}
	if !bytesEqual(data, input) || !before.ModTime().Equal(after.ModTime()) {
		t.Fatalf("unchanged edit rewrote the file: %q", data)
	}
}

func TestExpressionsSeeJSONNumbersAsNumbers(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	path := filepath.Join(directory, "values.json")
	if err := os.WriteFile(path, []byte("{\"replicas\": 2, \"ratio\": 1.5}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := newRunner(t, map[string]any{
		"operation": "set", "from": map[string]any{"file": "values.json"},
		"path": "$.*", "expr": "current * 2", "result": "all",
	})
	if _, err := runner.Run(t.Context(), step.Request{RunDir: directory}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := "{\"replicas\": 4, \"ratio\": 3}\n"
	if string(data) != want {
		t.Fatalf("file = %q, want %q", data, want)
	}
}

func TestUnresolvedReplacementExpressionIsReported(t *testing.T) {
	t.Parallel()
	runner := newRunner(t, map[string]any{
		"operation": "set", "from": map[string]any{"var": "document"},
		"path": "$.a", "expr": "{{ inputs.replicas }}",
	})
	_, err := runner.Run(t.Context(), step.Request{Vars: map[string]any{"document": map[string]any{"a": 1}}})
	if err == nil || !strings.Contains(err.Error(), "unresolved template") {
		t.Fatalf("error = %v, want unresolved template", err)
	}
}

func TestTOMLKeyWithEscapedQuote(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	path := filepath.Join(directory, "config.toml")
	if err := os.WriteFile(path, []byte("\"a\\\"=b\" = 1\nother = 2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := newRunner(t, map[string]any{
		"operation": "set", "from": map[string]any{"file": "config.toml"},
		"path": "$.other", "value": 3,
	})
	if _, err := runner.Run(t.Context(), step.Request{RunDir: directory}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := "\"a\\\"=b\" = 1\nother = 3\n"
	if string(data) != want {
		t.Fatalf("file = %q, want %q", data, want)
	}
}

func TestYAMLScalarSpans(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, input, path string
		value             any
		want              string
	}{
		{
			name: "plain scalar keeps a comma", input: "desc: hello, world\nother: 1\n",
			path: "$.desc", value: "bye", want: "desc: bye\nother: 1\n",
		},
		{
			name: "flow sequence last element", input: "a: [1, 2]\n",
			path: "$.a[1]", value: 3, want: "a: [1, 3]\n",
		},
		{
			name: "flow mapping last value", input: "a: {x: 1, y: 2}\n",
			path: "$.a.y", value: 3, want: "a: {x: 1, y: 3}\n",
		},
		{
			name: "quoted string replaced by a number", input: "port: '8080'\n",
			path: "$.port", value: 8080, want: "port: 8080\n",
		},
		{
			name: "nested literal block", input: "data:\n  script: |\n    echo hi\n    echo bye\nother: 1\n",
			path: "$.data.script", value: "echo new\n", want: "data:\n  script: |\n    echo new\nother: 1\n",
		},
		{
			name: "literal block gains lines", input: "script: |\n  echo hi\nother: 1\n",
			path: "$.script", value: "one\ntwo\n", want: "script: |\n  one\n  two\nother: 1\n",
		},
		{
			name: "folded block", input: "note: >\n  wrapped text\nother: 1\n",
			path: "$.note", value: "new text\n", want: "note: >\n  new text\nother: 1\n",
		},
		{
			name: "sequence item", input: "args:\n  - alpha, beta\n  - gamma\n",
			path: "$.args[0]", value: "delta", want: "args:\n  - delta\n  - gamma\n",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			path := filepath.Join(directory, "config.yaml")
			if err := os.WriteFile(path, []byte(test.input), 0o600); err != nil {
				t.Fatal(err)
			}
			runner := newRunner(t, map[string]any{
				"operation": "set", "from": map[string]any{"file": "config.yaml"},
				"path": test.path, "value": test.value,
			})
			if _, err := runner.Run(t.Context(), step.Request{RunDir: directory}); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(data) != test.want {
				t.Fatalf("file = %q, want %q", data, test.want)
			}
		})
	}
}

func TestDuplicateSelectionCollapsesBeforeResultCheck(t *testing.T) {
	t.Parallel()
	runner := newRunner(t, map[string]any{
		"operation": "set", "from": map[string]any{"var": "document"},
		"path": "$['a','a']", "value": 2,
	})
	result, err := runner.Run(t.Context(), step.Request{Vars: map[string]any{"document": map[string]any{"a": 1}}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outputs["count"] != 1 {
		t.Fatalf("outputs = %#v, want a single match", result.Outputs)
	}
}

func TestOverlappingSelectionIsRejected(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, path string
		document   any
	}{
		{name: "parent and child", path: "$..*", document: map[string]any{"a": map[string]any{"b": 1}}},
		// "/a" and "/a/b" surround "/a." in plain lexical order, so overlap
		// detection has to sort pointers segment by segment.
		{name: "sibling sorts between", path: "$..*", document: map[string]any{
			"a": map[string]any{"b": 1}, "a.": 2,
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner := newRunner(t, map[string]any{
				"operation": "set", "from": map[string]any{"var": "document"},
				"path": test.path, "value": 0, "result": "all",
			})
			_, err := runner.Run(t.Context(), step.Request{Vars: map[string]any{"document": test.document}})
			if err == nil || !strings.Contains(err.Error(), "overlapping locations") {
				t.Fatalf("error = %v, want overlap rejection", err)
			}
		})
	}
}

// sameValue takes a fast path for numbers; it must stay exactly as strict as
// the JSON round trip, because the post-write verification relies on the same
// notion of equality to accept an unchanged match that was left unpatched.
func TestSameValueMatchesRoundTrip(t *testing.T) {
	values := []any{
		nil, true, false, "2", "", "abc",
		json.Number("2"), json.Number("2.0"), json.Number("2e0"), json.Number("3"),
		json.Number("9007199254740993"), json.Number("-0"),
		int(2), int(3), int8(2), int16(2), int32(2), int64(2), int64(9007199254740993),
		uint(2), uint64(2), float32(2), float64(2), float64(2.5), float64(0),
		[]any{json.Number("2")}, []any{2}, []any{2.0}, []any{"2"},
		map[string]any{"a": json.Number("2")}, map[string]any{"a": 2}, map[string]any{"a": 2.5},
		time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC), "2020-01-01T00:00:00Z",
	}
	for _, left := range values {
		for _, right := range values {
			want := reflect.DeepEqual(normalizeNumbers(left), normalizeNumbers(right))
			if got := sameValue(left, right); got != want {
				t.Errorf("sameValue(%T %v, %T %v) = %t, round trip says %t", left, left, right, right, got, want)
			}
		}
	}
}

// The outputs hand back documents the step built for itself, so they must not
// alias each other or the caller's variable.
func TestOutputsDoNotAliasEachOther(t *testing.T) {
	t.Parallel()
	runner := newRunner(t, map[string]any{
		"operation": "set", "from": map[string]any{"var": "document"},
		"path": "$.*", "value": map[string]any{"tag": "new"}, "result": "all",
	})
	source := map[string]any{"a": map[string]any{"tag": "old"}, "b": map[string]any{"tag": "old"}}
	result, err := runner.Run(t.Context(), step.Request{Vars: map[string]any{"document": source}})
	if err != nil {
		t.Fatal(err)
	}
	replacements := result.Outputs["replacements"].([]any)
	replacements[0].(map[string]any)["tag"] = "mutated"
	value := result.Outputs["value"].(map[string]any)
	for _, key := range []string{"a", "b"} {
		if got := value[key].(map[string]any)["tag"]; got != "new" {
			t.Fatalf("value[%q].tag = %q, want new", key, got)
		}
	}
	if got := replacements[1].(map[string]any)["tag"]; got != "new" {
		t.Fatalf("replacements alias each other: %q", got)
	}
	if got := source["a"].(map[string]any)["tag"]; got != "old" {
		t.Fatalf("source variable was mutated: %q", got)
	}
}

func benchFile(b *testing.B, name string, count int) string {
	b.Helper()
	directory := b.TempDir()
	values := make([]string, count)
	for i := range values {
		values[i] = "    {\"id\": " + strconv.Itoa(i) + ", \"replicas\": 1}"
	}
	body := "[\n" + strings.Join(values, ",\n") + "\n]\n"
	path := filepath.Join(directory, name)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		b.Fatal(err)
	}
	b.Logf("%s: %d bytes, %d matches", name, len(body), count)
	return directory
}

func BenchmarkFileEditAll(b *testing.B) {
	for _, count := range []int{100, 1000, 5000} {
		b.Run(strconv.Itoa(count), func(b *testing.B) {
			directory := benchFile(b, "values.json", count)
			runner, err := New(map[string]any{
				"operation": "set", "from": map[string]any{"file": "values.json"},
				"path": "$[*].replicas", "expr": "index + 2", "result": "all",
			})
			if err != nil {
				b.Fatal(err)
			}
			b.ResetTimer()
			b.ReportAllocs()
			for range b.N {
				if _, err := runner.Run(b.Context(), step.Request{RunDir: directory}); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkApplyTextEdits(b *testing.B) {
	for _, count := range []int{100, 1000, 5000} {
		b.Run(strconv.Itoa(count), func(b *testing.B) {
			data := []byte(strings.Repeat("x", count*40))
			b.ResetTimer()
			b.ReportAllocs()
			text := []byte("0123456789")
			edits := make([]textEdit, count)
			for i := range edits {
				edits[i] = textEdit{start: i * 40, end: i*40 + 10, text: text}
			}
			b.ResetTimer()
			b.ReportAllocs()
			for range b.N {
				if _, err := applyTextEdits(data, edits); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
