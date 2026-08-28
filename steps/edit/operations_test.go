package edit

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/up2jj/wuko/step"
)

func TestJSONStructuralEditsPreserveUnrelatedBytes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, input, want string
		config            map[string]any
	}{
		{
			name: "create", input: "{\n  \"name\": \"demo\",\n  \"dependencies\": {\n    \"old\": \"keep\"\n  }\n}\n",
			config: map[string]any{"operation": "set", "path": "$.dependencies.wuko", "value": "v1", "missing": "create"},
			want:   "{\n  \"name\": \"demo\",\n  \"dependencies\": {\n    \"old\": \"keep\",\n    \"wuko\": \"v1\"\n  }\n}\n",
		},
		{
			name: "delete", input: "{\n  \"keep\": 1,\n  \"remove\": 2,\n  \"tail\": 3\n}\n",
			config: map[string]any{"operation": "delete", "path": "$.remove"},
			want:   "{\n  \"keep\": 1,\n  \"tail\": 3\n}\n",
		},
		{
			name: "append", input: "{\"items\": [\"a\",  \"b\"], \"keep\": true}\n",
			config: map[string]any{"operation": "append", "path": "$.items", "value": "c"},
			want:   "{\"items\": [\"a\",  \"b\", \"c\"], \"keep\": true}\n",
		},
		{
			name: "insert", input: "{\"items\": [\"a\",  \"b\"], \"keep\": true}\n",
			config: map[string]any{"operation": "insert", "path": "$.items[1]", "position": "before", "value": "x"},
			want:   "{\"items\": [\"a\",  \"x\", \"b\"], \"keep\": true}\n",
		},
		{
			name: "merge", input: "{\n  \"service\": {\n    \"image\": \"v1\",\n    \"keep\": true\n  },\n  \"tail\": 3\n}\n",
			config: map[string]any{"operation": "merge", "path": "$.service", "value": map[string]any{"image": "v2", "added": 4}},
			want:   "{\n  \"service\": {\n    \"image\": \"v2\",\n    \"keep\": true,\n    \"added\": 4\n  },\n  \"tail\": 3\n}\n",
		},
		{
			name: "rename", input: "{\n  \"old\": {\"styled\":true},\n  \"keep\": 2\n}\n",
			config: map[string]any{"operation": "rename", "path": "$.old", "name": "new"},
			want:   "{\n  \"new\": {\"styled\":true},\n  \"keep\": 2\n}\n",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			path := filepath.Join(directory, "document.json")
			if err := os.WriteFile(path, []byte(test.input), 0o640); err != nil {
				t.Fatal(err)
			}
			test.config["from"] = map[string]any{"file": "document.json"}
			runner := newRunner(t, test.config)
			if _, err := runner.Run(t.Context(), step.Request{RunDir: directory}); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(data) != test.want {
				t.Fatalf("file:\n%s\nwant:\n%s", data, test.want)
			}
		})
	}
}

func TestYAMLStructuralEditsPreserveUnrelatedBytes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, input, want string
		config            map[string]any
	}{
		{
			name: "create", input: "# top\ndependencies:\n  old: keep # inline\ntail: true\n",
			config: map[string]any{"operation": "set", "path": "$.dependencies.wuko", "value": "v1", "missing": "create"},
			want:   "# top\ndependencies:\n  old: keep # inline\n  wuko: v1\ntail: true\n",
		},
		{
			name: "create map parents", input: "name: demo\n",
			config: map[string]any{"operation": "set", "path": "$.dependencies.tools.wuko", "value": "v1", "missing": "create"},
			want:   "name: demo\ndependencies:\n  tools:\n    wuko: v1\n",
		},
		{
			name: "delete", input: "keep: 1\nremove: 2 # gone\n# standalone\ntail: 3\n",
			config: map[string]any{"operation": "delete", "path": "$.remove"},
			want:   "keep: 1\n# standalone\ntail: 3\n",
		},
		{
			name: "append", input: "items:\n  - a\n  - b\ntail: true\n",
			config: map[string]any{"operation": "append", "path": "$.items", "value": "c"},
			want:   "items:\n  - a\n  - b\n  - c\ntail: true\n",
		},
		{
			name: "insert flow", input: "items: [a, b]\ntail: true\n",
			config: map[string]any{"operation": "insert", "path": "$.items[1]", "position": "before", "value": "x"},
			want:   "items: [a, x, b]\ntail: true\n",
		},
		{
			name: "merge", input: "service:\n  image: v1\n  keep: true # styled\ntail: 3\n",
			config: map[string]any{"operation": "merge", "path": "$.service", "value": map[string]any{"image": "v2", "added": 4}},
			want:   "service:\n  image: v2\n  keep: true # styled\n  added: 4\ntail: 3\n",
		},
		{
			name: "rename", input: "'old': {styled: true}\nkeep: 2\n",
			config: map[string]any{"operation": "rename", "path": "$.old", "name": "new"},
			want:   "'new': {styled: true}\nkeep: 2\n",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			path := filepath.Join(directory, "document.yaml")
			if err := os.WriteFile(path, []byte(test.input), 0o640); err != nil {
				t.Fatal(err)
			}
			test.config["from"] = map[string]any{"file": "document.yaml"}
			runner := newRunner(t, test.config)
			if _, err := runner.Run(t.Context(), step.Request{RunDir: directory}); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(data) != test.want {
				t.Fatalf("file:\n%s\nwant:\n%s", data, test.want)
			}
		})
	}
}

