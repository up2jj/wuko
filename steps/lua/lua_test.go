package lua

import (
	"bytes"
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

func TestLuaArgumentExpressionsUseRuntimeRootsAndPreserveTypes(t *testing.T) {
	runner, err := New(map[string]any{
		"source": `wuko.output("args", wuko.args)`,
		"args": map[string]any{
			"inventory": map[string]any{"expr": "steps.decode.value"},
			"summary":   map[string]any{"expr": `inputs.prefix + ":" + dependencies.build.artifact + ":" + batch.name + ":" + foreach.item + ":" + matrix.os + ":" + finally.status + ":" + workflow.name + ":" + workflow.dir + ":" + run.dir`},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := step.Request{
		WorkflowName: "release", WorkflowDir: "/workflow", RunDir: "/run",
		Inputs:       map[string]any{"prefix": "deploy"},
		Steps:        map[string]any{"decode": map[string]any{"value": []any{map[string]any{"name": "api", "replicas": 2}}}},
		Dependencies: map[string]map[string]any{"build": {"artifact": "app"}},
		Bindings: map[string]any{
			"batch": map[string]any{"name": "batch"}, "foreach": map[string]any{"item": "item"},
			"matrix": map[string]any{"os": "linux"}, "finally": map[string]any{"status": "succeeded"},
		},
	}
	result, err := runner.Run(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	args := result.Outputs["args"].(map[string]any)
	wantInventory := []any{map[string]any{"name": "api", "replicas": float64(2)}}
	if !reflect.DeepEqual(args["inventory"], wantInventory) {
		t.Fatalf("inventory = %#v, want %#v", args["inventory"], wantInventory)
	}
	wantSummary := "deploy:app:batch:item:linux:succeeded:release:/workflow:/run"
	if args["summary"] != wantSummary {
		t.Fatalf("summary = %#v, want %q", args["summary"], wantSummary)
	}
}

func TestLuaExposesRuntimeRoots(t *testing.T) {
	runner, err := New(map[string]any{"source": `
wuko.steps.decode.value[1].name = "changed"
wuko.output("roots", {
  input = wuko.inputs.target,
  step = wuko.steps.decode.value[1].name,
  dependency = wuko.dependencies.build.artifact,
  workflow_name = wuko.workflow.name,
  workflow_dir = wuko.workflow.dir,
  run_dir = wuko.run.dir,
  environment_loaders = wuko.run.environment_loaders,
})
`})
	if err != nil {
		t.Fatal(err)
	}
	steps := map[string]any{"decode": map[string]any{"value": []any{map[string]any{"name": "api"}}}}
	result, err := runner.Run(t.Context(), step.Request{
		WorkflowName: "release", WorkflowDir: "/workflow", RunDir: "/run", EnvironmentLoaders: []string{"mise", "direnv"},
		Inputs: map[string]any{"target": "prod"}, Steps: steps,
		Dependencies: map[string]map[string]any{"build": {"artifact": "app.tar.gz"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"input": "prod", "step": "changed", "dependency": "app.tar.gz",
		"workflow_name": "release", "workflow_dir": "/workflow", "run_dir": "/run", "environment_loaders": []any{"mise", "direnv"},
	}
	if got := result.Outputs["roots"]; !reflect.DeepEqual(got, want) {
		t.Fatalf("roots = %#v, want %#v", got, want)
	}
	if got := steps["decode"].(map[string]any)["value"].([]any)[0].(map[string]any)["name"]; got != "api" {
		t.Fatalf("Lua mutation changed request steps to %q", got)
	}
}

func TestLuaRejectsInvalidArgumentExpressions(t *testing.T) {
	tests := []struct {
		name string
		expr any
		want string
	}{
		{name: "non-string", expr: 42, want: "must be a non-empty string"},
		{name: "blank", expr: "  ", want: "must be a non-empty string"},
		{name: "invalid", expr: "steps.", want: "compiling argument"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := New(map[string]any{"source": "return", "args": map[string]any{"value": map[string]any{"expr": test.expr}}})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("New() error = %v, want %q", err, test.want)
			}
		})
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

func TestLuaUtilityHelpers(t *testing.T) {
	runner, err := New(map[string]any{"source": `
local h = wuko.helpers
local generated_uuid = h.uuid({version = 4})
local generated_password = h.password(12)
wuko.output("helpers", {
  base64 = h.base64_encode("wuko ✓"),
  base64_round_trip = h.base64_decode(h.base64_encode("wuko ✓")),
  hex = h.hex_encode("Wuko", true),
  hex_round_trip = h.hex_decode(h.hex_encode("🚀")),
  url = h.url_encode("a+b /✓"),
  url_round_trip = h.url_decode(h.url_encode("a+b /✓")),
  html = h.html_encode("<wuko>&"),
  html_round_trip = h.html_decode(h.html_encode("<wuko>&")),
  sha256 = h.sha256("hello"),
  hmac = h.hmac_sha256("payload", "secret"),
  base = h.base_convert("255", 10, 16, true),
  roman = h.roman_encode(2024),
  unroman = h.roman_decode("MMXXIV"),
  ordinal = h.ordinal(22),
  bytes = h.count_bytes("é"),
  runes = h.count_runes("é"),
  graphemes = h.count_graphemes("é"),
  words = h.count_words("one  two"),
  lines = h.count_lines("one\ntwo\n"),
  uuid_length = h.count_bytes(generated_uuid),
  token_length = h.count_bytes(h.random_token(4)),
  random_fixed = h.random_int(7, 7),
  random_string_length = h.count_runes(h.random_string(8, "abc")),
  password_length = h.count_bytes(generated_password),
  current_time = h.current_time(),
  timestamp = h.unix_timestamp(),
})
`})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(t.Context(), step.Request{StepID: "helpers", WorkflowName: "test"})
	if err != nil {
		t.Fatal(err)
	}
	output := result.Outputs["helpers"].(map[string]any)
	wants := map[string]any{
		"base64": "d3VrbyDinJM=", "base64_round_trip": "wuko ✓",
		"hex": "57756B6F", "hex_round_trip": "🚀",
		"url": "a%2Bb%20%2F%E2%9C%93", "url_round_trip": "a+b /✓",
		"html": "&lt;wuko&gt;&amp;", "html_round_trip": "<wuko>&",
		"sha256": "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824",
		"hmac":   "b82fcb791acec57859b989b430a826488ce2e479fdf92326bd0a2e8375a42ba4",
		"base":   "FF", "roman": "MMXXIV", "unroman": float64(2024), "ordinal": "22nd",
		"bytes": float64(2), "runes": float64(1), "graphemes": float64(1),
		"words": float64(2), "lines": float64(2), "uuid_length": float64(36),
		"token_length": float64(8), "random_fixed": float64(7),
		"random_string_length": float64(8), "password_length": float64(12),
	}
	for key, want := range wants {
		if got := output[key]; got != want {
			t.Errorf("%s = %#v, want %#v", key, got, want)
		}
	}
	if current, ok := output["current_time"].(string); !ok || current == "" {
		t.Fatalf("current_time = %#v", output["current_time"])
	}
	if timestamp, ok := output["timestamp"].(float64); !ok || timestamp <= 0 {
		t.Fatalf("timestamp = %#v", output["timestamp"])
	}
}

func TestLuaTimeHelpersAndWorkflowTimezone(t *testing.T) {
	t.Parallel()
	runner, err := New(map[string]any{"source": `
local h = wuko.helpers
local parsed = h.parse_time("2026-03-28 12:00", "2006-01-02 15:04", wuko.workflow.timezone)
local tomorrow = h.add_time(parsed, {days = 1, timezone = wuko.workflow.timezone})
wuko.output("value", h.format_time(tomorrow, "2006-01-02 15:04 Z07:00", wuko.workflow.timezone))
`})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(t.Context(), step.Request{StepID: "time", WorkflowName: "release", WorkflowTimezone: "Europe/Warsaw"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outputs["value"] != "2026-03-29 12:00 +02:00" {
		t.Fatalf("value = %#v", result.Outputs["value"])
	}
}

func TestLuaURIHelpers(t *testing.T) {
	runner, err := New(map[string]any{
		"source": `
local h = wuko.helpers
local uri = h.build_uri({
  scheme = "https",
  host = "api.example.com",
  path = "/releases",
  query = {channel = "stable", tag = {"v1", "v2"}},
})
local parts = h.parse_uri(uri)
local mailto = h.build_uri({
  scheme = "mailto",
  opaque = "ops@example.com",
  query = {subject = "Deployment ready"},
})
wuko.output("uri", uri)
wuko.output("host", parts.host)
wuko.output("tags", parts.query.tag)
wuko.output("mailto", mailto)
`,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(t.Context(), step.Request{StepID: "helpers", WorkflowName: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outputs["uri"] != "https://api.example.com/releases?channel=stable&tag=v1&tag=v2" ||
		result.Outputs["host"] != "api.example.com" || result.Outputs["mailto"] != "mailto:ops@example.com?subject=Deployment+ready" {
		t.Fatalf("outputs = %#v", result.Outputs)
	}
	if tags := result.Outputs["tags"].([]any); !reflect.DeepEqual(tags, []any{"v1", "v2"}) {
		t.Fatalf("tags = %#v", tags)
	}
}

func TestLuaURIHelperErrors(t *testing.T) {
	for _, test := range []struct {
		name   string
		source string
		want   string
	}{
		{name: "parse", source: `wuko.helpers.parse_uri("https://example.com/%zz")`, want: "invalid URL escape"},
		{name: "build", source: `wuko.helpers.build_uri({path = "/", query = {q = 1}})`, want: "string or list"},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner, err := New(map[string]any{"source": test.source})
			if err != nil {
				t.Fatal(err)
			}
			_, err = runner.Run(t.Context(), step.Request{StepID: "helpers", WorkflowName: "test"})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestLuaConventionalCommitHelpers(t *testing.T) {
	runner, err := New(map[string]any{"source": `
local h = wuko.helpers
local message = h.build_conventional_commit({
  type = "fix",
  scope = "auth",
  subject = "correct sessions",
  task = "WUKO-12",
})
wuko.output("message", message)
wuko.output("valid", h.is_conventional_commit(message, {task_regex = "WUKO-[0-9]+"}))
wuko.output("invalid", h.is_conventional_commit("bad message"))
wuko.output("nil_options", h.is_conventional_commit(message, nil))
`})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(t.Context(), step.Request{StepID: "helpers", WorkflowName: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outputs["message"] != "fix(auth): correct sessions WUKO-12" || result.Outputs["valid"] != true ||
		result.Outputs["invalid"] != false || result.Outputs["nil_options"] != true {
		t.Fatalf("outputs = %#v", result.Outputs)
	}
}

func TestLuaConventionalCommitHelperErrors(t *testing.T) {
	for _, source := range []string{
		`wuko.helpers.build_conventional_commit({type = "feat"})`,
		`wuko.helpers.is_conventional_commit("feat: message", {task_regex = "("})`,
	} {
		runner, err := New(map[string]any{"source": source})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := runner.Run(t.Context(), step.Request{StepID: "helpers"}); err == nil {
			t.Fatalf("source %q succeeded", source)
		}
	}
}

func TestLuaHelpersExposeNoClockFunction(t *testing.T) {
	t.Parallel()
	runner, err := New(map[string]any{"source": `
if wuko.helpers.now ~= nil then error("clock helper exposed") end
wuko.output("pure", true)
`})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(t.Context(), step.Request{StepID: "time"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outputs["pure"] != true {
		t.Fatalf("pure = %#v", result.Outputs["pure"])
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
		{name: "negative repeat", source: `wuko.helpers.repeat_text("x", -1)`, want: "must not be negative"},
		{name: "truncate suffix", source: `wuko.helpers.truncate("value", 1, "...")`, want: "suffix"},
		{name: "tabs width", source: `wuko.helpers.tabs_to_spaces("x", 0)`, want: "at least 1"},
		{name: "spaces width", source: `wuko.helpers.spaces_to_tabs("x", 0)`, want: "at least 1"},
		{name: "empty quote", source: `wuko.helpers.quote("x", "")`, want: "must not be empty"},
		{name: "normalization form", source: `wuko.helpers.normalize_unicode("x", "other")`, want: "normalization form"},
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

func TestLuaTextHelpers(t *testing.T) {
	runner, err := New(map[string]any{
		"source": `
local h = wuko.helpers
wuko.output("reverse_text", h.reverse_text("A👩‍❤️‍💋‍👩B"))
wuko.output("reverse_words", h.reverse_words("one two"))
wuko.output("repeat_default", h.repeat_text("ha"))
wuko.output("repeat", h.repeat_text("ha", 3, "-"))
wuko.output("truncate", h.truncate("A👩‍❤️‍💋‍👩BC", 3, "…"))
wuko.output("squeeze", h.squeeze(" too   many "))
wuko.output("remove_whitespace", h.remove_whitespace(" a b "))
wuko.output("remove_punctuation", h.remove_punctuation("hi, there!"))
wuko.output("remove_accents", h.remove_accents("crème"))
wuko.output("remove_non_ascii", h.remove_non_ascii("café"))
wuko.output("strip_html", h.strip_html("<b>hi</b> &amp; bye"))
wuko.output("tabs_to_spaces", h.tabs_to_spaces("a\tb", 2))
wuko.output("tabs_to_spaces_default", h.tabs_to_spaces("a\tb"))
wuko.output("spaces_to_tabs", h.spaces_to_tabs("a  b", 2))
wuko.output("spaces_to_tabs_default", h.spaces_to_tabs("a    b"))
wuko.output("newlines_to_spaces", h.newlines_to_spaces("a\nb"))
wuko.output("spaces_to_newlines", h.spaces_to_newlines("a b"))
wuko.output("rotate", h.rotate("abcd", -1))
wuko.output("rotate_default", h.rotate("abcd"))
wuko.output("quote", h.quote("hi"))
wuko.output("escape_regex", h.escape_regex("a.b"))
wuko.output("normalize_unicode", h.normalize_unicode("é", "nfc"))
wuko.output("normalize_unicode_default", h.normalize_unicode("é"))
`,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(t.Context(), step.Request{StepID: "text-helpers", WorkflowName: "test"})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"reverse_text":              "B👩‍❤️‍💋‍👩A",
		"reverse_words":             "two one",
		"repeat_default":            "haha",
		"repeat":                    "ha-ha-ha",
		"truncate":                  "A👩‍❤️‍💋‍👩…",
		"squeeze":                   "too many",
		"remove_whitespace":         "ab",
		"remove_punctuation":        "hi there",
		"remove_accents":            "creme",
		"remove_non_ascii":          "caf",
		"strip_html":                "hi & bye",
		"tabs_to_spaces":            "a  b",
		"tabs_to_spaces_default":    "a    b",
		"spaces_to_tabs":            "a\tb",
		"spaces_to_tabs_default":    "a\tb",
		"newlines_to_spaces":        "a b",
		"spaces_to_newlines":        "a\nb",
		"rotate":                    "dabc",
		"rotate_default":            "bcda",
		"quote":                     `"hi"`,
		"escape_regex":              `a\.b`,
		"normalize_unicode":         "é",
		"normalize_unicode_default": "é",
	}
	if !reflect.DeepEqual(result.Outputs, want) {
		t.Fatalf("outputs = %#v, want %#v", result.Outputs, want)
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
		{"reserved store", `wuko.kv.list({scope="global", store="picker"})`, "is reserved by wuko"},
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

func TestLuaExecRunOutputPoliciesAndCaptureLimit(t *testing.T) {
	runner, err := New(map[string]any{"source": `
local result = wuko.exec.run({
  command = "sh",
  args = {"-c", "printf 12345; printf abcde >&2"},
  stdout = "capture",
  stderr = "inherit",
  capture_limit = "3B"
})
wuko.output("command", result)
`})
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	result, err := runner.Run(t.Context(), step.Request{
		StepID: "lua", WorkflowName: "test", RunDir: t.TempDir(), Env: map[string]string{}, Stdout: &stdout, Stderr: &stderr,
	})
	if err != nil {
		t.Fatal(err)
	}
	command := result.Outputs["command"].(map[string]any)
	if command["stdout"] != "123" || command["stderr"] != "" || command["stdout_truncated"] != true || command["stderr_truncated"] != false {
		t.Fatalf("command output = %#v", command)
	}
	if stdout.String() != "" || stderr.String() != "abcde" {
		t.Fatalf("streamed stdout = %q, stderr = %q", stdout.String(), stderr.String())
	}
}

func TestLuaExecRunValidatesOutputConfiguration(t *testing.T) {
	tests := []struct {
		name   string
		option string
		value  string
		want   string
	}{
		{name: "stdout", option: "stdout", value: "quiet", want: "exec.run stdout must be"},
		{name: "stderr", option: "stderr", value: "quiet", want: "exec.run stderr must be"},
		{name: "capture limit", option: "capture_limit", value: "0B", want: "exec.run capture_limit must be a positive"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner, err := New(map[string]any{"source": `wuko.exec.run({command = "true", ` + test.option + ` = "` + test.value + `"})`})
			if err != nil {
				t.Fatal(err)
			}
			_, err = runner.Run(t.Context(), step.Request{
				StepID: "lua", WorkflowName: "test", RunDir: t.TempDir(), Env: map[string]string{}, Stdout: io.Discard, Stderr: io.Discard,
			})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Run() error = %v, want %q", err, test.want)
			}
		})
	}
}
