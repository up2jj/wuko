package decode

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/up2jj/wuko/step"
)

func TestStructuredFormatsDecodeJSONCompatibleValues(t *testing.T) {
	tests := []struct {
		name   string
		format string
		input  string
		want   any
	}{
		{
			name: "json", format: "json",
			input: `{"name":"wukó","items":[1,true,null]}`,
			want:  map[string]any{"name": "wukó", "items": []any{json.Number("1"), true, nil}},
		},
		{
			name: "yaml", format: "yaml",
			input: "name: wukó\nitems: [1, true, null]\nreleased: 2026-08-26T12:30:00Z\n",
			want: map[string]any{
				"name": "wukó", "items": []any{json.Number("1"), true, nil},
				"released": "2026-08-26T12:30:00Z",
			},
		},
		{
			name: "toml", format: "toml",
			input: "name = \"wukó\"\nreleased = 2026-08-26\nitems = [1, 2]\n",
			want: map[string]any{
				"name": "wukó", "released": "2026-08-26", "items": []any{json.Number("1"), json.Number("2")},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := runFrom(t, test.format, test.input, nil)
			if !reflect.DeepEqual(result.Outputs["value"], test.want) {
				t.Fatalf("value = %#v, want %#v", result.Outputs["value"], test.want)
			}
		})
	}
}

func TestStructuredFormatsAcceptTopLevelScalarsAndNull(t *testing.T) {
	tests := []struct {
		format string
		input  string
		want   any
	}{
		{format: "json", input: "42", want: json.Number("42")},
		{format: "json", input: "null", want: nil},
		{format: "yaml", input: "true\n", want: true},
		{format: "yaml", input: "null\n", want: nil},
	}
	for _, test := range tests {
		t.Run(test.format+"_"+test.input, func(t *testing.T) {
			result := runFrom(t, test.format, test.input, nil)
			if !reflect.DeepEqual(result.Outputs["value"], test.want) {
				t.Fatalf("value = %#v, want %#v", result.Outputs["value"], test.want)
			}
		})
	}
}

func TestLinesOptionsAndEndings(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		trim      bool
		omitEmpty bool
		want      []any
	}{
		{name: "empty", input: "", want: []any{}},
		{name: "missing final newline", input: "one\ntwo", want: []any{"one", "two"}},
		{name: "final newline", input: "one\ntwo\n", want: []any{"one", "two"}},
		{name: "crlf", input: "one\r\ntwo\r\n", want: []any{"one", "two"}},
		{name: "preserve blanks", input: " one \n\n \n", want: []any{" one ", "", " "}},
		{name: "trim", input: " one \n\n \n", trim: true, want: []any{"one", "", ""}},
		{name: "omit exact empty", input: " one \n\n \n", omitEmpty: true, want: []any{" one ", " "}},
		{name: "trim then omit", input: " one \n\n \n", trim: true, omitEmpty: true, want: []any{"one"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := runFrom(t, "lines", test.input, map[string]any{
				"trim": test.trim, "omit_empty": test.omitEmpty,
			})
			if !reflect.DeepEqual(result.Outputs["value"], test.want) {
				t.Fatalf("value = %#v, want %#v", result.Outputs["value"], test.want)
			}
		})
	}
}

