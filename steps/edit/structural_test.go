package edit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/up2jj/wuko/step"
)

// structuralCase edits file in a temporary run directory and compares the bytes
// left behind, which is the only way to observe the layout the patchers keep.
type structuralCase struct {
	name, input, want string
	config            map[string]any
}

func runStructuralCases(t *testing.T, file string, tests []structuralCase) {
	t.Helper()
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			path := filepath.Join(directory, file)
			if err := os.WriteFile(path, []byte(test.input), 0o640); err != nil {
				t.Fatal(err)
			}
			test.config["from"] = map[string]any{"file": file}
			runner := newRunner(t, test.config)
			if _, err := runner.Run(t.Context(), step.Request{RunDir: directory}); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(data) != test.want {
				t.Fatalf("file:\n%q\nwant:\n%q", data, test.want)
			}
		})
	}
}

func TestJSONStructuralCornerCases(t *testing.T) {
	t.Parallel()
	runStructuralCases(t, "document.json", []structuralCase{
		{
			name: "append to an inline empty array", input: "{\"a\": []}\n",
			config: map[string]any{"operation": "append", "path": "$.a", "value": 1},
			want:   "{\"a\": [1]}\n",
		},
		{
			name: "append opens a line in a block empty array", input: "{\n  \"a\": [\n  ]\n}\n",
			config: map[string]any{"operation": "append", "path": "$.a", "value": 1},
			want:   "{\n  \"a\": [\n    1\n  ]\n}\n",
		},
		{
			name: "merge fills a block empty object", input: "{\n  \"a\": {\n  }\n}\n",
			config: map[string]any{"operation": "merge", "path": "$.a", "value": map[string]any{"x": 1, "z": 2}},
			want:   "{\n  \"a\": {\n    \"x\": 1,\n    \"z\": 2\n  }\n}\n",
		},
		{
			name: "merge into the root empty object", input: "{}\n",
			config: map[string]any{"operation": "merge", "path": "$", "value": map[string]any{"x": 1}},
			want:   "{\"x\": 1}\n",
		},
		{
			name: "insert before the first element", input: "{\n  \"a\": [\n    1,\n    2\n  ]\n}\n",
			config: map[string]any{"operation": "insert", "path": "$.a[0]", "position": "before", "value": 0},
			want:   "{\n  \"a\": [\n    0,\n    1,\n    2\n  ]\n}\n",
		},
		{
			name: "insert after the last element", input: "{\n  \"a\": [\n    1,\n    2\n  ]\n}\n",
			config: map[string]any{"operation": "insert", "path": "$.a[1]", "position": "after", "value": 3},
			want:   "{\n  \"a\": [\n    1,\n    2,\n    3\n  ]\n}\n",
		},
		{
			name: "delete the first array element", input: "{\n  \"a\": [\n    1,\n    2,\n    3\n  ]\n}\n",
			config: map[string]any{"operation": "delete", "path": "$.a[0]"},
			want:   "{\n  \"a\": [\n    2,\n    3\n  ]\n}\n",
		},
		{
			name: "delete the last array element", input: "{\n  \"a\": [\n    1,\n    2,\n    3\n  ]\n}\n",
			config: map[string]any{"operation": "delete", "path": "$.a[2]"},
			want:   "{\n  \"a\": [\n    1,\n    2\n  ]\n}\n",
		},
		{
			name: "a compact separator stays compact", input: "{\"a\":1}\n",
			config: map[string]any{"operation": "merge", "path": "$", "value": map[string]any{"b": 2}},
			want:   "{\"a\":1, \"b\":2}\n",
		},
		{
			name: "tab indentation is reused", input: "{\n\t\"a\": 1\n}\n",
			config: map[string]any{"operation": "merge", "path": "$", "value": map[string]any{"b": 2}},
			want:   "{\n\t\"a\": 1,\n\t\"b\": 2\n}\n",
		},
		{
			name: "carriage returns survive a delete", input: "{\r\n  \"a\": 1,\r\n  \"b\": 2\r\n}\r\n",
			config: map[string]any{"operation": "delete", "path": "$.b"},
			want:   "{\r\n  \"a\": 1\r\n}\r\n",
		},
		{
			name: "rename escapes the new key", input: "{\"a\\\"b\": 1}\n",
			config: map[string]any{"operation": "rename", "path": "$['a\"b']", "name": "c/d"},
			want:   "{\"c/d\": 1}\n",
		},
		{
			name: "rename inside a block object", input: "{\n  \"a\": 1,\n  \"b\": 2\n}\n",
			config: map[string]any{"operation": "rename", "path": "$.a", "name": "c"},
			want:   "{\n  \"c\": 1,\n  \"b\": 2\n}\n",
		},
		{
			name: "non-ascii keys are addressable", input: "{\"ą\": 1, \"b\": 2}\n",
			config: map[string]any{"operation": "delete", "path": "$['ą']"},
			want:   "{\"b\": 2}\n",
		},
		{
			name: "the root array appends", input: "[1, 2]\n",
			config: map[string]any{"operation": "append", "path": "$", "value": 3},
			want:   "[1, 2, 3]\n",
		},
		{
			name: "the root array deletes", input: "[1, 2]\n",
			config: map[string]any{"operation": "delete", "path": "$[0]"},
			want:   "[2]\n",
		},
		{
			name: "merge recurses into a nested object", input: "{\n  \"a\": {\n    \"b\": {\n      \"c\": 1\n    }\n  }\n}\n",
			config: map[string]any{"operation": "merge", "path": "$.a", "value": map[string]any{"b": map[string]any{"d": 2}}},
			want:   "{\n  \"a\": {\n    \"b\": {\n      \"c\": 1,\n      \"d\": 2\n    }\n  }\n}\n",
		},
		{
			name: "append an object to an array of objects", input: "{\n  \"a\": [\n    {\"x\": 1}\n  ]\n}\n",
			config: map[string]any{"operation": "append", "path": "$.a", "value": map[string]any{"z": 2}},
			want:   "{\n  \"a\": [\n    {\"x\": 1},\n    {\"z\":2}\n  ]\n}\n",
		},
		{
			name: "an untouched integer keeps every digit", input: "{\"a\": 10000000000000000000000, \"b\": 1}\n",
			config: map[string]any{"operation": "delete", "path": "$.b"},
			want:   "{\"a\": 10000000000000000000000}\n",
		},
		{
			name: "an untouched float keeps its exponent", input: "{\"a\": 1.0e10, \"b\": 1}\n",
			config: map[string]any{"operation": "delete", "path": "$.b"},
			want:   "{\"a\": 1.0e10}\n",
		},
		{
			name: "append reaches every selected array", input: "{\"a\": [1], \"b\": [2]}\n",
			config: map[string]any{"operation": "append", "path": "$.*", "value": 9, "result": "all"},
			want:   "{\"a\": [1, 9], \"b\": [2, 9]}\n",
		},
		{
			name: "rename reaches every selected member", input: "{\"a\": {\"x\": 1}, \"b\": {\"x\": 2}}\n",
			config: map[string]any{"operation": "rename", "path": "$.*.x", "name": "z", "result": "all"},
			want:   "{\"a\": {\"z\": 1}, \"b\": {\"z\": 2}}\n",
		},
	})
}

