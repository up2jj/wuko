package cmd

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/up2jj/wuko/engine"
	"github.com/up2jj/wuko/scriptsdk"
	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/steps/shell"
	"github.com/up2jj/wuko/workflow"
)

func TestScriptSDKCommandPreservesFlagLikeValues(t *testing.T) {
	b, err := scriptsdk.Open(t.Context(), step.Request{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.Close() })
	environment := map[string]string{}
	if err := b.InjectBash(environment, "/unused/wuko"); err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	root := newRootCmd(dependencies{
		stdout: &out, stderr: &stderr, stdin: strings.NewReader("untouched"),
		getenv: func(key string) string { return environment[key] },
		cwd:    func() (string, error) { t.Fatal("SDK client must not discover workflows"); return "", nil },
	})
	root.SetArgs([]string{"__script-sdk", "output", "value", "--help"})
	if err := root.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if b.Result().Outputs["value"] != "--help" || out.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("result=%#v stdout=%q stderr=%q", b.Result(), out.String(), stderr.String())
	}
}

func TestBashSDKInjectedFunctionsWithoutExternalTools(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("Bash is unavailable")
	}
	dir := t.TempDir()
	prior := filepath.Join(dir, "prior ' startup.bash")
	if err := os.WriteFile(prior, []byte(`export PRIOR_LOADED="$(wuko_helper upper startup)"`), 0o600); err != nil {
		t.Fatal(err)
	}
	source := `set -euo pipefail
printf 'ordinary stdout\n'
printf 'ordinary stderr\n' >&2
IFS= read -r line
wuko_output input "$line"
wuko_output startup "$PRIOR_LOADED"
wuko_output label "$(wuko_helper upper "$(wuko_get args.title)")"
wuko_output flaglike --help
wuko_output empty ''
wuko_output quoting $'quote\'"\\\n$(not-executed)'
wuko_output_json packages "$(wuko_get_json args.packages)"
wuko_output_json large 9007199254740993
wuko_output_json flag false
wuko_output_json nil null
wuko_set_var release_label ready
wuko_set_var_json release_flag true
wuko_output_json typed "$(wuko_helper_json list '[1,true,null]')"
wuko_output descriptor "$WUKO_SDK_DESCRIPTOR"
wuko_output secret "$(wuko_secret test:token)"
"$BASH" -c 'wuko_output child inherited'
if wuko_helper nonexistent >unexpected.out 2>expected.err; then exit 90; fi
[[ ! -s unexpected.out && -s expected.err ]]
if wuko_output_json bad 'not-json' >unexpected.out 2>expected.err; then exit 91; fi
[[ ! -s unexpected.out && -s expected.err ]]
`
	file := filepath.Join(dir, "script with spaces.bash")
	if err := os.WriteFile(file, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"script", "file", "argv"} {
		t.Run(mode, func(t *testing.T) {
			raw := map[string]any{
				"sdk": "bash", "stdin": "stdin stays available\n",
				"sdk_args": map[string]any{"title": "release", "packages": map[string]any{"expr": "vars.packages"}},
				"env":      map[string]any{"BASH_ENV": prior, "PATH": filepath.Join(dir, "empty-path"), "GORACE": "atexit_sleep_ms=0"},
			}
			switch mode {
			case "script":
				raw["script"], raw["shell"] = source, bash
			case "file":
				raw["command"], raw["args"] = bash, []any{file}
			case "argv":
				raw["argv"] = map[string]any{"expr": "[vars.bash, vars.file]"}
			}
			runner, err := shell.New(raw)
			if err != nil {
				t.Fatal(err)
			}
			var stdout, stderr bytes.Buffer
			result, err := runner.Run(t.Context(), step.Request{
				RunDir: dir, Stdout: &stdout, Stderr: &stderr,
				Vars:   map[string]any{"packages": []any{"api", "web"}, "bash": bash, "file": file},
				Secret: func(ref string) (string, error) { return "resolved:" + ref, nil },
			})
			if err != nil {
				t.Fatalf("%v\n%s", err, stderr.String())
			}
			data := result.Outputs["data"].(map[string]any)
			for key, want := range map[string]any{
				"input": "stdin stays available", "label": "RELEASE", "startup": "STARTUP", "flaglike": "--help", "empty": "",
				"quoting": "quote'\"\\\n$(not-executed)", "large": int64(9007199254740993), "flag": false, "nil": nil,
				"child": "inherited", "secret": "resolved:test:token",
			} {
				if data[key] != want {
					t.Errorf("data[%s]=%#v, want %#v", key, data[key], want)
				}
			}
			if len(data["packages"].([]any)) != 2 || len(data["typed"].([]any)) != 3 || result.Variables["release_label"] != "ready" || result.Variables["release_flag"] != true {
				t.Fatalf("result=%#v", result)
			}
			if stdout.String() != "ordinary stdout\n" || stderr.String() != "ordinary stderr\n" {
				t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
			}
			if _, err := os.Stat(data["descriptor"].(string)); !os.IsNotExist(err) {
				t.Fatalf("descriptor survived attempt: %v", err)
			}
		})
	}
}

func TestBashSDKFallsBackWhenNetworkRedirectionsAreUnavailable(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("Bash is unavailable")
	}
	// "0" models a Bash build without /dev/tcp; "" models a host without IPv4 loopback.
	for _, port := range []string{"0", ""} {
		t.Run(fmt.Sprintf("port=%q", port), func(t *testing.T) { testBashSDKFallback(t, bash, port) })
	}
}