func TestRejectsInvalidConfiguration(t *testing.T) {
	tests := []struct {
		name string
		raw  map[string]any
		want string
	}{
		{name: "missing input", raw: map[string]any{"format": "json"}, want: "exactly one"},
		{name: "both inputs", raw: map[string]any{"format": "json", "from": "vars.data", "path": "data.json"}, want: "exactly one"},
		{name: "empty from", raw: map[string]any{"format": "json", "from": ""}, want: "must not be empty"},
		{name: "invalid from", raw: map[string]any{"format": "json", "from": "data"}, want: "rooted at vars or steps"},
		{name: "empty path", raw: map[string]any{"format": "json", "path": ""}, want: "must not be empty"},
		{name: "missing format", raw: map[string]any{"from": "vars.data"}, want: "format is required"},
		{name: "unsupported format", raw: map[string]any{"format": "xml", "from": "vars.data"}, want: "json, yaml, toml, or lines"},
		{name: "lines option with json", raw: map[string]any{"format": "json", "from": "vars.data", "trim": false}, want: "only supported with lines"},
		{name: "zero limit", raw: map[string]any{"format": "json", "from": "vars.data", "max_bytes": "0B"}, want: "positive size"},
		{name: "invalid limit", raw: map[string]any{"format": "json", "from": "vars.data", "max_bytes": "12MB"}, want: "64KiB"},
		{name: "unknown field", raw: map[string]any{"format": "json", "from": "vars.data", "unknown": true}, want: "field unknown"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := New(test.raw)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("New() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestTemplatedConfigurationIsAcceptedDuringValidation(t *testing.T) {
	_, err := New(map[string]any{
		"format": "{{ .vars.format }}", "from": "{{ .vars.from }}", "max_bytes": "{{ .vars.limit }}",
		"trim": true,
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestFromMustResolveToString(t *testing.T) {
	runner, err := New(map[string]any{"format": "json", "from": "vars.data"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.Run(t.Context(), step.Request{Vars: map[string]any{"data": map[string]any{}}})
	if err == nil || !strings.Contains(err.Error(), "want string") {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestInputLimitAllowsExactSizeAndRejectsLargerInput(t *testing.T) {
	result := runFrom(t, "lines", "four", map[string]any{"max_bytes": "4B"})
	if !reflect.DeepEqual(result.Outputs["value"], []any{"four"}) {
		t.Fatalf("result = %#v", result)
	}
	runner, err := New(map[string]any{"format": "lines", "from": "vars.data", "max_bytes": "4B"})
	if err != nil {
		t.Fatal(err)
	}
	result, err = runner.Run(t.Context(), step.Request{Vars: map[string]any{"data": "five!"}})
	if err == nil || !strings.Contains(err.Error(), "exceeds max_bytes") || len(result.Outputs) != 0 {
		t.Fatalf("result = %#v, error = %v", result, err)
	}
}

func TestParseErrorsContainLocationsAndRejectExtraDocuments(t *testing.T) {
	tests := []struct {
		name   string
		format string
		input  string
		want   string
	}{
		{name: "json syntax", format: "json", input: "{\n  \"value\": ]\n}", want: "line 2, column"},
		{name: "json trailing", format: "json", input: "{}\n[]", want: "multiple JSON values"},
		{name: "yaml syntax", format: "yaml", input: "value: [one,\n", want: "line 1, column"},
		{name: "yaml documents", format: "yaml", input: "one\n---\ntwo\n", want: "multiple YAML documents"},
		{name: "toml syntax", format: "toml", input: "value = [1,\n", want: "line 2, column"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner, err := New(map[string]any{"format": test.format, "from": "vars.data"})
			if err != nil {
				t.Fatal(err)
			}
			_, err = runner.Run(t.Context(), step.Request{Vars: map[string]any{"data": test.input}})
			if err == nil || !strings.Contains(err.Error(), test.want) || !strings.Contains(err.Error(), "from \"vars.data\"") {
				t.Fatalf("Run() error = %v, want %q with source context", err, test.want)
			}
		})
	}
}

func TestReadsRelativeAndAbsoluteRegularFiles(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "data.json")
	if err := os.WriteFile(path, []byte(`{"name":"wuko"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, configured := range []string{"data.json", path} {
		runner, err := New(map[string]any{"format": "json", "path": configured})
		if err != nil {
			t.Fatal(err)
		}
		result, err := runner.Run(t.Context(), step.Request{RunDir: root})
		if err != nil {
			t.Fatal(err)
		}
		if result.Outputs["value"].(map[string]any)["name"] != "wuko" {
			t.Fatalf("result = %#v", result)
		}
	}
}

func TestFileFailuresAreAtomic(t *testing.T) {
	root := t.TempDir()
	large := filepath.Join(root, "large")
	if err := os.WriteFile(large, []byte("12345"), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		path string
		want string
	}{
		{name: "missing", path: "missing", want: "opening decode file"},
		{name: "directory", path: ".", want: "regular file"},
		{name: "oversized", path: "large", want: "exceeds max_bytes"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner, err := New(map[string]any{"format": "lines", "path": test.path, "max_bytes": "4B"})
			if err != nil {
				t.Fatal(err)
			}
			result, err := runner.Run(t.Context(), step.Request{RunDir: root})
			if err == nil || !strings.Contains(err.Error(), test.want) || len(result.Outputs) != 0 {
				t.Fatalf("result = %#v, error = %v", result, err)
			}
		})
	}
}

func TestReadBoundedHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	data, err := readBounded(ctx, strings.NewReader("value"), 10)
	if !errors.Is(err, context.Canceled) || data != nil {
		t.Fatalf("data = %q, error = %v", data, err)
	}
}

func TestRunHonorsCancellationBeforeReadingFile(t *testing.T) {
	runner, err := New(map[string]any{"format": "lines", "path": "data.txt"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	result, err := runner.Run(ctx, step.Request{RunDir: t.TempDir()})
	if !errors.Is(err, context.Canceled) || len(result.Outputs) != 0 {
		t.Fatalf("result = %#v, error = %v", result, err)
	}
}

func runFrom(t *testing.T, format, input string, options map[string]any) step.Result {
	t.Helper()
	raw := map[string]any{"format": format, "from": "vars.data"}
	for key, value := range options {
		raw[key] = value
	}
	runner, err := New(raw)
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(t.Context(), step.Request{Vars: map[string]any{"data": input}})
	if err != nil {
		t.Fatal(err)
	}
	return result
}
