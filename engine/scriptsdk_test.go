package engine

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"

	"github.com/up2jj/wuko/step"
	runworkflow "github.com/up2jj/wuko/steps/runworkflow"
	"github.com/up2jj/wuko/steps/set"
	"github.com/up2jj/wuko/steps/shell"
	"github.com/up2jj/wuko/workflow"
)

func scriptSDKNode(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("node")
	if err != nil {
		if os.Getenv("WUKO_NODE_INTEGRATION") == "1" {
			t.Fatal("Node.js is required for SDK integration tests")
		}
		t.Skip("Node.js is unavailable")
	}
	resolved, err := exec.Command(path, "-p", `process.versions.node.split('.')[0] + '\n' + process.execPath`).Output()
	if err != nil {
		if os.Getenv("WUKO_NODE_INTEGRATION") == "1" {
			t.Fatalf("resolving Node runtime: %v", err)
		}
		t.Skipf("Node runtime is unavailable: %v", err)
	}
	parts := strings.SplitN(strings.TrimSpace(string(resolved)), "\n", 2)
	major, _ := strconv.Atoi(parts[0])
	if len(parts) != 2 || major < 22 {
		if os.Getenv("WUKO_NODE_INTEGRATION") == "1" {
			t.Fatal("SDK integration tests require Node.js 22+")
		}
		t.Skip("SDK integration tests require Node.js 22+")
	}
	return parts[1]
}

func scriptSDKRegistry(t *testing.T) *step.Registry {
	t.Helper()
	registry := step.NewRegistry()
	for _, register := range []func(*step.Registry) error{shell.Register, set.Register, runworkflow.Register} {
		if err := register(registry); err != nil {
			t.Fatal(err)
		}
	}
	return registry
}

func nodeSDKStep(node, id, source string) workflow.Step {
	return workflow.Step{ID: id, Type: "shell", With: map[string]any{
		"sdk": "node", "command": node, "args": []any{"--input-type=module", "-e", source},
	}}
}

func TestScriptSDKCallsEngineWithIsolatedState(t *testing.T) {
	node := scriptSDKNode(t)
	registry := scriptSDKRegistry(t)
	if err := registry.Register("inspect", func(raw map[string]any) (step.Runner, error) {
		return runnerFunc(func(_ context.Context, request step.Request) (step.Result, error) {
			if request.Interactive || request.Stdin != nil {
				t.Error("child invocation is interactive")
			}
			return step.Result{Outputs: map[string]any{"value": raw["value"], "snapshot": request.Vars["base"]}, Variables: map[string]any{"child_only": true}}, nil
		}), nil
	}); err != nil {
		t.Fatal(err)
	}
	definition := testDefinition(t, "script-host",
		nodeSDKStep(node, "script", `
const value = await wuko.step('inspect', {value: {count: 3}});
await wuko.output('child', value);
await wuko.output('rendered', await wuko.render('Hello {' + '{ .data.name }' + '}', {data: {name: 'SDK'}}));
await wuko.setVar('published', 'done');
try { await wuko.step('unknown_step', {}); throw new Error('unexpected success'); }
catch (error) { if (error.code !== 'host_error') throw error; }
`),
		workflow.Step{ID: "after", Type: "set", With: map[string]any{"variable": "copied", "value": "{{ .vars.published }}:{{ .steps.script.data.child.snapshot }}"}},
	)
	definition.Vars = map[string]any{"base": "parent"}
	var stderr bytes.Buffer
	state, err := New(registry).Run(t.Context(), definition, Options{RunDir: t.TempDir(), Stderr: &stderr})
	if err != nil {
		t.Fatalf("%v\n%s", err, stderr.String())
	}
	data := state.Steps["script"].(map[string]any)["data"].(map[string]any)
	if data["rendered"] != "Hello SDK" || state.Vars["copied"] != "done:parent" {
		t.Fatalf("state = %#v, data = %#v", state.Vars, data)
	}
	if data["child"].(map[string]any)["value"].(map[string]any)["count"] != int64(3) {
		t.Fatalf("child = %#v", data["child"])
	}
	if _, exists := state.Vars["child_only"]; exists {
		t.Fatal("child mutated parent variables")
	}
	if len(state.Steps) != 2 {
		t.Fatalf("child leaked steps: %#v", state.Steps)
	}
}

