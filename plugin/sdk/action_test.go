package sdk

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/up2jj/wuko/workflow"
)

func TestRegisterActionSnapshotsStructuredValues(t *testing.T) {
	plugin, err := New("acme")
	if err != nil {
		t.Fatal(err)
	}
	config := map[string]any{
		"nested": map[string]any{"enabled": true},
		"items":  []any{"one", map[string]any{"two": 2}},
	}
	action := Action{
		Inputs: map[string]ActionInput{
			"options": {Type: ObjectInput, Default: Some(config)},
		},
		Outputs: map[string]ActionOutput{"result": {Value: "steps.run.outputs.result"}},
		Steps:   []ActionStep{{ID: "run", Type: "shell", With: map[string]any{"command": "printf", "args": []any{"%s", "{{ .inputs.options.nested.enabled }}"}}}},
	}
	if err := plugin.RegisterAction("build", action); err != nil {
		t.Fatal(err)
	}
	config["nested"].(map[string]any)["enabled"] = false
	action.Steps[0].With["command"] = "mutated"

	plugin.mu.RLock()
	snapshot := bytes.Clone(plugin.actions["build"])
	plugin.mu.RUnlock()
	if !json.Valid(snapshot) {
		t.Fatalf("snapshot is not JSON: %s", snapshot)
	}
	text := string(snapshot)
	if !strings.Contains(text, `"enabled":true`) || !strings.Contains(text, `"command":"printf"`) {
		t.Fatalf("registered action changed after caller mutation: %s", text)
	}
	if !strings.Contains(text, `"version":1`) || !strings.Contains(text, `"name":"build"`) {
		t.Fatalf("SDK-owned action fields missing: %s", text)
	}
}

func TestRegisterActionRejectsInvalidAndDuplicateNames(t *testing.T) {
	plugin, _ := New("acme")
	valid := Action{Steps: []ActionStep{{ID: "run", Type: "shell"}}}
	if err := plugin.RegisterAction("not-valid", valid); err == nil {
		t.Fatal("expected invalid name error")
	}
	if err := plugin.RegisterAction("build", valid); err != nil {
		t.Fatal(err)
	}
	if err := plugin.RegisterAction("build", valid); err == nil {
		t.Fatal("expected duplicate name error")
	}
}

