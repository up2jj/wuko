package workflow

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTryCatchLoadsAsNamedControl(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workflow.yaml")
	data := `version: 1
name: recovery
steps:
  - id: deployment
    if: vars.enabled
    try:
      steps:
        - {id: deploy, type: shell, with: {command: ./deploy.sh}}
    catch:
      steps:
        - {id: rollback, type: shell, with: {command: ./rollback.sh}}
`
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	definition, err := NewLoader(nil).Decode(path, LoadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	control := definition.Steps[0]
	if !control.IsTryCatch() || control.ID != "deployment" || control.If != "vars.enabled" {
		t.Fatalf("control = %#v", control)
	}
	children := control.ChildSequences()
	if len(children) != 2 || children[0].Role != ChildTry || children[1].Role != ChildCatch {
		t.Fatalf("children = %#v", children)
	}
}

func TestTryCatchStructureValidation(t *testing.T) {
	ordinary := func(id string) Step { return Step{ID: id, Type: "shell"} }
	valid := func() Step {
		return Step{ID: "deployment", Try: &TryBlock{Steps: []Step{ordinary("deploy")}}, Catch: &CatchBlock{Steps: []Step{ordinary("rollback")}}}
	}
	tests := []struct {
		name string
		step Step
		want string
	}{
		{name: "valid", step: valid()},
		{name: "missing id", step: Step{Try: &TryBlock{Steps: []Step{ordinary("deploy")}}, Catch: &CatchBlock{Steps: []Step{ordinary("rollback")}}}, want: "valid id"},
		{name: "missing catch", step: Step{ID: "deployment", Try: &TryBlock{Steps: []Step{ordinary("deploy")}}}, want: "declared together"},
		{name: "empty try", step: Step{ID: "deployment", Try: &TryBlock{}, Catch: &CatchBlock{Steps: []Step{ordinary("rollback")}}}, want: "try must contain"},
		{name: "mixed fields", step: func() Step { step := valid(); step.Type = "shell"; return step }(), want: "cannot be combined"},
		{name: "return", step: Step{ID: "deployment", Try: &TryBlock{Steps: []Step{{Return: &ReturnControl{Outputs: map[string]string{}}}}}, Catch: &CatchBlock{Steps: []Step{ordinary("rollback")}}}, want: "return is not supported"},
		{name: "nested", step: Step{ID: "deployment", Try: &TryBlock{Steps: []Step{{ID: "inner", Try: &TryBlock{Steps: []Step{ordinary("run")}}, Catch: &CatchBlock{Steps: []Step{ordinary("rescue")}}}}}, Catch: &CatchBlock{Steps: []Step{ordinary("rollback")}}}, want: "nested try/catch"},
		{name: "inside cancel on", step: Step{ID: "watch", CancelOn: &CancelOnGroup{Monitors: []Step{ordinary("monitor")}, Steps: []Step{valid()}}}, want: "try/catch is not supported inside cancel_on"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := (&Definition{Version: 1, Name: "test", Steps: []Step{test.step}}).ValidateStructure()
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

func TestTryCatchKeepsEnclosingScopeRestrictions(t *testing.T) {
	control := Step{
		ID:    "deployment",
		Try:   &TryBlock{Steps: []Step{{Concurrent: &ConcurrentGroup{MaxConcurrency: 1, Steps: []Step{{ID: "one", Type: "shell"}, {ID: "two", Type: "shell"}}}}}},
		Catch: &CatchBlock{Steps: []Step{{ID: "rollback", Type: "shell"}}},
	}
	definition := &Definition{Version: 1, Name: "test", Steps: []Step{{
		Concurrent: &ConcurrentGroup{MaxConcurrency: 1, Steps: []Step{control, Step{ID: "sibling", Type: "shell"}}},
	}}}
	err := definition.ValidateStructure()
	if err == nil || !strings.Contains(err.Error(), "nested concurrent groups are not supported") {
		t.Fatalf("error = %v", err)
	}
}