type sdkChildWorkflowHost struct{ calls int }

func (host *sdkChildWorkflowHost) ValidateWorkflow(context.Context, WorkflowInvocation) error {
	return nil
}
func (host *sdkChildWorkflowHost) RunWorkflow(_ context.Context, call WorkflowInvocation) (map[string]any, error) {
	host.calls++
	return map[string]any{"workflow": call.Workflow(), "target": call.Target(), "vars": call.Vars()}, nil
}

func TestScriptSDKInvokesNamedWorkflow(t *testing.T) {
	node := scriptSDKNode(t)
	host := &sdkChildWorkflowHost{}
	definition := testDefinition(t, "caller", nodeSDKStep(node, "script", `
await wuko.output('child', await wuko.runWorkflow('build', {target: 'release', vars: {revision: 'abc', count: 2}}));
`))
	var stderr bytes.Buffer
	state, err := New(scriptSDKRegistry(t), WithWorkflowInvoker(host)).Run(t.Context(), definition, Options{RunDir: t.TempDir(), Stderr: &stderr})
	if err != nil {
		t.Fatalf("%v\n%s", err, stderr.String())
	}
	child := state.Steps["script"].(map[string]any)["data"].(map[string]any)["child"].(map[string]any)
	if host.calls != 1 || child["workflow"] != "build" || child["target"] != "release" || child["vars"].(map[string]any)["count"] != int64(2) {
		t.Fatalf("child = %#v, calls = %d", child, host.calls)
	}
}

func TestScriptSDKRetriesDiscardFailedAttemptWrites(t *testing.T) {
	node := scriptSDKNode(t)
	body := nodeSDKStep(node, "", `
if (wuko.attempt.number === 1) {
  await wuko.output('failed_only', true);
  await wuko.setVar('failed_only', true);
  process.exitCode = 7;
} else {
  await wuko.output('attempt', wuko.attempt.number);
  await wuko.setVar('published', true);
}
`)
	definition := testDefinition(t, "retry-sdk", attemptStep("publish", immediateRetry(2), body))
	state, err := New(scriptSDKRegistry(t)).Run(t.Context(), definition, Options{RunDir: t.TempDir(), Stdout: io.Discard, Stderr: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	data := attemptBody(state, "publish", "publish_body")["data"].(map[string]any)
	if _, exists := data["failed_only"]; exists {
		t.Fatal("failed output leaked into retry")
	}
	vars := attemptVars(state, "publish")
	if _, exists := vars["failed_only"]; exists {
		t.Fatal("failed variable leaked into retry")
	}
	if data["attempt"] != int64(2) || vars["published"] != true {
		t.Fatalf("result = %#v %#v", data, vars)
	}
}

func TestScriptSDKReferenceValidation(t *testing.T) {
	for _, test := range []struct {
		name                 string
		sdk                  bool
		arg, reference, want string
	}{
		{"typed arg", true, "vars.items", "{{ .steps.script.data.anything }}", ""},
		{"unknown arg variable", true, "vars.missing", "", "missing"},
		{"unknown arg step", true, "steps.missing.value", "", "missing"},
		{"dynamic variables", true, "", "{{ .vars.created_by_script }}", ""},
		{"ordinary shell variables remain closed", false, "", "{{ .vars.created_by_script }}", "created_by_script"},
		{"ordinary shell outputs remain closed", false, "", "{{ .steps.script.data.anything }}", "data"},
	} {
		t.Run(test.name, func(t *testing.T) {
			script := workflow.Step{ID: "script", Type: "shell", With: map[string]any{"command": "node"}}
			if test.sdk {
				script.With["sdk"] = "node"
			}
			if test.arg != "" {
				script.With["sdk_args"] = map[string]any{"items": map[string]any{"expr": test.arg}}
			}
			definition := testDefinition(t, "references", script, workflow.Step{ID: "use", Type: "set", With: map[string]any{"variable": "result", "value": test.reference}})
			definition.Vars = map[string]any{"items": []any{1, 2}}
			err := New(scriptSDKRegistry(t)).Validate(t.Context(), definition, Options{})
			if test.want == "" && err != nil {
				t.Fatal(err)
			}
			if test.want != "" && (err == nil || !strings.Contains(err.Error(), test.want)) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}
