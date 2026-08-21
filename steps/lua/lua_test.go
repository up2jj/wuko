package lua

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
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
		"source": `wuko.output("binding", {item = wuko.foreach.item, os = wuko.matrix.os})`,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(t.Context(), step.Request{Bindings: map[string]any{
		"foreach": map[string]any{"item": "api"}, "matrix": map[string]any{"os": "linux"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"item": "api", "os": "linux"}
	if got := result.Outputs["binding"].(map[string]any); got["item"] != want["item"] || got["os"] != want["os"] {
		t.Fatalf("binding = %#v", got)
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