func TestTOMLStructuralEditsPreserveUnrelatedBytes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, input, want string
		config            map[string]any
	}{
		{
			name: "create", input: "# top\nname = 'demo'\n\n[dependencies]\nold = 'keep' # inline\n\n[tail]\nenabled = true\n",
			config: map[string]any{"operation": "set", "path": "$.dependencies.wuko", "value": "v1", "missing": "create"},
			want:   "# top\nname = 'demo'\n\n[dependencies]\nold = 'keep' # inline\n\nwuko = \"v1\"\n[tail]\nenabled = true\n",
		},
		{
			name: "delete", input: "keep = 1\nremove = 2 # gone\n# standalone\ntail = 3\n",
			config: map[string]any{"operation": "delete", "path": "$.remove"},
			want:   "keep = 1\n# standalone\ntail = 3\n",
		},
		{
			name: "append", input: "items = ['a',  \"b\"] # style\nkeep = true\n",
			config: map[string]any{"operation": "append", "path": "$.items", "value": "c"},
			want:   "items = ['a',  \"b\", \"c\"] # style\nkeep = true\n",
		},
		{
			name: "insert", input: "items = ['a',  \"b\"] # style\nkeep = true\n",
			config: map[string]any{"operation": "insert", "path": "$.items[1]", "position": "before", "value": "x"},
			want:   "items = ['a',  \"x\", \"b\"] # style\nkeep = true\n",
		},
		{
			name: "merge", input: "[service]\nimage = 'v1'\nkeep = true # styled\n\n[tail]\nvalue = 3\n",
			config: map[string]any{"operation": "merge", "path": "$.service", "value": map[string]any{"image": "v2", "added": 4}},
			want:   "[service]\nimage = \"v2\"\nkeep = true # styled\n\nadded = 4\n[tail]\nvalue = 3\n",
		},
		{
			name: "rename", input: "'old' = { styled = true }\nkeep = 2\n",
			config: map[string]any{"operation": "rename", "path": "$.old", "name": "new"},
			want:   "new = { styled = true }\nkeep = 2\n",
		},
		{
			name: "merge subtable under array", input: "[[items]]\na = 1\n[items.sub]\nb = 2\n",
			config: map[string]any{"operation": "merge", "path": "$.items[0].sub", "value": map[string]any{"c": 3}},
			want:   "[[items]]\na = 1\n[items.sub]\nb = 2\nc = 3\n",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			path := filepath.Join(directory, "document.toml")
			if err := os.WriteFile(path, []byte(test.input), 0o640); err != nil {
				t.Fatal(err)
			}
			test.config["from"] = map[string]any{"file": "document.toml"}
			runner := newRunner(t, test.config)
			if _, err := runner.Run(t.Context(), step.Request{RunDir: directory}); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(data) != test.want {
				t.Fatalf("file:\n%s\nwant:\n%s", data, test.want)
			}
		})
	}
}

