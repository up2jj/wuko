package workflow

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCancelOnLoadsAndExposesChildRoles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workflow.yaml")
	data := `version: 1
name: monitored
steps:
  - id: deployment_watch
    cancel_on:
      monitors:
        - id: deployment_finished
          type: wait
          with: {duration: 1s}
      steps:
        - id: deploy
          type: shell
          with: {command: ./deploy}
      collect: '{"ok": cancel_on.ok}'
`
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	definition, err := NewLoader(nil).Decode(path, LoadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	control := definition.Steps[0]
	if !control.IsCancelOn() || control.ID != "deployment_watch" || control.CancelOn.Collect == "" {
		t.Fatalf("control = %#v", control)
	}
	children := control.ChildSequences()
	if len(children) != 2 || children[0].Role != ChildMonitors || children[1].Role != ChildSteps {
		t.Fatalf("children = %#v", children)
	}
}

func TestCancelOnStructureValidation(t *testing.T) {
	step := func(id string) Step { return Step{ID: id, Type: "shell"} }
	tests := []struct {
		name string
		step Step
		want string
	}{
		{name: "valid", step: Step{ID: "watch", CancelOn: &CancelOnGroup{Monitors: []Step{step("monitor")}, Steps: []Step{step("body")}}}},
		{name: "missing id", step: Step{CancelOn: &CancelOnGroup{Monitors: []Step{step("monitor")}, Steps: []Step{step("body")}}}, want: "invalid id"},
		{name: "no monitors", step: Step{ID: "watch", CancelOn: &CancelOnGroup{Steps: []Step{step("body")}}}, want: "at least one monitor"},
		{name: "duplicate monitor", step: Step{ID: "watch", CancelOn: &CancelOnGroup{Monitors: []Step{step("monitor"), step("monitor")}, Steps: []Step{step("body")}}}, want: "duplicate"},
		{name: "return", step: Step{ID: "watch", CancelOn: &CancelOnGroup{Monitors: []Step{step("monitor")}, Steps: []Step{{Return: &ReturnControl{Outputs: map[string]string{}}}}}}, want: "return is not supported"},
		{name: "require", step: Step{ID: "watch", CancelOn: &CancelOnGroup{Monitors: []Step{step("monitor")}, Steps: []Step{{Require: stringPointer("steps.yaml")}}}}, want: "require is not supported"},
		{name: "defer", step: Step{ID: "watch", CancelOn: &CancelOnGroup{Monitors: []Step{step("monitor")}, Steps: []Step{{ID: "body", Type: "shell", Defer: []Step{step("cleanup")}}}}}, want: "defer is not supported"},
		{name: "nested", step: Step{ID: "watch", CancelOn: &CancelOnGroup{Monitors: []Step{step("monitor")}, Steps: []Step{{ID: "nested", CancelOn: &CancelOnGroup{Monitors: []Step{step("inner_monitor")}, Steps: []Step{step("inner_body")}}}}}}, want: "nested cancel_on"},
		{name: "observe monitor", step: Step{ID: "watch", CancelOn: &CancelOnGroup{Monitors: []Step{observeStep("watcher")}, Steps: []Step{step("body")}}}, want: "observe is not supported inside cancel_on"},
		{name: "observe body", step: Step{ID: "watch", CancelOn: &CancelOnGroup{Monitors: []Step{step("monitor")}, Steps: []Step{observeStep("watcher")}}}, want: "observe is not supported inside cancel_on"},
		{name: "observe nested in body", step: Step{ID: "watch", CancelOn: &CancelOnGroup{Monitors: []Step{step("monitor")}, Steps: []Step{{Concurrent: &ConcurrentGroup{MaxConcurrency: 1, Steps: []Step{observeStep("watcher"), step("sibling")}}}}}}, want: "observe is not supported inside cancel_on"},
		{name: "too many monitors", step: Step{ID: "watch", CancelOn: &CancelOnGroup{Monitors: manyMonitors(101), Steps: []Step{step("body")}}}, want: "cancel_on supports at most 100 monitors"},
		{name: "monitor ceiling", step: Step{ID: "watch", CancelOn: &CancelOnGroup{Monitors: manyMonitors(100), Steps: []Step{step("body")}}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			definition := &Definition{Version: 1, Name: "test", Steps: []Step{test.step}}
			err := definition.ValidateStructure()
			if test.want == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func stringPointer(value string) *string { return &value }

func observeStep(id string) Step {
	return Step{ID: id, Observe: &ObserveGroup{
		Source: ObserveSource{Type: "filesystem", With: map[string]any{"paths": []any{"**"}}},
		Steps:  []Step{{ID: id + "_body", Type: "shell"}},
	}}
}

func manyMonitors(count int) []Step {
	monitors := make([]Step, count)
	for index := range monitors {
		monitors[index] = Step{ID: fmt.Sprintf("monitor_%d", index), Type: "shell"}
	}
	return monitors
}

func TestCancelOnAllowsLabelsOnAnonymousMonitors(t *testing.T) {
	definition := &Definition{Version: 1, Name: "test", Steps: []Step{{
		ID: "watch", CancelOn: &CancelOnGroup{
			Monitors: []Step{{ID: "parallel", Concurrent: &ConcurrentGroup{MaxConcurrency: 1, Steps: []Step{{ID: "check_one", Type: "shell"}, {ID: "check_two", Type: "shell"}}}}},
			Steps:    []Step{{ID: "body", Type: "shell"}},
		},
	}}}
	if err := definition.ValidateStructure(); err != nil {
		t.Fatal(err)
	}
}

func TestCancelOnIsAnOrdinaryConcurrentChild(t *testing.T) {
	control := Step{ID: "watch", CancelOn: &CancelOnGroup{
		Monitors: []Step{{ID: "monitor", Type: "shell"}},
		Steps:    []Step{{ID: "body", Type: "shell"}},
	}}
	definition := &Definition{Version: 1, Name: "test", Steps: []Step{{Concurrent: &ConcurrentGroup{
		MaxConcurrency: 2,
		Steps:          []Step{control, {ID: "sibling", Type: "shell"}},
	}}}}
	if err := definition.ValidateStructure(); err != nil {
		t.Fatal(err)
	}
}

func TestCancelOnRejectsRequireBeforeFragmentExpansion(t *testing.T) {
	root := t.TempDir()
	fragment := filepath.Join(root, "fragment.yaml")
	if err := os.WriteFile(fragment, []byte("- {id: expanded, type: shell}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	definition := `version: 1
name: monitored
steps:
  - id: watch
    cancel_on:
      monitors:
        - {id: monitor, type: shell}
      steps:
        - require: fragment.yaml
`
	path := filepath.Join(root, "workflow.yaml")
	if err := os.WriteFile(path, []byte(definition), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := NewLoader(nil).Decode(path, LoadOptions{})
	if err == nil || !strings.Contains(err.Error(), "require is not supported inside cancel_on") {
		t.Fatalf("error = %v", err)
	}
}

func TestCancelOnKeepsEnclosingScopeRestrictions(t *testing.T) {
	step := func(id string) Step { return Step{ID: id, Type: "shell"} }
	control := func(body ...Step) Step {
		return Step{ID: "watch", CancelOn: &CancelOnGroup{Monitors: []Step{step("monitor")}, Steps: body}}
	}
	tests := []struct {
		name string
		step Step
		want string
	}{
		{
			name: "inside executor",
			step: Step{Executor: &ExecutorScope{Type: "docker"}, Steps: []Step{control(step("body"))}},
			want: "cancel_on controls are not supported inside executor blocks",
		},
		{
			name: "concurrent body inside concurrent group",
			step: Step{Concurrent: &ConcurrentGroup{MaxConcurrency: 1, Steps: []Step{
				control(Step{Concurrent: &ConcurrentGroup{MaxConcurrency: 1, Steps: []Step{step("one"), step("two")}}}),
				step("sibling"),
			}}},
			want: "nested concurrent groups are not supported",
		},
		{
			name: "executor monitor inside concurrent group",
			step: Step{Concurrent: &ConcurrentGroup{MaxConcurrency: 1, Steps: []Step{{
				ID: "watch", CancelOn: &CancelOnGroup{
					Monitors: []Step{{ID: "container", Executor: &ExecutorScope{Type: "docker"}, Steps: []Step{step("wait")}}},
					Steps:    []Step{step("body")},
				},
			}, step("sibling")}}},
			want: "executor blocks are only supported in sequential workflow scopes",
		},
		{
			name: "foreach body inside foreach",
			step: Step{ID: "outer", Foreach: &ForeachGroup{Items: "[]", MaxConcurrency: 1, Steps: []Step{
				control(Step{ID: "inner", Foreach: &ForeachGroup{Items: "[]", MaxConcurrency: 1, Steps: []Step{step("run")}}}),
			}}},
			want: "nested foreach controls are not supported",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			definition := &Definition{Version: 1, Name: "test", Steps: []Step{test.step}}
			err := definition.ValidateStructure()
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}
