package engine

import (
	"bytes"
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/up2jj/wuko/step"
	luastep "github.com/up2jj/wuko/steps/lua"
	"github.com/up2jj/wuko/workflow"
)

type actionRetryRunner struct {
	kind       string
	failures   *int
	recordKeys *[]string
}

func (runner actionRetryRunner) Run(_ context.Context, request step.Request) (step.Result, error) {
	if runner.kind == "record" {
		*runner.recordKeys = append(*runner.recordKeys, request.OperationID)
		return step.Result{Outputs: map[string]any{"value": "recorded"}}, nil
	}
	*runner.failures++
	if *runner.failures == 1 {
		return step.Result{}, errors.New("action failed")
	}
	return step.Result{Outputs: map[string]any{"value": "done"}}, nil
}

func TestCompositeActionRetryKeepsInnerOperationIDsStable(t *testing.T) {
	var failures int
	var recordKeys []string
	registry := newTestRegistry(t, map[string]step.Builder{"action_retry": func(raw map[string]any) (step.Runner, error) {
		return actionRetryRunner{kind: raw["kind"].(string), failures: &failures, recordKeys: &recordKeys}, nil
	}})
	action := testAction(t, "retry-action",
		workflow.Step{ID: "record", Type: "action_retry", With: map[string]any{"kind": "record"}},
		workflow.Step{ID: "finish", Type: "action_retry", With: map[string]any{"kind": "fail"}},
	)
	action.Outputs = map[string]workflow.ActionOutput{"result": {Value: "steps.finish.value"}}
	definition := testDefinition(t, "caller", workflow.Step{
		ID: "remote", Uses: workflow.ActionSource{URL: "https://example.test/action"}, Action: action,
		Retry: immediateRetry(2), With: map[string]any{},
	})

	state, err := New(registry).Run(t.Context(), definition, Options{RunDir: t.TempDir(), Stdout: io.Discard, Stderr: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if len(recordKeys) != 2 || recordKeys[0] == "" || recordKeys[0] != recordKeys[1] {
		t.Fatalf("inner operation IDs = %#v", recordKeys)
	}
	if state.Steps["remote"].(map[string]any)["result"] != "done" {
		t.Fatalf("state = %#v", state)
	}
}

type actionCaptureRunner struct {
	workflowName string
	stepID       string
	value        any
	order        *[]string
	request      *step.Request
}

func (runner actionCaptureRunner) Run(_ context.Context, request step.Request) (step.Result, error) {
	*runner.order = append(*runner.order, request.WorkflowName+":"+request.StepID)
	if runner.request != nil && request.WorkflowName == "composite" {
		*runner.request = request
	}
	variables := map[string]any{}
	if request.WorkflowName == "composite" {
		variables["internal_only"] = true
	}
	return step.Result{Outputs: map[string]any{"value": runner.value}, Variables: variables}, nil
}

func TestCompositeActionRunsSequentiallyWithTypedInputsAndDeclaredOutputs(t *testing.T) {
	var order []string
	var innerRequest step.Request
	registry := newTestRegistry(t, map[string]step.Builder{"action_capture": func(raw map[string]any) (step.Runner, error) {
		return actionCaptureRunner{value: raw["value"], order: &order, request: &innerRequest}, nil
	}})
	action := testAction(t, "composite",
		workflow.Step{ID: "first", Type: "action_capture", With: map[string]any{"value": "first"}},
		workflow.Step{ID: "second", Type: "action_capture", If: "inputs.enabled && len(inputs.items) == 2", With: map[string]any{"value": "done"}},
	)
	action.Inputs = map[string]workflow.ActionInput{
		"items":   {Type: "array", Required: true},
		"enabled": {Type: "boolean", Default: true, HasDefault: true},
	}
	action.Outputs = map[string]workflow.ActionOutput{"result": {Value: `required(steps.second.value, "missing action result")`}}
	definition := testDefinition(t, "caller",
		workflow.Step{ID: "prepare", Type: "action_capture", With: map[string]any{"value": []any{"b", "a"}}},
		workflow.Step{ID: "remote", Uses: workflow.ActionSource{URL: "https://example.test/action@v1"}, Action: action, With: map[string]any{"items": map[string]any{"expr": "list(steps.prepare.value[1], steps.prepare.value[0])"}}},
		workflow.Step{ID: "consume", Type: "action_capture", With: map[string]any{"value": "{{ .steps.remote.result }}"}},
	)
	state, err := New(registry).Run(t.Context(), definition, Options{RunDir: t.TempDir(), Stdout: io.Discard, Stderr: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	wantOrder := []string{"caller:prepare", "composite:first", "composite:second", "caller:consume"}
	if !reflect.DeepEqual(order, wantOrder) {
		t.Fatalf("order = %#v, want %#v", order, wantOrder)
	}
	if got := state.Steps["remote"].(map[string]any); !reflect.DeepEqual(got, map[string]any{"result": "done"}) {
		t.Fatalf("remote outputs = %#v", got)
	}
	if _, exists := state.Steps["first"]; exists {
		t.Fatal("internal step leaked into caller state")
	}
	if _, exists := state.Vars["internal_only"]; exists {
		t.Fatal("internal variable leaked into caller state")
	}
	if got := innerRequest.Inputs["items"]; !reflect.DeepEqual(got, []any{"a", "b"}) {
		t.Fatalf("inner inputs = %#v", innerRequest.Inputs)
	}
}

func TestCompositeActionKeepsCallerAndActionTemplatesIsolated(t *testing.T) {
	var order []string
	registry := newTestRegistry(t, map[string]step.Builder{"action_capture": func(raw map[string]any) (step.Runner, error) {
		return actionCaptureRunner{value: raw["value"], order: &order}, nil
	}})
	action := testAction(t, "composite", workflow.Step{
		ID: "render", Type: "action_capture", With: map[string]any{"value": `{{ template "value" . }}`},
	})
	action.Templates = map[string]workflow.TemplateDefinition{
		"value": {Inline: `action={{ .inputs.target }}`},
	}
	action.Inputs = map[string]workflow.ActionInput{"target": {Type: "string", Required: true}}
	action.Outputs = map[string]workflow.ActionOutput{"result": {Value: "steps.render.value"}}
	definition := testDefinition(t, "caller", workflow.Step{
		ID: "remote", Uses: workflow.ActionSource{URL: "https://example.test/action"}, Action: action,
		With: map[string]any{"target": `{{ template "value" . }}`},
	})
	definition.Vars = map[string]any{"target": "linux"}
	definition.Templates = map[string]workflow.TemplateDefinition{
		"value": {Inline: `caller={{ .vars.target }}`},
	}
	state, err := New(registry).Run(t.Context(), definition, Options{RunDir: t.TempDir(), Stdout: io.Discard, Stderr: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if got := state.Steps["remote"].(map[string]any)["result"]; got != "action=caller=linux" {
		t.Fatalf("result = %v", got)
	}
}

func TestCompositeActionRejectsInputContractViolations(t *testing.T) {
	registry := newTestRegistry(t, nil)
	action := &workflow.Action{Version: 1, Name: "action", Inputs: map[string]workflow.ActionInput{"name": {Type: "string", Required: true}}, Steps: []workflow.Step{{ID: "run", Type: "capture", With: map[string]any{}}}}
	definition := testDefinition(t, "caller", workflow.Step{ID: "remote", Uses: workflow.ActionSource{URL: "https://example.test"}, Action: action, With: map[string]any{"unknown": true}})
	err := New(registry).Validate(t.Context(), definition, Options{Stdout: io.Discard, Stderr: io.Discard})
	if err == nil || !strings.Contains(err.Error(), "unknown input") {
		t.Fatalf("error = %v", err)
	}
}

func TestCompositeActionValidateRejectsStaticInputTypeMismatch(t *testing.T) {
	registry := newTestRegistry(t, nil)
	action := &workflow.Action{Version: 1, Name: "action", Inputs: map[string]workflow.ActionInput{"name": {Type: "string", Required: true}}, Steps: []workflow.Step{{ID: "run", Type: "capture", With: map[string]any{}}}}
	definition := testDefinition(t, "caller", workflow.Step{ID: "remote", Uses: workflow.ActionSource{URL: "https://example.test"}, Action: action, With: map[string]any{"name": true}})
	err := New(registry).Validate(t.Context(), definition, Options{Stdout: io.Discard, Stderr: io.Discard})
	if err == nil || !strings.Contains(err.Error(), "does not match type string") {
		t.Fatalf("error = %v", err)
	}
}

func TestCompositeActionDryRunPrintsNestedPlan(t *testing.T) {
	registry := newTestRegistry(t, map[string]step.Builder{"capture": func(map[string]any) (step.Runner, error) { return countingRunner{}, nil }})
	action := testAction(t, "action", workflow.Step{ID: "inside", Type: "capture"})
	definition := testDefinition(t, "caller", workflow.Step{ID: "remote", Uses: workflow.ActionSource{URL: "https://example.test/action"}, Action: action, With: map[string]any{}})
	var output bytes.Buffer
	if _, err := New(registry).Run(t.Context(), definition, Options{DryRun: true, Stdout: &output, Stderr: io.Discard}); err != nil {
		t.Fatal(err)
	}
	if got := output.String(); !strings.Contains(got, "remote (uses https://example.test/action)") || !strings.Contains(got, "inside (capture)") {
		t.Fatalf("output = %q", got)
	}
}

func TestPackagedActionResolvesRelativeLuaFileFromActionRoot(t *testing.T) {
	registry := newTestRegistry(t, nil)
	if err := luastep.Register(registry); err != nil {
		t.Fatal(err)
	}
	action := &workflow.Action{
		Version: 1, Name: "packaged",
		Outputs: map[string]workflow.ActionOutput{"result": {Value: "steps.script.value"}},
		Steps:   []workflow.Step{{ID: "script", Type: "lua", With: map[string]any{"file": "scripts/action.lua"}}},
		Files:   map[string]workflow.ActionFile{"scripts/action.lua": {Data: []byte(`wuko.output("value", "from-package")`), Mode: 0o644}},
	}
	definition := testDefinition(t, "caller", workflow.Step{ID: "remote", Uses: workflow.ActionSource{URL: "https://example.test/package"}, Action: action, With: map[string]any{}})
	state, err := New(registry).Run(t.Context(), definition, Options{RunDir: t.TempDir(), Stdout: io.Discard, Stderr: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if got := state.Steps["remote"].(map[string]any)["result"]; got != "from-package" {
		t.Fatalf("result = %#v", got)
	}
}