func TestYAMLStructuralCornerCases(t *testing.T) {
	t.Parallel()
	runStructuralCases(t, "document.yaml", []structuralCase{
		{
			name: "a neighbour keeps its inline comment", input: "a: 1 # keep\nb: 2 # drop\nc: 3\n",
			config: map[string]any{"operation": "delete", "path": "$.b"},
			want:   "a: 1 # keep\nc: 3\n",
		},
		{
			name: "a standalone comment survives", input: "a: 1\n\n# section\nb: 2\nc: 3\n",
			config: map[string]any{"operation": "delete", "path": "$.c"},
			want:   "a: 1\n\n# section\nb: 2\n",
		},
		{
			name: "a literal block neighbour is untouched", input: "script: |\n  line one\n  line two\nb: 2\n",
			config: map[string]any{"operation": "delete", "path": "$.b"},
			want:   "script: |\n  line one\n  line two\n",
		},
		{
			name: "deleting a key takes its whole literal block", input: "script: |\n  line one\n  line two\nb: 2\n",
			config: map[string]any{"operation": "delete", "path": "$.script"},
			want:   "b: 2\n",
		},
		{
			name: "renaming a key leaves its literal block", input: "script: |\n  line one\nb: 2\n",
			config: map[string]any{"operation": "rename", "path": "$.script", "name": "run"},
			want:   "run: |\n  line one\nb: 2\n",
		},
		{
			name: "a quoted key keeps its quotes", input: "\"old\": 1\n",
			config: map[string]any{"operation": "rename", "path": "$.old", "name": "new"},
			want:   "\"new\": 1\n",
		},
		{
			name: "a new key that needs quoting gets them", input: "a: 1\n",
			config: map[string]any{"operation": "rename", "path": "$.a", "name": "x: y"},
			want:   "'x: y': 1\n",
		},
		{
			name: "padding around a key is preserved", input: "a  : 1\nb: 2\n",
			config: map[string]any{"operation": "rename", "path": "$.a", "name": "c"},
			want:   "c  : 1\nb: 2\n",
		},
		{
			name: "a four-space sequence keeps its indent", input: "items:\n    - a\n    - b\n",
			config: map[string]any{"operation": "append", "path": "$.items", "value": "c"},
			want:   "items:\n    - a\n    - b\n    - c\n",
		},
		{
			name: "a flush sequence stays flush", input: "items:\n- a\n- b\n",
			config: map[string]any{"operation": "append", "path": "$.items", "value": "c"},
			want:   "items:\n- a\n- b\n- c\n",
		},
		{
			name: "the root sequence appends", input: "- a\n- b\n",
			config: map[string]any{"operation": "append", "path": "$", "value": "c"},
			want:   "- a\n- b\n- c\n",
		},
		{
			name: "insert before the first element", input: "items:\n  - a\n  - b\n",
			config: map[string]any{"operation": "insert", "path": "$.items[0]", "position": "before", "value": "z"},
			want:   "items:\n  - z\n  - a\n  - b\n",
		},
		{
			name: "delete the middle element", input: "items:\n  - a\n  - b\n  - c\n",
			config: map[string]any{"operation": "delete", "path": "$.items[1]"},
			want:   "items:\n  - a\n  - c\n",
		},
		{
			name: "comments between elements stay put", input: "items:\n  # first\n  - a\n  # second\n  - b\n",
			config: map[string]any{"operation": "delete", "path": "$.items[1]"},
			want:   "items:\n  # first\n  - a\n  # second\n",
		},
		{
			name: "an element appends above a trailing comment", input: "items:\n  - a\n  # note\n",
			config: map[string]any{"operation": "append", "path": "$.items", "value": "b"},
			want:   "items:\n  - a\n  - b\n  # note\n",
		},
		{
			name: "append to an empty flow sequence", input: "items: []\n",
			config: map[string]any{"operation": "append", "path": "$.items", "value": "a"},
			want:   "items: [a]\n",
		},
		{
			name: "delete the first flow mapping member", input: "api: {a: 1, b: 2}\n",
			config: map[string]any{"operation": "delete", "path": "$.api.a"},
			want:   "api: {b: 2}\n",
		},
		{
			name: "delete the last flow mapping member", input: "api: {a: 1, b: 2}\n",
			config: map[string]any{"operation": "delete", "path": "$.api.b"},
			want:   "api: {a: 1}\n",
		},
		{
			name: "append into a nested sequence of mappings", input: "jobs:\n  - name: a\n    steps:\n      - run: x\n",
			config: map[string]any{"operation": "append", "path": "$.jobs[0].steps", "value": map[string]any{"run": "z"}},
			want:   "jobs:\n  - name: a\n    steps:\n      - run: x\n      - run: z\n",
		},
		{
			name:  "insert into a deeply nested sequence",
			input: "jobs:\n  build:\n    steps:\n      - run: x\n      - run: z\n",
			config: map[string]any{
				"operation": "insert", "path": "$.jobs.build.steps[1]", "position": "before",
				"value": map[string]any{"run": "w"},
			},
			want: "jobs:\n  build:\n    steps:\n      - run: x\n      - run: w\n      - run: z\n",
		},
		{
			name: "merge writes a nested map as a block", input: "a:\n  b: 1\n",
			config: map[string]any{"operation": "merge", "path": "$.a", "value": map[string]any{"c": map[string]any{"d": 2}}},
			want:   "a:\n  b: 1\n  c:\n    d: 2\n",
		},
		{
			name: "merge keeps the replaced scalar's style", input: "a: 'x'\n",
			config: map[string]any{"operation": "merge", "path": "$", "value": map[string]any{"a": "z"}},
			want:   "a: 'z'\n",
		},
		{
			name: "a leading document marker is kept", input: "---\na: 1\nb: 2\n",
			config: map[string]any{"operation": "delete", "path": "$.b"},
			want:   "---\na: 1\n",
		},
		{
			name: "carriage returns survive a delete", input: "a: 1\r\nb: 2\r\n",
			config: map[string]any{"operation": "delete", "path": "$.b"},
			want:   "a: 1\r\n",
		},
		{
			name: "append terminates a file with no final newline", input: "items:\n  - a",
			config: map[string]any{"operation": "append", "path": "$.items", "value": "b"},
			want:   "items:\n  - a\n  - b\n",
		},
		{
			name: "create terminates a file with no final newline", input: "a: 1",
			config: map[string]any{"operation": "set", "path": "$.b", "value": 2, "missing": "create"},
			want:   "a: 1\nb: 2\n",
		},
		{
			name: "delete a later key under a sequence dash", input: "people:\n  - name: x\n    age: 3\n",
			config: map[string]any{"operation": "delete", "path": "$.people[0].age"},
			want:   "people:\n  - name: x\n",
		},
		{
			name: "rename a key under a sequence dash", input: "items:\n  - name: x\n",
			config: map[string]any{"operation": "rename", "path": "$.items[0].name", "name": "id"},
			want:   "items:\n  - id: x\n",
		},
		{
			name: "insert after an element written on its dash line", input: "items:\n  - name: x\n",
			config: map[string]any{
				"operation": "insert", "path": "$.items[0]", "position": "after",
				"value": map[string]any{"name": "z"},
			},
			want: "items:\n  - name: x\n  - name: z\n",
		},
		{
			name: "append a multi-key mapping to a sequence", input: "items:\n  - a\n",
			config: map[string]any{"operation": "append", "path": "$.items", "value": map[string]any{"k": 1, "l": 2}},
			want:   "items:\n  - a\n  - k: 1\n    l: 2\n",
		},
		{
			name: "append reaches every selected sequence", input: "a:\n  - 1\nb:\n  - 2\n",
			config: map[string]any{"operation": "append", "path": "$.*", "value": 9, "result": "all"},
			want:   "a:\n  - 1\n  - 9\nb:\n  - 2\n  - 9\n",
		},
	})
}

