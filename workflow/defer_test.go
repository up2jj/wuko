package workflow

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadAttachedDeferTracksLocation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workflow.yaml")
	data := []byte(`version: 1
name: attached-defer
steps:
  - id: create
    type: shell
    with: {command: create}
    defer:
      - id: remove
        type: shell
        with: {command: remove}
`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	definition, err := loadLocal(path)
	if err != nil {
		t.Fatal(err)
	}
	deferred := definition.Steps[0].Defer
	if len(deferred) != 1 || deferred[0].ID != "remove" {
		t.Fatalf("defer = %#v", deferred)
	}
	if deferred[0].Location.Source != path || deferred[0].Location.Line == 0 {
		t.Fatalf("defer location = %#v", deferred[0].Location)
	}
}

func TestLoadExpandsRequiredStepsInsideDefer(t *testing.T) {
	dir := t.TempDir()
	fragmentPath := filepath.Join(dir, "cleanup.yaml")
	if err := os.WriteFile(fragmentPath, []byte("steps:\n  - id: remove\n    type: shell\n    with: {command: remove}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "workflow.yaml")
	data := []byte("version: 1\nname: defer-require\nsteps:\n  - id: create\n    type: shell\n    defer:\n      - require: cleanup.yaml\n")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	definition, err := loadLocal(path)
	if err != nil {
		t.Fatal(err)
	}
	deferred := definition.Steps[0].Defer
	if len(deferred) != 1 || deferred[0].ID != "remove" || deferred[0].Location.Source != fragmentPath {
		t.Fatalf("defer = %#v", deferred)
	}
}

func TestAttachedDeferValidation(t *testing.T) {
	step := func(id string) Step { return Step{ID: id, Type: "shell"} }
	tests := []struct {
		name  string
		steps []Step
		final []Step
		want  string
	}{
		{name: "valid", steps: []Step{{ID: "create", Type: "shell", Defer: []Step{step("remove")}}}},
		{name: "empty", steps: []Step{{ID: "create", Type: "shell", Defer: []Step{}}}, want: "must contain at least one step"},
		{name: "duplicate", steps: []Step{{ID: "create", Type: "shell", Defer: []Step{step("create")}}}, want: `duplicate step id "create"`},
		{name: "concurrent", steps: []Step{{Concurrent: &ConcurrentGroup{MaxConcurrency: 2, Steps: []Step{{ID: "create", Type: "shell", Defer: []Step{step("remove")}}, step("other")}}}}, want: "only supported in sequential scopes"},
		{name: "fanout", steps: []Step{{ID: "loop", Foreach: &ForeachGroup{Items: "[]", MaxConcurrency: 1, Steps: []Step{{ID: "create", Type: "shell", Defer: []Step{step("remove")}}}}}}, want: "only supported in sequential scopes"},
		{name: "nested defer", steps: []Step{{ID: "create", Type: "shell", Defer: []Step{{ID: "remove", Type: "shell", Defer: []Step{step("after")}}}}}, want: "not supported inside cleanup"},
		{name: "inside finally", steps: []Step{step("run")}, final: []Step{{ID: "cleanup", Type: "shell", Defer: []Step{step("after")}}}, want: "not supported inside cleanup"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			definition := &Definition{Version: 1, Name: "test", Steps: test.steps, Finally: test.final}
			err := definition.ValidateStructure()
			if test.want == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ValidateStructure() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestDecodeRejectsNonListDefer(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workflow.yaml")
	if err := os.WriteFile(path, []byte("version: 1\nname: invalid\nsteps:\n  - id: run\n    type: shell\n    defer: {id: cleanup, type: shell}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := loadLocal(path)
	if err == nil || !strings.Contains(err.Error(), "defer must be a list") {
		t.Fatalf("loadLocal() error = %v", err)
	}
}