func TestFileDeleteAllArrayElementsIsStable(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, file, input, want string
	}{
		{"json", "document.json", "{\"items\":[1, 2, 3],\"keep\":true}\n", "{\"items\":[],\"keep\":true}\n"},
		{"yaml", "document.yaml", "items: [1, 2, 3]\nkeep: true\n", "items: []\nkeep: true\n"},
		{"toml", "document.toml", "items = [1, 2, 3]\nkeep = true\n", "items = []\nkeep = true\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			path := filepath.Join(directory, test.file)
			if err := os.WriteFile(path, []byte(test.input), 0o600); err != nil {
				t.Fatal(err)
			}
			runner := newRunner(t, map[string]any{
				"operation": "delete", "from": map[string]any{"file": test.file},
				"path": "$.items[*]", "result": "all",
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

func TestSetCreatesMissingMapPath(t *testing.T) {
	t.Parallel()
	runner := newRunner(t, map[string]any{
		"operation": "set", "from": map[string]any{"var": "document"},
		"path": "$.dependencies.tools.wuko", "value": "v1", "missing": "create",
	})
	result, err := runner.Run(t.Context(), step.Request{Vars: map[string]any{
		"document": map[string]any{"name": "demo"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"name": "demo", "dependencies": map[string]any{"tools": map[string]any{"wuko": "v1"}}}
	if !reflect.DeepEqual(result.Outputs["value"], want) {
		t.Fatalf("value = %#v, want %#v", result.Outputs["value"], want)
	}
	if !reflect.DeepEqual(result.Outputs["paths"], []any{"$['dependencies']['tools']['wuko']"}) {
		t.Fatalf("paths = %#v", result.Outputs["paths"])
	}
}

func TestSetCreateExpressionSeesNullCurrent(t *testing.T) {
	t.Parallel()
	runner := newRunner(t, map[string]any{
		"operation": "set", "from": map[string]any{"var": "document"},
		"path": "$.created", "expr": "current == nil ? path : 'wrong'", "missing": "create",
	})
	result, err := runner.Run(t.Context(), step.Request{Vars: map[string]any{"document": map[string]any{}}})
	if err != nil {
		t.Fatal(err)
	}
	if got := result.Outputs["value"].(map[string]any)["created"]; got != "$['created']" {
		t.Fatalf("created = %#v", got)
	}
}

func TestArrayOperationsUseOriginalLocations(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		raw  map[string]any
		want []any
	}{
		{
			name: "append",
			raw:  map[string]any{"operation": "append", "path": "$.items", "value": []any{"nested"}},
			want: []any{"a", "b", "c", []any{"nested"}},
		},
		{
			name: "insert before all",
			raw:  map[string]any{"operation": "insert", "path": "$.items[*]", "expr": "index", "position": "before", "result": "all"},
			want: []any{0, "a", 1, "b", 2, "c"},
		},
		{
			name: "insert after all",
			raw:  map[string]any{"operation": "insert", "path": "$.items[*]", "expr": "index", "position": "after", "result": "all"},
			want: []any{"a", 0, "b", 1, "c", 2},
		},
		{
			name: "delete all",
			raw:  map[string]any{"operation": "delete", "path": "$.items[0,2]", "result": "all"},
			want: []any{"b"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			test.raw["from"] = map[string]any{"var": "document"}
			runner := newRunner(t, test.raw)
			result, err := runner.Run(t.Context(), step.Request{Vars: map[string]any{
				"document": map[string]any{"items": []any{"a", "b", "c"}},
			}})
			if err != nil {
				t.Fatal(err)
			}
			got := result.Outputs["value"].(map[string]any)["items"]
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("items = %#v, want %#v", got, test.want)
			}
			if test.raw["operation"] == "delete" && len(result.Outputs["replacements"].([]any)) != 0 {
				t.Fatalf("delete replacements = %#v", result.Outputs["replacements"])
			}
		})
	}
}

func TestMergeRecursesThroughMapsAndReplacesLeaves(t *testing.T) {
	t.Parallel()
	runner := newRunner(t, map[string]any{
		"operation": "merge", "from": map[string]any{"var": "document"}, "path": "$.service",
		"value": map[string]any{"image": "v2", "limits": map[string]any{"cpu": 2}, "ports": []any{443}},
	})
	result, err := runner.Run(t.Context(), step.Request{Vars: map[string]any{"document": map[string]any{
		"service": map[string]any{"image": "v1", "limits": map[string]any{"memory": "1Gi"}, "ports": []any{80}},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"image": "v2", "limits": map[string]any{"memory": "1Gi", "cpu": 2}, "ports": []any{443}}
	if got := result.Outputs["value"].(map[string]any)["service"]; !reflect.DeepEqual(got, want) {
		t.Fatalf("service = %#v, want %#v", got, want)
	}
}

func TestRenameMovesObjectMemberWithoutOverwriting(t *testing.T) {
	t.Parallel()
	runner := newRunner(t, map[string]any{
		"operation": "rename", "from": map[string]any{"var": "document"}, "path": "$.old", "name": "new",
	})
	result, err := runner.Run(t.Context(), step.Request{Vars: map[string]any{"document": map[string]any{"old": 1}}})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result.Outputs["value"], map[string]any{"new": 1}) {
		t.Fatalf("value = %#v", result.Outputs["value"])
	}

	conflict := newRunner(t, map[string]any{
		"operation": "rename", "from": map[string]any{"var": "document"}, "path": "$.old", "name": "new",
	})
	_, err = conflict.Run(t.Context(), step.Request{Vars: map[string]any{"document": map[string]any{"old": 1, "new": 2}}})
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("error = %v, want conflict", err)
	}
}

func TestStructuralOperationValidation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, want string
		raw        map[string]any
	}{
		{"delete value", "not allowed", map[string]any{"operation": "delete", "path": "$.a", "value": 1}},
		{"append missing value", "exactly one", map[string]any{"operation": "append", "path": "$.a"}},
		{"insert position", "position", map[string]any{"operation": "insert", "path": "$.a[0]", "value": 1}},
		{"rename name", "name is required", map[string]any{"operation": "rename", "path": "$.a"}},
		{"create delete", "only allowed", map[string]any{"operation": "delete", "path": "$.a", "missing": "create"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			test.raw["from"] = map[string]any{"var": "document"}
			_, err := New(test.raw)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestCreateRejectsQueriesAndMissingArrayIndexes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, path, want string
		document         any
	}{
		{"non singular", "$.*.missing", "singular", map[string]any{"a": map[string]any{}}},
		{"missing array", "$.items[1].name", "index 1", map[string]any{"items": []any{map[string]any{}}}},
		{"infer array", "$.items[0].name", "indexes must already exist", map[string]any{}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner := newRunner(t, map[string]any{
				"operation": "set", "from": map[string]any{"var": "document"},
				"path": test.path, "value": 1, "missing": "create",
			})
			_, err := runner.Run(t.Context(), step.Request{Vars: map[string]any{"document": test.document}})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestStructuralEditsSurviveAwkwardLayouts(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, file, input, want string
		config                  map[string]any
	}{
		{
			name: "yaml rename plain key", file: "document.yaml",
			input:  "srv:\n  host: h\n",
			config: map[string]any{"operation": "rename", "path": "$.srv.host", "name": "hostname"},
			want:   "srv:\n  hostname: h\n",
		},
		{
			name: "yaml rename plain key in flow mapping", file: "document.yaml",
			input:  "srv: {host: h}\n",
			config: map[string]any{"operation": "rename", "path": "$.srv.host", "name": "hostname"},
			want:   "srv: {hostname: h}\n",
		},
		{
			name: "json merge two keys into an empty object", file: "document.json",
			input:  "{\"api\": {}}\n",
			config: map[string]any{"operation": "merge", "path": "$.api", "value": map[string]any{"x": 1, "z": 2}},
			want:   "{\"api\": {\"x\": 1, \"z\": 2}}\n",
		},
		{
			name: "yaml merge two keys into an empty flow mapping", file: "document.yaml",
			input:  "api: {}\n",
			config: map[string]any{"operation": "merge", "path": "$.api", "value": map[string]any{"x": 1, "z": 2}},
			want:   "api: {x: 1, z: 2}\n",
		},
		{
			name: "toml append past a trailing comma", file: "document.toml",
			input:  "list = [\n  \"a\",\n  \"b\",\n]\n",
			config: map[string]any{"operation": "append", "path": "$.list", "value": "c"},
			want:   "list = [\n  \"a\",\n  \"b\",\n  \"c\",\n]\n",
		},
		{
			name: "yaml append past a trailing comma", file: "document.yaml",
			input:  "list: [a, b, ]\n",
			config: map[string]any{"operation": "append", "path": "$.list", "value": "c"},
			want:   "list: [a, b, c]\n",
		},
		{
			name: "yaml merge without a trailing newline", file: "document.yaml",
			input:  "a:\n  b: 1",
			config: map[string]any{"operation": "merge", "path": "$.a", "value": map[string]any{"c": 2}},
			want:   "a:\n  b: 1\n  c: 2\n",
		},
		{
			name: "yaml delete a key sharing the sequence dash", file: "document.yaml",
			input:  "people:\n  - name: x\n    age: 3\n",
			config: map[string]any{"operation": "delete", "path": "$.people[0].name"},
			want:   "people:\n  - age: 3\n",
		},
		{
			name: "yaml insert where the dash stands alone", file: "document.yaml",
			input: "items:\n  -\n    name: x\n",
			config: map[string]any{
				"operation": "insert", "path": "$.items[0]", "position": "before",
				"value": map[string]any{"name": "z"},
			},
			want: "items:\n  - name: z\n  -\n    name: x\n",
		},
		{
			name: "toml create above a documented table", file: "document.toml",
			input:  "x = 1\n\n# describes the server\n[server]\nport = 80\n",
			config: map[string]any{"operation": "set", "path": "$.y", "value": 2, "missing": "create"},
			want:   "x = 1\n\ny = 2\n# describes the server\n[server]\nport = 80\n",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			path := filepath.Join(directory, test.file)
			if err := os.WriteFile(path, []byte(test.input), 0o640); err != nil {
				t.Fatal(err)
			}
			test.config["from"] = map[string]any{"file": test.file}
			runner := newRunner(t, test.config)
			if _, err := runner.Run(t.Context(), step.Request{RunDir: directory}); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(data) != test.want {
				t.Fatalf("file:\n%s\nwant:\n%s", data, test.want)
			}
		})
	}
}

func TestMergeWritesAddedKeysInSortedOrder(t *testing.T) {
	t.Parallel()
	tests := []struct{ file, input, want string }{
		{"document.json", "{\"api\": {\"a\": 0}}\n", "{\"api\": {\"a\": 0, \"q\": 3, \"w\": 2, \"x\": 1}}\n"},
		{"document.yaml", "api:\n  a: 0\n", "api:\n  a: 0\n  q: 3\n  w: 2\n  x: 1\n"},
	}
	for _, test := range tests {
		t.Run(test.file, func(t *testing.T) {
			directory := t.TempDir()
			path := filepath.Join(directory, test.file)
			if err := os.WriteFile(path, []byte(test.input), 0o640); err != nil {
				t.Fatal(err)
			}
			runner := newRunner(t, map[string]any{
				"operation": "merge", "from": map[string]any{"file": test.file}, "path": "$.api",
				"value": map[string]any{"x": 1, "w": 2, "q": 3},
			})
			if _, err := runner.Run(t.Context(), step.Request{RunDir: directory}); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(data) != test.want {
				t.Fatalf("file:\n%s\nwant:\n%s", data, test.want)
			}
		})
	}
}

func TestTOMLInlineTableEditsAreRefused(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		config map[string]any
	}{
		{"replace member", map[string]any{
			"operation": "merge", "path": "$.srv", "value": map[string]any{"port": 80},
		}},
		{"add member", map[string]any{
			"operation": "merge", "path": "$.srv", "value": map[string]any{"tls": true},
		}},
		{"delete member", map[string]any{"operation": "delete", "path": "$.srv.port"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			path := filepath.Join(directory, "document.toml")
			if err := os.WriteFile(path, []byte("srv = {host = \"h\", port = 8080}\n"), 0o640); err != nil {
				t.Fatal(err)
			}
			test.config["from"] = map[string]any{"file": "document.toml"}
			runner := newRunner(t, test.config)
			if _, err := runner.Run(t.Context(), step.Request{RunDir: directory}); err == nil ||
				!strings.Contains(err.Error(), "inline table") {
				t.Fatalf("error = %v, want an inline table error", err)
			}
		})
	}
}

func TestPositionAcceptsUnresolvedTemplate(t *testing.T) {
	t.Parallel()
	raw := map[string]any{
		"operation": "insert", "from": map[string]any{"var": "document"},
		"path": "$.items[0]", "value": 1, "position": "${{ inputs.direction }}",
	}
	runner, err := New(raw)
	if err != nil {
		t.Fatalf("New() = %v, want a runner that waits for the rendered position", err)
	}
	_, err = runner.Run(t.Context(), step.Request{Vars: map[string]any{"document": map[string]any{"items": []any{1}}}})
	if err == nil || !strings.Contains(err.Error(), "unresolved template") {
		t.Fatalf("error = %v, want an unresolved template error", err)
	}
}
