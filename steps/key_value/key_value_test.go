package keyvalue

import (
	"math"
	"os"
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
		{"bad operation", map[string]any{"operation": "clear", "scope": "local", "store": "prefs"}, "operation must be"},
		{"missing scope", map[string]any{"operation": "list", "store": "prefs"}, "scope is required"},
		{"bad scope", map[string]any{"operation": "list", "scope": "shared", "store": "prefs"}, "scope must be"},
		{"bad static scope with templated store", map[string]any{"operation": "list", "scope": "shared", "store": "{{ .vars.store }}"}, "scope must be"},
		{"bad store", map[string]any{"operation": "list", "scope": "local", "store": "../prefs"}, "invalid store name"},
		{"bad static store with templated scope", map[string]any{"operation": "list", "scope": "{{ .vars.scope }}", "store": "../prefs"}, "invalid store name"},
		{"get missing key", config("get", "", nil, false), "key is required"},
		{"get with value", config("get", "key", true, true), "value is not allowed"},
		{"set missing value", config("set", "key", nil, false), "value is required"},
		{"set invalid value", config("set", "key", math.NaN(), true), "value is not JSON-compatible"},
		{"delete with value", config("delete", "key", true, true), "value is not allowed"},
		{"list with key", config("list", "key", nil, false), "key is not allowed"},
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