func TestTOMLStructuralCornerCases(t *testing.T) {
	t.Parallel()
	runStructuralCases(t, "document.toml", []structuralCase{
		{
			name: "delete inside a table", input: "[server]\nhost = \"h\"\nport = 80\n",
			config: map[string]any{"operation": "delete", "path": "$.server.port"},
			want:   "[server]\nhost = \"h\"\n",
		},
		{
			name: "a table may be left empty", input: "[server]\nhost = \"h\"\n",
			config: map[string]any{"operation": "delete", "path": "$.server.host"},
			want:   "[server]\n",
		},
		{
			name: "create joins its table, not the next one", input: "[server]\nhost = \"h\"\n\n[client]\nx = 1\n",
			config: map[string]any{"operation": "set", "path": "$.server.port", "value": 80, "missing": "create"},
			want:   "[server]\nhost = \"h\"\nport = 80\n\n[client]\nx = 1\n",
		},
		{
			name: "create at the root stays above the first header", input: "a = 1\n[server]\nhost = \"h\"\n",
			config: map[string]any{"operation": "set", "path": "$.b", "value": 2, "missing": "create"},
			want:   "a = 1\nb = 2\n[server]\nhost = \"h\"\n",
		},
		{
			name: "create into an empty table", input: "[a]\n\n[b]\nx = 1\n",
			config: map[string]any{"operation": "set", "path": "$.a.y", "value": 2, "missing": "create"},
			want:   "[a]\ny = 2\n\n[b]\nx = 1\n",
		},
		{
			name: "create appends when the file ends in a comment", input: "a = 1\n# trailing note\n",
			config: map[string]any{"operation": "set", "path": "$.b", "value": 2, "missing": "create"},
			want:   "a = 1\n# trailing note\nb = 2\n",
		},
		{
			name: "create terminates a file with no final newline", input: "a = 1",
			config: map[string]any{"operation": "set", "path": "$.b", "value": 2, "missing": "create"},
			want:   "a = 1\nb = 2\n",
		},
		{
			name: "create inside a dotted table header", input: "[a.b]\nx = 1\n",
			config: map[string]any{"operation": "set", "path": "$.a.b.y", "value": 2, "missing": "create"},
			want:   "[a.b]\nx = 1\ny = 2\n",
		},
		{
			name: "delete one half of a dotted key", input: "a.b = 1\na.c = 2\n",
			config: map[string]any{"operation": "delete", "path": "$.a.b"},
			want:   "a.c = 2\n",
		},
		{
			name: "rename replaces only the last dotted part", input: "a.b = 1\n",
			config: map[string]any{"operation": "rename", "path": "$.a.b", "name": "z"},
			want:   "a.z = 1\n",
		},
		{
			name: "rename keeps quoting a key that needs it", input: "\"a b\" = 1\n",
			config: map[string]any{"operation": "rename", "path": "$['a b']", "name": "c d"},
			want:   "\"c d\" = 1\n",
		},
		{
			name: "rename drops quotes a bare key does not need", input: "\"a b\" = 1\n",
			config: map[string]any{"operation": "rename", "path": "$['a b']", "name": "cd"},
			want:   "cd = 1\n",
		},
		{
			name: "delete inside an array of tables", input: "[[job]]\nname = \"a\"\n\n[[job]]\nname = \"b\"\n",
			config: map[string]any{"operation": "delete", "path": "$.job[1].name"},
			want:   "[[job]]\nname = \"a\"\n\n[[job]]\n",
		},
		{
			name: "create inside an array of tables", input: "[[job]]\nname = \"a\"\n\n[[job]]\nname = \"b\"\n",
			config: map[string]any{"operation": "set", "path": "$.job[0].env", "value": "dev", "missing": "create"},
			want:   "[[job]]\nname = \"a\"\nenv = \"dev\"\n\n[[job]]\nname = \"b\"\n",
		},
		{
			name: "rename inside an array of tables", input: "[[job]]\nname = \"a\"\n",
			config: map[string]any{"operation": "rename", "path": "$.job[0].name", "name": "id"},
			want:   "[[job]]\nid = \"a\"\n",
		},
		{
			name: "append to an empty array", input: "tags = []\n",
			config: map[string]any{"operation": "append", "path": "$.tags", "value": "a"},
			want:   "tags = [\"a\"]\n",
		},
		{
			name: "append to a block array without a trailing comma", input: "tags = [\n  \"a\",\n  \"b\"\n]\n",
			config: map[string]any{"operation": "append", "path": "$.tags", "value": "c"},
			want:   "tags = [\n  \"a\",\n  \"b\",\n  \"c\"\n]\n",
		},
		{
			name: "insert opens its own line in a block array", input: "tags = [\n  \"a\",\n  \"b\"\n]\n",
			config: map[string]any{"operation": "insert", "path": "$.tags[1]", "position": "before", "value": "z"},
			want:   "tags = [\n  \"a\",\n  \"z\",\n  \"b\"\n]\n",
		},
		{
			name: "delete the first element of a block array", input: "tags = [\n  \"a\",\n  \"b\"\n]\n",
			config: map[string]any{"operation": "delete", "path": "$.tags[0]"},
			want:   "tags = [\n  \"b\"\n]\n",
		},
		{
			name: "delete the last element of a block array", input: "tags = [\n  \"a\",\n  \"b\"\n]\n",
			config: map[string]any{"operation": "delete", "path": "$.tags[1]"},
			want:   "tags = [\n  \"a\"\n]\n",
		},
		{
			name: "a comment inside an array is left alone", input: "tags = [\n  \"a\", # first\n  \"b\"\n]\n",
			config: map[string]any{"operation": "append", "path": "$.tags", "value": "c"},
			want:   "tags = [\n  \"a\", # first\n  \"b\",\n  \"c\"\n]\n",
		},
		{
			name: "a bracket inside a string does not close the array", input: "tags = [\"a]b\", \"c\"]\n",
			config: map[string]any{"operation": "append", "path": "$.tags", "value": "d"},
			want:   "tags = [\"a]b\", \"c\", \"d\"]\n",
		},
		{
			name: "a hash inside a string is not a comment", input: "a = \"x # y\"\nb = 1\n",
			config: map[string]any{"operation": "delete", "path": "$.b"},
			want:   "a = \"x # y\"\n",
		},
		{
			name: "set keeps the comment trailing the value", input: "a = 1 # note\nb = 2\n",
			config: map[string]any{"operation": "set", "path": "$.a", "value": 5},
			want:   "a = 5 # note\nb = 2\n",
		},
		{
			name: "delete takes the comment trailing the value", input: "a = 1 # note\nb = 2\n",
			config: map[string]any{"operation": "delete", "path": "$.a"},
			want:   "b = 2\n",
		},
		{
			name: "an untouched literal string is not re-quoted", input: "a = 'x\\ny'\nb = 1\n",
			config: map[string]any{"operation": "delete", "path": "$.b"},
			want:   "a = 'x\\ny'\n",
		},
		{
			name: "untouched dates and floats keep their spelling", input: "a = 1979-05-27T07:32:00Z\nb = 3.14\nc = 1\n",
			config: map[string]any{"operation": "delete", "path": "$.c"},
			want:   "a = 1979-05-27T07:32:00Z\nb = 3.14\n",
		},
		{
			name: "merge replaces and adds within a table", input: "[server]\nhost = \"h\"\n",
			config: map[string]any{"operation": "merge", "path": "$.server", "value": map[string]any{"port": 80, "host": "i"}},
			want:   "[server]\nhost = \"i\"\nport = 80\n",
		},
		{
			name: "merge writes an added map as an inline table", input: "[server]\nhost = \"h\"\n",
			config: map[string]any{"operation": "merge", "path": "$.server", "value": map[string]any{"tls": map[string]any{"min": 2}}},
			want:   "[server]\nhost = \"h\"\ntls = {\"min\" = 2}\n",
		},
		{
			name: "a comment above a key is kept", input: "# lead\na = 1\nb = 2\n",
			config: map[string]any{"operation": "delete", "path": "$.b"},
			want:   "# lead\na = 1\n",
		},
		{
			name: "append reaches every selected array", input: "a = [1]\nb = [2]\n",
			config: map[string]any{"operation": "append", "path": "$.*", "value": 9, "result": "all"},
			want:   "a = [1, 9]\nb = [2, 9]\n",
		},
	})
}

