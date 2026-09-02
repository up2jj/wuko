package engine_test

import (
	"testing"

	"github.com/up2jj/wuko/engine"
	gitstep "github.com/up2jj/wuko/steps/git"
	"github.com/up2jj/wuko/workflow"
)

func TestConventionalCommitRendersConfigurationAndDeclaresVariable(t *testing.T) {
	definition := testDefinition(t, "commit", workflow.Step{
		ID: "create", Type: "git_conventional_commit", With: map[string]any{
			"operation": "create", "type": "{{ .vars.type }}", "scope": "workflow",
			"subject": "{{ .vars.subject }}", "task": "{{ .vars.task }}", "variable": "commit_message",
		},
	}, workflow.Step{
		ID: "validate", Type: "git_conventional_commit", With: map[string]any{
			"operation": "validate", "message": "{{ .vars.commit_message }}", "strict": true,
			"task_regex": `WUKO-[0-9]+`,
		},
	})
	definition.Vars = map[string]any{"type": "feat", "subject": "add commit support", "task": "WUKO-142"}
	registry := newTestRegistry(t, nil)
	if err := gitstep.Register(registry); err != nil {
		t.Fatal(err)
	}
	state, err := engine.New(registry).Run(t.Context(), definition, engine.Options{})
	if err != nil {
		t.Fatal(err)
	}
	want := "feat(workflow): add commit support WUKO-142"
	if state.Vars["commit_message"] != want {
		t.Fatalf("variables = %#v", state.Vars)
	}
	validated := state.Steps["validate"].(map[string]any)
	if validated["value"] != want || validated["task"] != "WUKO-142" || validated["subject"] != "add commit support" {
		t.Fatalf("outputs = %#v", validated)
	}
}