func testBashSDKFallback(t *testing.T, bash, port string) {
	bridge, err := scriptsdk.Open(t.Context(), step.Request{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bridge.Close() })
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	environment := map[string]string{"PATH": "", "GORACE": "atexit_sleep_ms=0"}
	if err := bridge.InjectBash(environment, executable); err != nil {
		t.Fatal(err)
	}
	path := environment["BASH_ENV"]
	bootstrap, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Force the bootstrap capability probe to fail without changing the real
	// Unix socket endpoint used by the executable fallback.
	lines := strings.Split(string(bootstrap), "\n")
	replaced := false
	for i, line := range lines {
		if strings.HasPrefix(line, "__wuko_sdk_port=") {
			lines[i] = "__wuko_sdk_port='" + port + "'"
			replaced = true
		}
	}
	if !replaced {
		t.Fatal("missing bootstrap listener configuration")
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o600); err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(t.Context(), bash, "-c", `set -euo pipefail; wuko_output label "$(wuko_helper upper fallback)"`)
	for key, value := range environment {
		command.Env = append(command.Env, key+"="+value)
	}
	if output, err := command.CombinedOutput(); err != nil || len(output) != 0 {
		t.Fatalf("fallback: %v: %s", err, output)
	}
	if bridge.Result().Outputs["label"] != "FALLBACK" {
		t.Fatal("fallback did not stage the output")
	}
}

type bashWorkflowHost struct{}

func (bashWorkflowHost) ValidateWorkflow(context.Context, engine.WorkflowInvocation) error {
	return nil
}
func (bashWorkflowHost) RunWorkflow(_ context.Context, call engine.WorkflowInvocation) (map[string]any, error) {
	return map[string]any{"workflow": call.Workflow(), "target": call.Target(), "vars": call.Vars()}, nil
}

func TestBashSDKEngineCalls(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("Bash is unavailable")
	}
	registry := step.NewRegistry()
	if err := registerBuiltinSteps(registry); err != nil {
		t.Fatal(err)
	}
	definition := &workflow.Definition{
		Version: 1, Name: "bash-sdk", Vars: map[string]any{"base": "parent"},
		Steps: []workflow.Step{{ID: "script", Type: "shell", With: map[string]any{
			"sdk": "bash", "shell": bash, "script": `set -euo pipefail
wuko_output label "$(wuko_helper upper hello)"
wuko_output_json child "$(wuko_step set '{"variable":"child_only","value":true}')"
wuko_output rendered "$(wuko_render "Hello {"'{ .vars.base }'"}")"
wuko_output_json workflow "$(wuko_run_workflow build '{"count":2}' release)"
wuko_set_var published done
`,
		}}},
	}
	var stderr bytes.Buffer
	state, err := engine.New(registry, engine.WithWorkflowInvoker(bashWorkflowHost{})).Run(t.Context(), definition, engine.Options{RunDir: t.TempDir(), Env: map[string]string{"GORACE": "atexit_sleep_ms=0"}, Stdout: io.Discard, Stderr: &stderr})
	if err != nil {
		t.Fatalf("%v\n%s", err, stderr.String())
	}
	data := state.Steps["script"].(map[string]any)["data"].(map[string]any)
	if data["label"] != "HELLO" || data["rendered"] != "Hello parent" || state.Vars["published"] != "done" {
		t.Fatalf("state=%#v data=%#v", state.Vars, data)
	}
	if _, leaked := state.Vars["child_only"]; leaked {
		t.Fatal("child variables leaked into the parent")
	}
	child := data["workflow"].(map[string]any)
	if child["workflow"] != "build" || child["target"] != "release" || child["vars"].(map[string]any)["count"] != int64(2) {
		t.Fatalf("workflow call = %#v", child)
	}
}

func TestBashSDKRetryDiscardsFailedWrites(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("Bash is unavailable")
	}
	registry := step.NewRegistry()
	if err := shell.Register(registry); err != nil {
		t.Fatal(err)
	}
	definition := &workflow.Definition{Version: 1, Name: "bash-retry", Steps: []workflow.Step{{
		ID: "retry", Attempt: &workflow.AttemptControl{
			MaxAttempts: workflow.LiteralCount(2), BackoffMultiplier: workflow.LiteralFactor(1),
			Steps: []workflow.Step{{ID: "script", Type: "shell", With: map[string]any{
				"sdk": "bash", "script": `set -euo pipefail
if [[ $(wuko_get attempt.number) == 1 ]]; then
  wuko_output_json failed_only true
  wuko_set_var_json failed_only true
  exit 7
fi
wuko_output_json attempt "$(wuko_get attempt.number)"
wuko_set_var_json published true
`,
			}}},
		},
	}}}
	var stderr bytes.Buffer
	state, err := engine.New(registry).Run(t.Context(), definition, engine.Options{RunDir: t.TempDir(), Env: map[string]string{"GORACE": "atexit_sleep_ms=0"}, Stderr: &stderr})
	if err != nil {
		t.Fatalf("%v\n%s", err, stderr.String())
	}
	retry := state.Steps["retry"].(map[string]any)
	data := retry["steps"].(map[string]any)["script"].(map[string]any)["data"].(map[string]any)
	vars := retry["vars"].(map[string]any)
	if _, exists := data["failed_only"]; exists {
		t.Fatal("failed output survived retry")
	}
	if _, exists := vars["failed_only"]; exists {
		t.Fatal("failed variable survived retry")
	}
	if data["attempt"] != int64(2) || vars["published"] != true {
		t.Fatalf("data=%#v vars=%#v", data, vars)
	}
}