// Removing the last entry of a block collection cannot simply drop its lines:
// the key introducing it would be left with no value, or the document with no
// content at all. Each patcher rewrites the container as an empty collection.
func TestDeletingEveryEntryLeavesAnEmptyCollection(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, file, input, want string
		config                  map[string]any
	}{
		{
			name: "json object", file: "document.json", input: "{\n  \"a\": 1\n}\n",
			config: map[string]any{"operation": "delete", "path": "$.a"}, want: "{}\n",
		},
		{
			name: "json object in one pass", file: "document.json", input: "{\n  \"a\": 1,\n  \"b\": 2\n}\n",
			config: map[string]any{"operation": "delete", "path": "$.*", "result": "all"}, want: "{}\n",
		},
		{
			name: "json nested object", file: "document.json", input: "{\n  \"a\": {\n    \"b\": 1\n  }\n}\n",
			config: map[string]any{"operation": "delete", "path": "$.a.b"}, want: "{\n  \"a\": {}\n}\n",
		},
		{
			name: "json array", file: "document.json", input: "{\"a\": [1, 2]}\n",
			config: map[string]any{"operation": "delete", "path": "$.a[*]", "result": "all"}, want: "{\"a\": []}\n",
		},
		{
			name: "yaml root mapping", file: "document.yaml", input: "a: 1\n",
			config: map[string]any{"operation": "delete", "path": "$.a"}, want: "{}\n",
		},
		{
			name: "yaml root mapping in one pass", file: "document.yaml", input: "a: 1\nb: 2\n",
			config: map[string]any{"operation": "delete", "path": "$.*", "result": "all"}, want: "{}\n",
		},
		{
			name: "yaml nested mapping", file: "document.yaml", input: "a:\n  b: 1\n  c: 2\nd: 3\n",
			config: map[string]any{"operation": "delete", "path": "$.a.*", "result": "all"}, want: "a: {}\nd: 3\n",
		},
		{
			name: "yaml sequence", file: "document.yaml", input: "items:\n  - a\n",
			config: map[string]any{"operation": "delete", "path": "$.items[0]"}, want: "items: []\n",
		},
		{
			name: "yaml sequence in one pass", file: "document.yaml", input: "items:\n  - a\n  - b\n",
			config: map[string]any{"operation": "delete", "path": "$.items[*]", "result": "all"}, want: "items: []\n",
		},
		{
			name: "yaml root sequence", file: "document.yaml", input: "- a\n- b\n",
			config: map[string]any{"operation": "delete", "path": "$[*]", "result": "all"}, want: "[]\n",
		},
		{
			name: "yaml mapping under a sequence dash", file: "document.yaml", input: "people:\n  - name: x\n",
			config: map[string]any{"operation": "delete", "path": "$.people[0].name"}, want: "people:\n  - {}\n",
		},
		{
			name: "yaml flow mapping", file: "document.yaml", input: "api: {a: 1}\n",
			config: map[string]any{"operation": "delete", "path": "$.api.a"}, want: "api: {}\n",
		},
		{
			// A comment between the key and its block keeps the collection on
			// its own line: pulling it up would land inside the comment.
			name: "yaml sequence below a comment", file: "document.yaml", input: "items:\n  # note\n  - a\n",
			config: map[string]any{"operation": "delete", "path": "$.items[0]"}, want: "items:\n  # note\n  []\n",
		},
		{
			name: "toml array", file: "document.toml", input: "tags = [\"a\"]\n",
			config: map[string]any{"operation": "delete", "path": "$.tags[0]"}, want: "tags = []\n",
		},
		{
			name: "toml array in one pass", file: "document.toml", input: "tags = [\"a\", \"b\"]\n",
			config: map[string]any{"operation": "delete", "path": "$.tags[*]", "result": "all"}, want: "tags = []\n",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runStructuralCases(t, test.file, []structuralCase{
				{name: test.name, input: test.input, want: test.want, config: test.config},
			})
		})
	}
}

