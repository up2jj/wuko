package keyvalue

import (
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/up2jj/wuko/step"
)

func TestConfigValidation(t *testing.T) {
	tests := []struct {
		name string
		raw  map[string]any
		want string
	}{
		{"valid get", config("get", "key", nil, false), ""},
		{"valid null set", config("set", "key", nil, true), ""},
		{"valid list", config("list", "", nil, false), ""},
		{"valid templated selectors", map[string]any{"operation": "{{ .vars.operation }}", "scope": "{{ .vars.scope }}", "store": "prefs-{{ .vars.suffix }}"}, ""},
		{"missing operation", map[string]any{"scope": "local", "store": "prefs"}, "operation is required"},
		{"bad operation", map[string]any{"operation": "purge", "scope": "local", "store": "prefs"}, "operation must be"},
		{"valid clear", map[string]any{"operation": "clear", "scope": "local", "store": "prefs"}, ""},
		{"clear with key", config("clear", "key", nil, false), "key is not allowed for clear"},
		{"get with default", map[string]any{"operation": "get", "scope": "local", "store": "prefs", "key": "theme", "default": "light"}, ""},
		{"set with default", map[string]any{"operation": "set", "scope": "local", "store": "prefs", "key": "k", "value": 1, "default": 2}, "default is not allowed for set"},
		{"get with invalid default", map[string]any{"operation": "get", "scope": "local", "store": "prefs", "key": "k", "default": math.NaN()}, "default is not JSON-compatible"},
		{"list with prefix", map[string]any{"operation": "list", "scope": "local", "store": "prefs", "prefix": "build-"}, ""},
		{"get with prefix", map[string]any{"operation": "get", "scope": "local", "store": "prefs", "key": "k", "prefix": "build-"}, "prefix is not allowed for get"},
		{"missing scope", map[string]any{"operation": "list", "store": "prefs"}, "scope is required"},
		{"blank scope", map[string]any{"operation": "list", "scope": " ", "store": "prefs"}, "scope must not be empty"},
		{"blank store", map[string]any{"operation": "list", "scope": "local", "store": ""}, "store must not be empty"},
		{"blank operation", map[string]any{"operation": "", "scope": "local", "store": "prefs"}, "operation must not be empty"},
		{"bad scope", map[string]any{"operation": "list", "scope": "shared", "store": "prefs"}, "scope must be"},
		{"bad static scope with templated store", map[string]any{"operation": "list", "scope": "shared", "store": "{{ .vars.store }}"}, "scope must be"},
		{"bad store", map[string]any{"operation": "list", "scope": "local", "store": "../prefs"}, "invalid store name"},
		{"reserved store", map[string]any{"operation": "list", "scope": "local", "store": "changed"}, `store name "changed" is reserved`},
		{"reserved store ignoring case", map[string]any{"operation": "get", "scope": "global", "store": "Picker", "key": "state"}, "is reserved"},
		{"bad static store with templated scope", map[string]any{"operation": "list", "scope": "{{ .vars.scope }}", "store": "../prefs"}, "invalid store name"},
		{"get missing key", config("get", "", nil, false), "key is required"},
		{"get with value", config("get", "key", true, true), "value is not allowed"},
		{"set missing value and expr", config("set", "key", nil, false), "exactly one of value or expr is required"},
		{"set with value and expr", withExpr(config("set", "key", "dark", true), "1 + 1"), "exactly one of value or expr is required"},
		{"valid set expr", withExpr(config("set", "key", nil, false), "steps.load.value + 1"), ""},
		{"set empty expr", withExpr(config("set", "key", nil, false), "  "), "expr must not be empty"},
		{"set uncompilable expr", withExpr(config("set", "key", nil, false), "vars.count +"), "compiling expr"},
		{"set templated expr", withExpr(config("set", "key", nil, false), "{{ .vars.expression }}"), ""},
		{"get with expr", withExpr(config("get", "key", nil, false), "1"), "expr is not allowed for get"},
		{"valid update", withExpr(config("update", "key", nil, false), "found ? current + 1 : 1"), ""},
		{"update missing expr", config("update", "key", nil, false), "expr is required for update"},
		{"update missing key", withExpr(config("update", "", nil, false), "1"), "key is required for update"},
		{"update with value", withExpr(config("update", "key", 1, true), "1"), "value is not allowed for update"},
		{"delete with expr", withExpr(config("delete", "key", nil, false), "1"), "expr is not allowed for delete"},
		{"list with expr", withExpr(config("list", "", nil, false), "1"), "expr is not allowed for list"},
		{"set invalid value", config("set", "key", math.NaN(), true), "value is not JSON-compatible"},
		{"delete with value", config("delete", "key", true, true), "value is not allowed"},
		{"list with key", config("list", "key", nil, false), "key is not allowed"},
		{"valid variable", map[string]any{"operation": "list", "scope": "local", "store": "prefs", "variable": "entries"}, ""},
		{"empty variable", map[string]any{"operation": "list", "scope": "local", "store": "prefs", "variable": " "}, "variable must not be empty"},
		{"unknown field", map[string]any{"operation": "list", "scope": "local", "store": "prefs", "extra": true}, "field extra not found"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := New(tt.raw)
			if tt.want == "" && err != nil {
				t.Fatal(err)
			}
			if tt.want != "" && (err == nil || !strings.Contains(err.Error(), tt.want)) {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestOperationsReturnRuntimeNumbers(t *testing.T) {
	request := step.Request{LocalValueDir: t.TempDir()}
	run := func(raw map[string]any) step.Result {
		t.Helper()
		runner, err := New(raw)
		if err != nil {
			t.Fatal(err)
		}
		result, err := runner.Run(t.Context(), request)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}

	value := map[string]any{"integer": 3, "decimal": 1.5, "items": []any{4}}
	set := run(config("set", "numbers", value, true)).Outputs["value"]
	want := map[string]any{"integer": int64(3), "decimal": 1.5, "items": []any{int64(4)}}
	if !reflect.DeepEqual(set, want) {
		t.Fatalf("set value = %#v", set)
	}
	get := run(config("get", "numbers", nil, false)).Outputs["value"]
	if !reflect.DeepEqual(get, want) {
		t.Fatalf("get value = %#v", get)
	}
	entries := run(config("list", "", nil, false)).Outputs["entries"].([]any)
	if !reflect.DeepEqual(entries[0].(map[string]any)["value"], want) {
		t.Fatalf("list entries = %#v", entries)
	}
	deleted := run(config("delete", "numbers", nil, false)).Outputs["value"]
	if !reflect.DeepEqual(deleted, want) {
		t.Fatalf("deleted value = %#v", deleted)
	}
}

func TestOperations(t *testing.T) {
	request := step.Request{LocalValueDir: t.TempDir(), GlobalValueDir: t.TempDir()}
	run := func(raw map[string]any) step.Result {
		t.Helper()
		runner, err := New(raw)
		if err != nil {
			t.Fatal(err)
		}
		result, err := runner.Run(t.Context(), request)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	run(config("set", "theme", "dark", true))
	run(config("set", "nothing", nil, true))
	got := run(config("get", "theme", nil, false)).Outputs
	if !reflect.DeepEqual(got, map[string]any{"value": "dark", "found": true}) {
		t.Fatalf("get outputs = %#v", got)
	}
	missing := run(config("get", "missing", nil, false)).Outputs
	if !reflect.DeepEqual(missing, map[string]any{"value": nil, "found": false}) {
		t.Fatalf("missing outputs = %#v", missing)
	}
	entries := run(config("list", "", nil, false)).Outputs["entries"].([]any)
	if len(entries) != 2 || entries[0].(map[string]any)["key"] != "nothing" || entries[1].(map[string]any)["key"] != "theme" {
		t.Fatalf("entries = %#v", entries)
	}
	deleted := run(config("delete", "theme", nil, false)).Outputs
	if !reflect.DeepEqual(deleted, map[string]any{"value": "dark", "deleted": true}) {
		t.Fatalf("delete outputs = %#v", deleted)
	}
}

func TestValidateDoesNotTouchFilesystem(t *testing.T) {
	root := t.TempDir() + "/not-created"
	runner, err := New(config("list", "", nil, false))
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.(step.Validator).Validate(t.Context(), step.Request{LocalValueDir: root}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("validation touched storage root: %v", err)
	}
}

func TestReadOperationsDoNotTouchFilesystem(t *testing.T) {
	root := t.TempDir() + "/not-created"
	request := step.Request{LocalValueDir: root}
	for _, raw := range []map[string]any{config("get", "missing", nil, false), config("list", "", nil, false)} {
		runner, err := New(raw)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := runner.Run(t.Context(), request); err != nil {
			t.Fatalf("%s: %v", raw["operation"], err)
		}
		if _, err := os.Stat(root); !os.IsNotExist(err) {
			t.Fatalf("%s created storage root: %v", raw["operation"], err)
		}
	}
}

func TestLocalUnavailable(t *testing.T) {
	runner, err := New(config("list", "", nil, false))
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.Run(t.Context(), step.Request{GlobalValueDir: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "local key-value storage is unavailable") {
		t.Fatalf("error = %v", err)
	}
}

func TestSetExprKeepsResultTypes(t *testing.T) {
	request := step.Request{
		LocalValueDir: t.TempDir(),
		Vars:          map[string]any{"count": 2},
		Steps:         map[string]any{"previous": map[string]any{"items": []any{"a", "b"}}},
	}
	run := func(raw map[string]any) step.Result {
		t.Helper()
		runner, err := New(raw)
		if err != nil {
			t.Fatal(err)
		}
		result, err := runner.Run(t.Context(), request)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}

	set := run(withExpr(config("set", "report", nil, false), `{"total": vars.count + 1, "items": steps.previous.items}`))
	want := map[string]any{"total": int64(3), "items": []any{"a", "b"}}
	if !reflect.DeepEqual(set.Outputs["value"], want) {
		t.Fatalf("set value = %#v", set.Outputs["value"])
	}
	got := run(config("get", "report", nil, false)).Outputs["value"]
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("get value = %#v", got)
	}
}

func TestUpdateReadsAndWritesUnderOneLock(t *testing.T) {
	request := step.Request{LocalValueDir: t.TempDir()}
	run := func(raw map[string]any) map[string]any {
		t.Helper()
		runner, err := New(raw)
		if err != nil {
			t.Fatal(err)
		}
		result, err := runner.Run(t.Context(), request)
		if err != nil {
			t.Fatal(err)
		}
		return result.Outputs
	}

	increment := withExpr(config("update", "runs", nil, false), "found ? current + 1 : 1")
	first := run(increment)
	if !reflect.DeepEqual(first, map[string]any{"value": int64(1), "found": false, "changed": true}) {
		t.Fatalf("first update = %#v", first)
	}
	second := run(withExpr(config("update", "runs", nil, false), "found ? current + 1 : 1"))
	if !reflect.DeepEqual(second, map[string]any{"value": int64(2), "found": true, "changed": true}) {
		t.Fatalf("second update = %#v", second)
	}
	unchanged := run(withExpr(config("update", "runs", nil, false), "current"))
	if !reflect.DeepEqual(unchanged, map[string]any{"value": int64(2), "found": true, "changed": false}) {
		t.Fatalf("unchanged update = %#v", unchanged)
	}
}

func TestVariableReceivesTheOperationResult(t *testing.T) {
	request := step.Request{LocalValueDir: t.TempDir()}
	run := func(raw map[string]any, variable string) map[string]any {
		t.Helper()
		raw["variable"] = variable
		runner, err := New(raw)
		if err != nil {
			t.Fatal(err)
		}
		result, err := runner.Run(t.Context(), request)
		if err != nil {
			t.Fatal(err)
		}
		return result.Variables
	}

	if got := run(config("set", "theme", "dark", true), "saved"); !reflect.DeepEqual(got, map[string]any{"saved": "dark"}) {
		t.Fatalf("set variables = %#v", got)
	}
	if got := run(config("get", "theme", nil, false), "theme"); !reflect.DeepEqual(got, map[string]any{"theme": "dark"}) {
		t.Fatalf("get variables = %#v", got)
	}
	if got := run(withExpr(config("update", "theme", nil, false), `current + "-high-contrast"`), "theme"); !reflect.DeepEqual(got, map[string]any{"theme": "dark-high-contrast"}) {
		t.Fatalf("update variables = %#v", got)
	}
	want := map[string]any{"all": []any{map[string]any{"key": "theme", "value": "dark-high-contrast"}}}
	if got := run(config("list", "", nil, false), "all"); !reflect.DeepEqual(got, want) {
		t.Fatalf("list variables = %#v", got)
	}
	if got := run(config("delete", "theme", nil, false), "removed"); !reflect.DeepEqual(got, map[string]any{"removed": "dark-high-contrast"}) {
		t.Fatalf("delete variables = %#v", got)
	}
	runner, err := New(config("get", "theme", nil, false))
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.Variables != nil {
		t.Fatalf("variables without a target = %#v", result.Variables)
	}
}

func TestGetDefaultListPrefixAndClear(t *testing.T) {
	request := step.Request{LocalValueDir: t.TempDir()}
	run := func(raw map[string]any) map[string]any {
		t.Helper()
		runner, err := New(raw)
		if err != nil {
			t.Fatal(err)
		}
		result, err := runner.Run(t.Context(), request)
		if err != nil {
			t.Fatal(err)
		}
		return result.Outputs
	}

	missing := run(map[string]any{
		"operation": "get", "scope": "local", "store": "prefs", "key": "theme", "default": map[string]any{"name": "light"},
	})
	if !reflect.DeepEqual(missing, map[string]any{"value": map[string]any{"name": "light"}, "found": false}) {
		t.Fatalf("default = %#v", missing)
	}
	run(config("set", "theme", "dark", true))
	stored := run(map[string]any{"operation": "get", "scope": "local", "store": "prefs", "key": "theme", "default": "light"})
	if !reflect.DeepEqual(stored, map[string]any{"value": "dark", "found": true}) {
		t.Fatalf("stored value = %#v", stored)
	}

	run(config("set", "build-linux", 1, true))
	run(config("set", "build-darwin", 2, true))
	filtered := run(map[string]any{"operation": "list", "scope": "local", "store": "prefs", "prefix": "build-"})
	want := []any{
		map[string]any{"key": "build-darwin", "value": int64(2)},
		map[string]any{"key": "build-linux", "value": int64(1)},
	}
	if !reflect.DeepEqual(filtered["entries"], want) {
		t.Fatalf("prefixed entries = %#v", filtered["entries"])
	}

	cleared := run(map[string]any{"operation": "clear", "scope": "local", "store": "prefs"})
	if !reflect.DeepEqual(cleared, map[string]any{"cleared": int64(3)}) {
		t.Fatalf("clear = %#v", cleared)
	}
	empty := run(config("list", "", nil, false))
	if len(empty["entries"].([]any)) != 0 {
		t.Fatalf("entries after clear = %#v", empty["entries"])
	}
	if again := run(map[string]any{"operation": "clear", "scope": "local", "store": "prefs"}); !reflect.DeepEqual(again, map[string]any{"cleared": int64(0)}) {
		t.Fatalf("repeated clear = %#v", again)
	}
}

func TestSetExprRejectsUnusableResults(t *testing.T) {
	tests := []struct {
		name       string
		expression string
		want       string
	}{
		{"non-JSON result", "1 / 0.0", "expr result is not JSON-compatible"},
		{"failing expression", "vars.missing.field", "evaluating expr"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner, err := New(withExpr(config("set", "key", nil, false), tt.expression))
			if err != nil {
				t.Fatal(err)
			}
			_, err = runner.Run(t.Context(), step.Request{LocalValueDir: t.TempDir()})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestRunReportsWhichFieldIsUnrendered(t *testing.T) {
	tests := []struct {
		name  string
		raw   map[string]any
		field string
	}{
		{"key", map[string]any{"operation": "get", "scope": "local", "store": "prefs", "key": "{{ .vars.key }}"}, "key"},
		{"prefix", map[string]any{"operation": "list", "scope": "local", "store": "prefs", "prefix": "{{ .vars.prefix }}"}, "prefix"},
		{"store", map[string]any{"operation": "list", "scope": "local", "store": "{{ .vars.store }}"}, "store"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner, err := New(tt.raw)
			if err != nil {
				t.Fatal(err)
			}
			_, err = runner.Run(t.Context(), step.Request{LocalValueDir: t.TempDir()})
			want := tt.field + " contains an unresolved template"
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("error = %v, want %q", err, want)
			}
		})
	}
}

func TestFailedOperationsReportNoOutputs(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "prefs.json"), []byte("[1,2"), 0o600); err != nil {
		t.Fatal(err)
	}
	runner, err := New(config("get", "theme", nil, false))
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(t.Context(), step.Request{LocalValueDir: dir})
	if err == nil || !strings.Contains(err.Error(), "decoding store") {
		t.Fatalf("error = %v", err)
	}
	if result.Outputs != nil {
		t.Fatalf("failed run reported outputs %#v", result.Outputs)
	}
}

func TestNumbersNoGoTypeHoldsStayJSONShapes(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "prefs.json"), []byte(`{"huge": 1e400}`), 0o600); err != nil {
		t.Fatal(err)
	}
	runner, err := New(config("get", "huge", nil, false))
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(t.Context(), step.Request{LocalValueDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if got := result.Outputs["value"]; got != "1e400" {
		t.Fatalf("value = %#v (%T), want the string 1e400", got, got)
	}
}

func TestSetRejectsAnUnrenderedExpr(t *testing.T) {
	runner, err := New(withExpr(config("set", "key", nil, false), "{{ .vars.expression }}"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.Run(t.Context(), step.Request{LocalValueDir: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "expr contains an unresolved template") {
		t.Fatalf("error = %v", err)
	}
}

func withExpr(raw map[string]any, expression string) map[string]any {
	raw["expr"] = expression
	return raw
}

func config(operation, key string, value any, hasValue bool) map[string]any {
	raw := map[string]any{"operation": operation, "scope": "local", "store": "prefs"}
	if key != "" {
		raw["key"] = key
	}
	if hasValue {
		raw["value"] = value
	}
	return raw
}