func TestActionSnapshotConformsToWorkflowDecoder(t *testing.T) {
	plugin, _ := New("acme")
	err := plugin.RegisterAction("build", Action{
		Description: "build an artifact",
		Inputs: map[string]ActionInput{
			"target":  {Type: StringInput, Required: true},
			"options": {Type: ObjectInput, Default: Some(map[string]any{"tags": []any{"latest", map[string]any{"channel": "stable"}}})},
		},
		Outputs:   map[string]ActionOutput{"artifact": {Description: "path", Value: "steps.package.stdout"}},
		Templates: map[string]string{"artifact_name": "app-{{ .inputs.target }}"},
		Steps: []ActionStep{{
			ID:    "package",
			Type:  "shell",
			With:  map[string]any{"command": "printf", "args": []any{"dist/%s.tar.gz", "{{ .inputs.target }}"}, "stdout": "capture"},
			Defer: []ActionStep{{ID: "remove_temp", Type: "shell", With: map[string]any{"command": "true"}}},
		}},
		Finally: []ActionStep{{ID: "report", Type: "shell", With: map[string]any{"command": "true"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := workflow.ValidatePluginAction(plugin.actions["build"], "plugin:acme/build"); err != nil {
		t.Fatalf("SDK action does not conform to Wuko decoder: %v\n%s", err, plugin.actions["build"])
	}
}

func TestEveryActionStepControlConformsToWorkflowDecoder(t *testing.T) {
	leaf := func(id string) ActionStep {
		return ActionStep{ID: id, Type: "shell", With: map[string]any{"command": "true"}}
	}
	tests := map[string]ActionStep{
		"working directory": {WorkingDirectory: "build", Steps: []ActionStep{leaf("run")}},
		"environment":       {Env: map[string]string{"MODE": "test"}, Steps: []ActionStep{leaf("run")}},
		"worktree":          {ID: "tree", Worktree: &WorktreeGroup{Revision: "HEAD", Path: "auto", Publish: &WorktreePublish{Branch: "result"}, Steps: []ActionStep{leaf("run")}}},
		"conditional":       {If: "true", Steps: []ActionStep{leaf("run")}},
		"concurrent":        {Concurrent: &ConcurrentGroup{Steps: []ActionStep{leaf("one"), leaf("two")}, MaxConcurrency: 2, Timeout: "1m", FailFast: Bool(false)}},
		"batch":             {ID: "batch", Batch: &BatchGroup{Items: "[1, 2]", Size: 1, Collect: "steps.run.stdout", Steps: []ActionStep{leaf("run")}, MaxConcurrency: 1, MaxIterations: 2, Timeout: "1m", FailFast: Bool(true)}},
		"foreach":           {ID: "each", Foreach: &ForeachGroup{Items: "[1]", Collect: "steps.run.stdout", Steps: []ActionStep{leaf("run")}, MaxConcurrency: 1, MaxIterations: 2, Timeout: "1m", FailFast: Bool(true)}},
		"matrix":            {ID: "matrix", Matrix: &MatrixGroup{Axes: MatrixAxes{{Name: "os", Values: []any{"linux"}}, {Name: "arch", Expression: `["arm64"]`}}, Collect: "steps.run.stdout", Steps: []ActionStep{leaf("run")}, MaxConcurrency: 1, MaxIterations: 2, Timeout: "1m", FailFast: Bool(true)}},
		"loop":              {ID: "loop", Loop: &LoopGroup{Until: "true", Delay: "1ms", Steps: []ActionStep{leaf("run")}, MaxIterations: 2, Timeout: "1m"}},
		"once":              {ID: "once", Once: &OnceGroup{Key: "key", Scope: "local", OnBusy: "wait", Steps: []ActionStep{leaf("run")}}},
		"attempt":           {ID: "attempt", Attempt: &AttemptControl{Steps: []ActionStep{leaf("run")}, Timeout: "1m", MaxAttempts: 2, InitialDelay: "1ms", BackoffMultiplier: 2.0, MaxDelay: "1s", Jitter: 0.1, Methods: []string{"GET"}, Statuses: []Status{{From: 500, To: 599}}, Until: "true", Interval: "1ms", MaxElapsedTime: "2m", OperationID: "fetch"}},
		"cancel on":         {ID: "race", CancelOn: &CancelOnGroup{Monitors: []ActionStep{leaf("stop")}, Steps: []ActionStep{leaf("run")}, Collect: "steps.run.stdout"}},
		"try catch":         {ID: "recover", Try: &TryBlock{Steps: []ActionStep{leaf("try_step")}}, Catch: &CatchBlock{Steps: []ActionStep{leaf("catch_step")}}},
		"defer":             {ID: "run", Type: "shell", With: map[string]any{"command": "true"}, Defer: []ActionStep{leaf("cleanup")}},
	}
	for name, root := range tests {
		t.Run(name, func(t *testing.T) {
			plugin, _ := New("acme")
			if err := plugin.RegisterAction("control", Action{Steps: []ActionStep{root}, Finally: []ActionStep{leaf("finalize")}}); err != nil {
				t.Fatal(err)
			}
			if err := workflow.ValidatePluginAction(plugin.actions["control"], "plugin:acme/control"); err != nil {
				t.Fatalf("control does not conform: %v\n%s", err, plugin.actions["control"])
			}
		})
	}

	plugin, _ := New("acme")
	if err := plugin.RegisterAction("early", Action{Outputs: map[string]ActionOutput{"value": {Value: "inputs.value"}}, Steps: []ActionStep{{Return: &ReturnControl{Outputs: map[string]string{"value": "inputs.value"}}}}}); err != nil {
		t.Fatal(err)
	}
	if err := workflow.ValidatePluginAction(plugin.actions["early"], "plugin:acme/early"); err != nil {
		t.Fatalf("return control does not conform: %v\n%s", err, plugin.actions["early"])
	}
}

func TestServeAdvertisesAndReturnsActions(t *testing.T) {
	plugin, _ := New("acme")
	if err := plugin.RegisterAction("build", Action{Steps: []ActionStep{{ID: "run", Type: "shell"}}}); err != nil {
		t.Fatal(err)
	}
	inputReader, inputWriter := io.Pipe()
	outputReader, outputWriter := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- plugin.Serve(context.Background(), inputReader, outputWriter) }()
	encoder := json.NewEncoder(inputWriter)
	decoder := json.NewDecoder(outputReader)

	if err := encoder.Encode(map[string]any{"id": "1", "method": "initialize", "params": map[string]any{"protocol": Protocol}}); err != nil {
		t.Fatal(err)
	}
	var initialized struct {
		Result struct {
			Protocol string   `json:"protocol"`
			Actions  []string `json:"actions"`
		} `json:"result"`
	}
	if err := decoder.Decode(&initialized); err != nil {
		t.Fatal(err)
	}
	if initialized.Result.Protocol != Protocol || len(initialized.Result.Actions) != 1 || initialized.Result.Actions[0] != "build" {
		t.Fatalf("unexpected initialize response: %+v", initialized.Result)
	}

	if err := encoder.Encode(map[string]any{"id": "2", "method": "action.get", "params": map[string]any{"name": "build"}}); err != nil {
		t.Fatal(err)
	}
	var fetched struct {
		Result struct {
			Action struct {
				Version int    `json:"version"`
				Name    string `json:"name"`
			} `json:"action"`
		} `json:"result"`
	}
	if err := decoder.Decode(&fetched); err != nil {
		t.Fatal(err)
	}
	if fetched.Result.Action.Version != 1 || fetched.Result.Action.Name != "build" {
		t.Fatalf("unexpected action.get response: %+v", fetched.Result.Action)
	}
	if err := inputWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := plugin.RegisterAction("later", validAction()); err == nil {
		t.Fatal("expected registrations to remain frozen")
	}
}

func validAction() Action {
	return Action{Steps: []ActionStep{{ID: "run", Type: "shell"}}}
}