// A value-preserving edit must leave the file byte for byte as it found it, so
// re-running a workflow does not churn the working tree.
func TestStructuralEditsRewriteNothingWhenTheValueMatches(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, file, input string
		config            map[string]any
	}{
		{"json set", "document.json", "{\n  \"a\": 1\n}\n", map[string]any{"operation": "set", "path": "$.a", "value": 1}},
		{"yaml merge", "document.yaml", "a:\n  b: 1\n", map[string]any{"operation": "merge", "path": "$.a", "value": map[string]any{"b": 1}}},
		{"yaml create existing", "document.yaml", "a: 1\n", map[string]any{"operation": "set", "path": "$.a", "value": 1, "missing": "create"}},
		{"toml set", "document.toml", "a = 1 # note\n", map[string]any{"operation": "set", "path": "$.a", "value": 1}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runStructuralCases(t, test.file, []structuralCase{
				{name: test.name, input: test.input, want: test.input, config: test.config},
			})
		})
	}
}

// Running the same edit again must reach the same bytes, not compound them.
func TestStructuralEditsSettleAfterOneApplication(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, file, input, want string
		config                  map[string]any
	}{
		{
			name: "json merge", file: "document.json", input: "{\n  \"a\": {\n    \"b\": 1\n  }\n}\n",
			config: map[string]any{"operation": "merge", "path": "$.a", "value": map[string]any{"c": 2}},
			want:   "{\n  \"a\": {\n    \"b\": 1,\n    \"c\": 2\n  }\n}\n",
		},
		{
			name: "yaml create", file: "document.yaml", input: "a:\n  b: 1\n",
			config: map[string]any{"operation": "set", "path": "$.a.c", "value": 2, "missing": "create"},
			want:   "a:\n  b: 1\n  c: 2\n",
		},
		{
			name: "toml create", file: "document.toml", input: "[server]\nhost = \"h\"\n",
			config: map[string]any{"operation": "set", "path": "$.server.port", "value": 80, "missing": "create"},
			want:   "[server]\nhost = \"h\"\nport = 80\n",
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
			for pass := range 2 {
				if _, err := runner.Run(t.Context(), step.Request{RunDir: directory}); err != nil {
					t.Fatalf("pass %d: %v", pass+1, err)
				}
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if string(data) != test.want {
					t.Fatalf("pass %d file:\n%q\nwant:\n%q", pass+1, data, test.want)
				}
			}
		})
	}
}

