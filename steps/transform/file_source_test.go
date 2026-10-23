package transform

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/up2jj/wuko/process"
	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/steps/decode"
)

const fileSourceRecords = `[
  {"id": 1, "name": "b", "score": 1.50, "tags": ["x"], "big": 1e3, "nested": {"n": null}},
  {"id": 2, "name": "a", "score": 2, "tags": [], "big": 12345678901, "nested": {"n": true}},
  {"id": 3, "name": "b", "score": -0.25, "tags": ["y", "z"], "big": 0, "nested": {}}
]`

const fileSourceLines = `{"id": 1, "name": "b", "score": 1.50, "tags": ["x"], "big": 1e3, "nested": {"n": null}}

{"id": 2, "name": "a", "score": 2, "tags": [], "big": 12345678901, "nested": {"n": true}}
{"id": 3, "name": "b", "score": -0.25, "tags": ["y", "z"], "big": 0, "nested": {}}
`

func writeSourceFile(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func runFileSource(t *testing.T, from map[string]any, operations []any, request step.Request) (any, error) {
	t.Helper()
	runner, err := New(map[string]any{"from": from, "operations": operations})
	if err != nil {
		return nil, err
	}
	result, err := runner.Run(t.Context(), request)
	if err != nil {
		return nil, err
	}
	return result.Outputs["value"], nil
}

// A streamed file must give exactly what decode followed by transform gives, numbers
// included, for every pipeline shape: streamed stages, sinks, and collected operations.
func TestFileSourceMatchesDecodeThenTransform(t *testing.T) {
	t.Parallel()
	pipelines := [][]any{
		{"clone"},
		{map[string]any{"filter": "item.score > 0"}, map[string]any{"map": "{n: item.name, s: item.score, b: item.big}"}},
		{map[string]any{"unique_by": "item.name"}, map[string]any{"sort_by": "item.name"}},
		{map[string]any{"map": "item.tags"}, "flatten", "unique"},
		{map[string]any{"find": "item.name == 'a'"}},
		{map[string]any{"count_by": "item.nested.n == true"}},
		{map[string]any{"group_by": "item.name"}},
		{map[string]any{"sum_by": "item.id"}},
	}
	jsonPath := writeSourceFile(t, "records.json", fileSourceRecords)
	linesPath := writeSourceFile(t, "records.jsonl", fileSourceLines)
	decoder, err := decode.New(map[string]any{"format": "json", "path": jsonPath})
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decoder.Run(t.Context(), step.Request{})
	if err != nil {
		t.Fatal(err)
	}
	request := step.Request{Steps: map[string]any{"data": decoded.Outputs}}
	for _, operations := range pipelines {
		runner := buildRunner(t, map[string]any{"from": "steps.data.value", "operations": operations})
		want, err := runner.Run(t.Context(), request)
		if err != nil {
			t.Fatal(err)
		}
		for _, path := range []string{jsonPath, linesPath} {
			got, err := runFileSource(t, map[string]any{"file": path}, operations, step.Request{})
			if err != nil {
				t.Fatalf("%v from %s: %v", operations, filepath.Base(path), err)
			}
			if !reflect.DeepEqual(got, want.Outputs["value"]) {
				t.Fatalf("%v from %s = %#v, want %#v", operations, filepath.Base(path), got, want.Outputs["value"])
			}
		}
	}
}

// Once the pipeline has its result, the rest of the file is never read, so content past that
// point cannot fail the step.
func TestFileSourceStopsReadingOnceTheResultIsKnown(t *testing.T) {
	t.Parallel()
	for name, content := range map[string]string{
		"data.json":  `[{"id": 1}, {"id": 2}, {"id": 3}, not json at all`,
		"data.jsonl": "{\"id\": 1}\n{\"id\": 2}\n{\"id\": 3}\nnot json at all\n",
	} {
		path := writeSourceFile(t, name, content)
		got, err := runFileSource(t, map[string]any{"file": path}, []any{map[string]any{"map": "item.id"}, map[string]any{"take": 2}}, step.Request{})
		if err != nil || !reflect.DeepEqual(got, []any{1, 2}) {
			t.Fatalf("%s: value = %#v, error = %v", name, got, err)
		}
	}
}

func TestFileSourceResolvesRelativePathsAgainstTheRunDirectory(t *testing.T) {
	t.Parallel()
	path := writeSourceFile(t, "items.jsonl", "1\n2\n")
	got, err := runFileSource(t, map[string]any{"file": "items.jsonl"}, []any{map[string]any{"map": "item * 10"}}, step.Request{RunDir: filepath.Dir(path)})
	if err != nil || !reflect.DeepEqual(got, []any{10, 20}) {
		t.Fatalf("value = %#v, error = %v", got, err)
	}
}

func TestFileSourceReportsUnreadableInput(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, file, content string
		from                map[string]any
		want                string
	}{
		{name: "malformed item", file: "a.json", content: `[1, {"a": }]`, want: "item 1"},
		{name: "not an array", file: "a.json", content: `{"a": 1}`, want: "must contain a JSON array"},
		{name: "empty", file: "a.json", content: "", want: "is empty"},
		{name: "trailing value", file: "a.json", content: `[1] [2]`, want: "multiple JSON values"},
		{name: "unterminated", file: "a.json", content: `[1, 2`, want: "reading transform file"},
		{name: "malformed line", file: "a.jsonl", content: "1\n\n{oops}\n", want: "line 3"},
		{name: "two values on a line", file: "a.jsonl", content: "1 2\n", want: "exactly one JSON value"},
		{name: "too large", file: "a.json", content: `[1, 2, 3]`, from: map[string]any{"max_bytes": "4B"}, want: "exceeds max_bytes 4B"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			from := map[string]any{"file": writeSourceFile(t, test.file, test.content)}
			for key, value := range test.from {
				from[key] = value
			}
			_, err := runFileSource(t, from, []any{"clone"}, step.Request{})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

// A file that grows past max_bytes after it was opened still fails rather than being cut
// short silently.
func TestFileSourceLimitAppliesWhileReading(t *testing.T) {
	t.Parallel()
	reader := &limitedReader{reader: strings.NewReader("[1, 2, 3]"), remaining: 4}
	_, err := collect(jsonArrayItems(t.Context(), reader, "grown.json"))
	if err == nil || !strings.Contains(err.Error(), errFileTooLarge.Error()) {
		t.Fatalf("error = %v, want the size limit", err)
	}
}

func TestFileSourceConfiguration(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		from any
		want string
	}{
		{name: "unknown extension", from: map[string]any{"file": "data.txt"}, want: "from.format is required"},
		{name: "unknown format", from: map[string]any{"file": "data.json", "format": "csv"}, want: "json or jsonl"},
		{name: "bad size", from: map[string]any{"file": "data.json", "max_bytes": "lots"}, want: "from.max_bytes"},
		{name: "zero size", from: map[string]any{"file": "data.json", "max_bytes": "0B"}, want: "from.max_bytes"},
		{name: "expr and file", from: map[string]any{"file": "data.json", "expr": "vars.x"}, want: "exactly one non-empty expr"},
		{name: "unknown field", from: map[string]any{"file": "data.json", "path": "x"}, want: `unknown field "path"`},
		{name: "options without file", from: map[string]any{"format": "json"}, want: "must contain expr or file"},
		{name: "empty file", from: map[string]any{"file": " "}, want: "non-empty string"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := New(map[string]any{"from": test.from, "operations": []any{"clone"}})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}

	// A file source is a list, so an object operation is rejected when the workflow loads.
	if _, err := New(map[string]any{"from": map[string]any{"file": "data.json"}, "operations": []any{"keys"}}); err == nil || !strings.Contains(err.Error(), "cannot consume list") {
		t.Fatalf("object operation on a file source: error = %v", err)
	}
	// The format can come from the extension or be given for any name.
	for _, from := range []map[string]any{{"file": "DATA.NDJSON"}, {"file": "export.log", "format": "jsonl"}, {"file": "{{ .vars.path }}"}} {
		if _, err := New(map[string]any{"from": from, "operations": []any{"clone"}}); err != nil {
			t.Fatalf("from %v: %v", from, err)
		}
	}
}

func TestFileSourceRejectsUnrenderedTemplatesAtRunTime(t *testing.T) {
	t.Parallel()
	_, err := runFileSource(t, map[string]any{"file": "{{ .vars.path }}"}, []any{"clone"}, step.Request{})
	if err == nil || !strings.Contains(err.Error(), "from.file contains an unresolved template") {
		t.Fatalf("error = %v", err)
	}
}

type remoteOnlyExecutor struct{}

func (remoteOnlyExecutor) Run(context.Context, process.Options) (process.Result, error) {
	return process.Result{}, nil
}

func TestFileSourceRefusesToReadInsideAnExecutor(t *testing.T) {
	t.Parallel()
	path := writeSourceFile(t, "a.json", "[1]")
	_, err := runFileSource(t, map[string]any{"file": path}, []any{"clone"}, step.Request{Executor: remoteOnlyExecutor{}})
	if err == nil || !strings.Contains(err.Error(), "cannot read inside this executor") {
		t.Fatalf("error = %v", err)
	}
}

func TestFileSourceRequiresARegularFile(t *testing.T) {
	t.Parallel()
	for name, path := range map[string]string{"directory": t.TempDir(), "missing": filepath.Join(t.TempDir(), "none.json")} {
		_, err := runFileSource(t, map[string]any{"file": path, "format": "json"}, []any{"clone"}, step.Request{})
		if err == nil {
			t.Fatalf("%s: expected an error", name)
		}
	}
}
