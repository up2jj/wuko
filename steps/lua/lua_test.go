package lua

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/up2jj/wuko/step"
)

func TestInlineLuaStateAndEnvironment(t *testing.T) {
	runner, err := New(map[string]any{
		"source": `
local decoded = wuko.json.decode('{"ok":true}')
wuko.output("result", {name = wuko.args.name, token = wuko.env.get("TOKEN"), ok = decoded.ok, attempt = wuko.env.get("WUKO_STEP_ATTEMPT"), operation_id = wuko.env.get("WUKO_STEP_OPERATION_ID")})
wuko.set_var("done", true)
`,
		"args": map[string]any{"name": "example"},
		"env":  map[string]any{"TOKEN": "step-secret"},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(t.Context(), step.Request{
		StepID: "lua", WorkflowName: "test", RunDir: t.TempDir(), Env: map[string]string{"TOKEN": "secret"}, Stdout: io.Discard, Stderr: io.Discard,
		Attempt: 2, MaxAttempts: 3, OperationID: "lua-operation",
	})
	if err != nil {
		t.Fatal(err)
	}
	output := result.Outputs["result"].(map[string]any)
	if output["name"] != "example" || output["token"] != "step-secret" || output["ok"] != true || output["attempt"] != "2" || output["operation_id"] != "lua-operation" {
		t.Fatalf("output = %#v", output)
	}
	if result.Variables["done"] != true {
		t.Fatalf("variables = %#v", result.Variables)
	}
}

func TestLuaControlBindings(t *testing.T) {
	runner, err := New(map[string]any{
		"source": `wuko.output("binding", {batch_index = wuko.batch.index, batch_items = wuko.batch.items, item = wuko.foreach.item, os = wuko.matrix.os})`,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(t.Context(), step.Request{Bindings: map[string]any{
		"batch":   map[string]any{"index": 1, "items": []any{"api", "worker"}},
		"foreach": map[string]any{"item": "api"}, "matrix": map[string]any{"os": "linux"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"batch_index": float64(1), "batch_items": []any{"api", "worker"}, "item": "api", "os": "linux"}
	if got := result.Outputs["binding"].(map[string]any); got["batch_index"] != want["batch_index"] || !reflect.DeepEqual(got["batch_items"], want["batch_items"]) || got["item"] != want["item"] || got["os"] != want["os"] {
		t.Fatalf("binding = %#v", got)
	}
}

func TestLuaHelpers(t *testing.T) {
	runner, err := New(map[string]any{
		"source": `
local h = wuko.helpers
local original = {"b", "a"}
local object = h.dict("b", 2, "a", 1)
wuko.output("helpers", {
  lower = h.lower("WUKO"),
  upper = h.upper("wuko"),
  trim = h.trim("  value  "),
  trim_prefix = h.trim_prefix("release-v1", "release-"),
  trim_suffix = h.trim_suffix("release.yaml", ".yaml"),
  contains = h.contains("workflow", "flow"),
  has_prefix = h.has_prefix("release-v1", "release-"),
  has_suffix = h.has_suffix("release.yaml", ".yaml"),
  replace = h.replace("hello_world", "_", "-"),
  split_join = h.join(h.split("a,b", ","), ":"),
  default = h.default("", "fallback"),
  coalesce = h.coalesce("", 0, false, "value"),
  required = h.required("value", "value is required"),
  indent = h.indent("one\ntwo", 2),
  nindent = h.nindent("one\ntwo", 2),
  list = h.list("a", "b"),
  get = h.get(object, "a"),
  missing = h.get(object, "missing") == nil,
  has_key = h.has_key(object, "b"),
  keys = h.join(h.keys(object), ","),
  sorted = h.join(h.sort_alpha(original), ","),
  original = h.join(original, ","),
  json = h.to_json(object),
  json_compact = h.to_json_compact(object),
  yaml = h.to_yaml(object),
})
`,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(t.Context(), step.Request{StepID: "helpers", WorkflowName: "test"})
	if err != nil {
		t.Fatal(err)
	}
	output := result.Outputs["helpers"].(map[string]any)
	wants := map[string]any{
		"lower": "wuko", "upper": "WUKO", "trim": "value",
		"trim_prefix": "v1", "trim_suffix": "release", "contains": true,
		"has_prefix": true, "has_suffix": true, "replace": "hello-world",
		"split_join": "a:b", "default": "fallback", "coalesce": "value",
		"required": "value", "indent": "  one\n  two", "nindent": "\n  one\n  two",
		"get": float64(1), "missing": true, "has_key": true, "keys": "a,b",
		"sorted": "a,b", "original": "b,a",
		"json":         "{\n  \"a\": 1,\n  \"b\": 2\n}",
		"json_compact": `{"a":1,"b":2}`,
		"yaml":         "a: 1\nb: 2\n",
	}
	for key, want := range wants {
		if got := output[key]; got != want {
			t.Errorf("%s = %#v, want %#v", key, got, want)
		}
	}
	list := output["list"].([]any)
	if len(list) != 2 || list[0] != "a" || list[1] != "b" {
		t.Fatalf("list = %#v", list)
	}
}

func TestLuaHelpersRejectInvalidArguments(t *testing.T) {
	tests := []struct {
		name   string
		source string
		want   string
	}{
		{name: "required", source: `wuko.helpers.required("", "application is required")`, want: "application is required"},
		{name: "negative indent", source: `wuko.helpers.indent("value", -1)`, want: "indent width"},
		{name: "odd dict", source: `wuko.helpers.dict("key")`, want: "even number"},
		{name: "non-string dict key", source: `wuko.helpers.dict(true, "value")`, want: "want string"},
		{name: "non-string sort item", source: `wuko.helpers.sort_alpha({"value", 1})`, want: "want string"},
		{name: "get from list", source: `wuko.helpers.get({"value"}, "key")`, want: "map with string keys"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner, err := New(map[string]any{"source": tt.source})
			if err != nil {
				t.Fatal(err)
			}
			_, err = runner.Run(t.Context(), step.Request{StepID: "helpers", WorkflowName: "test"})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestLuaSlugifyHelper(t *testing.T) {
	runner, err := New(map[string]any{
		"source": `
local h = wuko.helpers
wuko.output("slugs", {
  default_slug = h.slugify("  Déjà vu / API  "),
  git = h.slugify("Feature / Payment API", {mode = "git"}),
  flat_git = h.slugify("Feature / Payment API", {mode = "git", preserve_slash = false}),
  underscore = h.slugify("Hello, world!", {separator = "_"}),
})
`,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(t.Context(), step.Request{StepID: "slugify", WorkflowName: "test"})
	if err != nil {
		t.Fatal(err)
	}
	slugs := result.Outputs["slugs"].(map[string]any)
	wants := map[string]any{
		"default_slug": "deja-vu-api",
		"git":          "feature/payment-api",
		"flat_git":     "feature-payment-api",
		"underscore":   "hello_world",
	}
	for key, want := range wants {
		if got := slugs[key]; got != want {
			t.Errorf("%s = %#v, want %#v", key, got, want)
		}
	}
}

func TestLuaSlugifyHelperRejectsInvalidArguments(t *testing.T) {
	tests := []struct {
		name   string
		source string
		want   string
	}{
		{name: "empty result", source: `wuko.helpers.slugify("---")`, want: "result is empty"},
		{name: "invalid options type", source: `wuko.helpers.slugify("value", "git")`, want: "options must be an object"},
		{name: "unknown option", source: `wuko.helpers.slugify("value", {unknown = true})`, want: "unknown slugify option"},
		{name: "dot separator in git mode", source: `wuko.helpers.slugify("value", {mode = "git", separator = "."})`, want: "not supported in git mode"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner, err := New(map[string]any{"source": tt.source})
			if err != nil {
				t.Fatal(err)
			}
			_, err = runner.Run(t.Context(), step.Request{StepID: "slugify", WorkflowName: "test"})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestLuaFinallyBinding(t *testing.T) {
	runner, err := New(map[string]any{
		"source": `wuko.output("outcome", {status = wuko.finally.status, step = wuko.finally.errors[1].step_id})`,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(t.Context(), step.Request{Bindings: map[string]any{
		"finally": map[string]any{
			"status": "failed",
			"errors": []any{map[string]any{"step_id": "deploy"}},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	outcome := result.Outputs["outcome"].(map[string]any)
	if outcome["status"] != "failed" || outcome["step"] != "deploy" {
		t.Fatalf("outcome = %#v", outcome)
	}
}

func TestLuaKeyValueAPI(t *testing.T) {
	runner, err := New(map[string]any{
		"source": `
local saved = wuko.kv.set({scope = "local", store = "prefs", key = "theme", value = {name = "dark"}})
wuko.kv.set({scope = "local", store = "prefs", key = "nothing"})
local theme, found = wuko.kv.get({scope = "local", store = "prefs", key = "theme"})
local missing, missing_found = wuko.kv.get({scope = "local", store = "prefs", key = "missing"})
local entries = wuko.kv.list({scope = "local", store = "prefs"})
local removed, deleted = wuko.kv.delete({scope = "local", store = "prefs", key = "theme"})
wuko.output("result", {
  saved = saved.name,
  theme = theme.name,
  found = found,
  missing_is_nil = missing == nil,
  missing_found = missing_found,
  first_key = entries[1].key,
  first_is_nil = entries[1].value == nil,
  second_key = entries[2].key,
  removed = removed.name,
  deleted = deleted,
})
`,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(t.Context(), step.Request{
		StepID: "kv", WorkflowName: "test", LocalValueDir: t.TempDir(), GlobalValueDir: t.TempDir(),
		Env: map[string]string{}, Stdout: io.Discard, Stderr: io.Discard,
	})
	if err != nil {
		t.Fatal(err)
	}
	output := result.Outputs["result"].(map[string]any)
	if output["saved"] != "dark" || output["theme"] != "dark" || output["found"] != true ||
		output["missing_is_nil"] != true || output["missing_found"] != false || output["first_key"] != "nothing" ||
		output["first_is_nil"] != true || output["second_key"] != "theme" || output["removed"] != "dark" || output["deleted"] != true {
		t.Fatalf("output = %#v", output)
	}
}

func TestLuaKeyValueRejectsInvalidOptionsAndUnavailableLocalStore(t *testing.T) {
	tests := []struct {
		name   string
		source string
		want   string
	}{
		{"unknown option", `wuko.kv.list({scope="global", store="prefs", extra=true})`, "unknown option"},
		{"wrong key type", `wuko.kv.get({scope="global", store="prefs", key=true})`, "key must be a non-empty string"},
		{"bad scope", `wuko.kv.list({scope="shared", store="prefs"})`, "scope must be"},
		{"local unavailable", `wuko.kv.list({scope="local", store="prefs"})`, "local key-value storage is unavailable"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner, err := New(map[string]any{"source": tt.source})
			if err != nil {
				t.Fatal(err)
			}
			_, err = runner.Run(t.Context(), step.Request{GlobalValueDir: t.TempDir(), Env: map[string]string{}, Stdout: io.Discard, Stderr: io.Discard})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestLuaHostHTTPFilesystemAndProcess(t *testing.T) {
	var requestSeen bool
	doHTTP := func(request *http.Request, _ time.Duration) (*http.Response, error) {
		requestSeen = true
		if request.Header.Get("X-Test") != "yes" {
			t.Errorf("X-Test = %q", request.Header.Get("X-Test"))
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"value":"remote"}`)),
		}, nil
	}

	runner, err := New(map[string]any{
		"source": `
local response = wuko.http.request({url = wuko.args.url, headers = { ["X-Test"] = "yes" }})
local decoded = wuko.json.decode(response.body)
wuko.fs.mkdir_all("data")
wuko.fs.write("data/value.txt", decoded.value)
local text = wuko.fs.read("data/value.txt")
local info = wuko.fs.stat("data/value.txt")
local entries = wuko.fs.list("data")
local command = wuko.exec.run({command = "sh", args = {"-c", "printf '%s' \"$TOKEN\""}, env = {TOKEN = "inner"}})
wuko.fs.rename("data/value.txt", "data/renamed.txt")
wuko.fs.remove("data/renamed.txt")
wuko.output("host", {status = response.status, text = text, size = info.size, count = #entries, stdout = command.stdout})
`,
		"args": map[string]any{"url": "https://example.test/task"},
	})
	if err != nil {
		t.Fatal(err)
	}
	runner.(*Runner).doHTTP = doHTTP
	runDir := t.TempDir()
	result, err := runner.Run(t.Context(), step.Request{StepID: "host", WorkflowName: "test", RunDir: runDir, Env: map[string]string{}, Stdout: io.Discard, Stderr: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	output := result.Outputs["host"].(map[string]any)
	if output["status"] != float64(http.StatusOK) || output["text"] != "remote" || output["stdout"] != "inner" || output["count"] != float64(1) {
		t.Fatalf("output = %#v", output)
	}
	if !requestSeen {
		t.Fatal("HTTP executor was not called")
	}
	if _, err := os.Stat(filepath.Join(runDir, "data", "renamed.txt")); !os.IsNotExist(err) {
		t.Fatalf("renamed file should have been removed, stat error = %v", err)
	}
}