func TestStructuralEditsRefuseUnsupportedTargets(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, file, input, want string
		config                  map[string]any
	}{
		{
			name: "yaml alias", file: "document.yaml", input: "base: &b\n  a: 1\nuse: *b\n",
			config: map[string]any{"operation": "delete", "path": "$.use.a"},
			want:   "aliases is not supported",
		},
		{
			name: "toml nested array element", file: "document.toml", input: "a = [[1, 2], [3]]\n",
			config: map[string]any{"operation": "append", "path": "$.a[1]", "value": 4},
			want:   "cannot safely locate array",
		},
		{
			name: "rename onto an existing key", file: "document.json", input: "{\"a\": 1, \"b\": 2}\n",
			config: map[string]any{"operation": "rename", "path": "$.a", "name": "b"},
			want:   "already exists",
		},
		{
			name: "delete the document root", file: "document.json", input: "{\"a\": 1}\n",
			config: map[string]any{"operation": "delete", "path": "$"},
			want:   "cannot target the document root",
		},
		{
			name: "append to something that is not an array", file: "document.yaml", input: "a: 1\n",
			config: map[string]any{"operation": "append", "path": "$.a", "value": 2},
			want:   "append",
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
			_, err := runner.Run(t.Context(), step.Request{RunDir: directory})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want it to mention %q", err, test.want)
			}
			data, readErr := os.ReadFile(path)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if string(data) != test.input {
				t.Fatalf("a refused edit rewrote the file:\n%q", data)
			}
		})
	}
}
