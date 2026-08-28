package workflow

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestOnceGroupDefaultsAndValidation(t *testing.T) {
	t.Parallel()
	var declaration struct {
		Once OnceGroup `yaml:"once"`
	}
	data := `once:
  key: schema-v1
  scope: local
  steps:
    - id: apply
      type: shell
`
	if err := yaml.Unmarshal([]byte(data), &declaration); err != nil {
		t.Fatal(err)
	}
	if declaration.Once.OnBusy != OnceBusyError {
		t.Fatalf("on_busy = %q, want %q", declaration.Once.OnBusy, OnceBusyError)
	}
	if err := declaration.Once.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestOnceGroupRejectsInvalidDeclarations(t *testing.T) {
	t.Parallel()
	step := Step{ID: "run", Type: "shell"}
	tests := []struct {
		name  string
		group OnceGroup
		want  string
	}{
		{name: "missing key", group: OnceGroup{Scope: "local", OnBusy: OnceBusyError, Steps: []Step{step}}, want: "key is required"},
		{name: "bad scope", group: OnceGroup{Key: "key", Scope: "project", OnBusy: OnceBusyError, Steps: []Step{step}}, want: "scope"},
		{name: "bad busy policy", group: OnceGroup{Key: "key", Scope: "local", OnBusy: "skip", Steps: []Step{step}}, want: "on_busy"},
		{name: "missing steps", group: OnceGroup{Key: "key", Scope: "local", OnBusy: OnceBusyError}, want: "at least one"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.group.Validate()
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestOnceStructureAndChildren(t *testing.T) {
	t.Parallel()
	child := Step{ID: "apply", Type: "shell"}
	definition := &Definition{
		Version: 1,
		Name:    "migration",
		Steps: []Step{{
			ID: "migrate", If: "true",
			Once: &OnceGroup{Key: "schema-v1", Scope: "local", OnBusy: OnceBusyError, Steps: []Step{child}},
		}},
	}
	if err := definition.ValidateStructure(); err != nil {
		t.Fatal(err)
	}
	children := definition.Steps[0].ChildSequences()
	if len(children) != 1 || children[0].Role != ChildSteps || len(children[0].Steps) != 1 || children[0].Steps[0].ID != "apply" {
		t.Fatalf("children = %#v", children)
	}
}

func TestOnceRejectsCleanupAndDeferredChildren(t *testing.T) {
	t.Parallel()
	once := Step{ID: "migrate", Once: &OnceGroup{
		Key: "schema-v1", Scope: "local", OnBusy: OnceBusyError,
		Steps: []Step{{ID: "apply", Type: "shell", Defer: []Step{{ID: "undo", Type: "shell"}}}},
	}}
	definition := &Definition{Version: 1, Name: "migration", Steps: []Step{{ID: "run", Type: "shell"}}, Finally: []Step{once}}
	if err := definition.ValidateStructure(); err == nil || !strings.Contains(err.Error(), "cleanup") {
		t.Fatalf("cleanup error = %v", err)
	}
	definition.Finally = nil
	definition.Steps = []Step{once}
	if err := definition.ValidateStructure(); err == nil || !strings.Contains(err.Error(), "defer") {
		t.Fatalf("defer error = %v", err)
	}
}

func TestOnceRejectsReturnAnywhereInBody(t *testing.T) {
	t.Parallel()
	// The body runs on a private state clone whose returning flag never propagates, so a
	// return would be swallowed and the swallowing recorded durably.
	early := Step{Return: &ReturnControl{Outputs: map[string]string{"early": `"yes"`}}}
	for name, body := range map[string][]Step{
		"direct": {{ID: "hello", Type: "shell"}, early},
		"nested": {{If: "true", Steps: []Step{early}}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			definition := &Definition{Version: 1, Name: "migration", Steps: []Step{{
				ID:   "gate",
				Once: &OnceGroup{Key: "schema-v1", Scope: "local", OnBusy: OnceBusyError, Steps: body},
			}}}
			if err := definition.ValidateStructure(); err == nil || !strings.Contains(err.Error(), "return is not supported inside once") {
				t.Fatalf("return error = %v", err)
			}
		})
	}
}

func TestOnceBodyKeepsEnclosingScopeRestrictions(t *testing.T) {
	t.Parallel()
	// A once does not reset the nesting it sits in: an executor block is top-level only,
	// and wrapping it in a once inside a foreach must not smuggle it past that rule.
	once := Step{ID: "gate", Once: &OnceGroup{
		Key: "schema-v1", Scope: "local", OnBusy: OnceBusyError,
		Steps: []Step{{Executor: &ExecutorScope{Type: "shell"}, Steps: []Step{{ID: "apply", Type: "shell"}}}},
	}}
	definition := &Definition{Version: 1, Name: "migration", Steps: []Step{{
		ID:      "fan",
		Foreach: &ForeachGroup{Items: "[]", MaxConcurrency: 1, Steps: []Step{once}},
	}}}
	if err := definition.ValidateStructure(); err == nil || !strings.Contains(err.Error(), "executor blocks are only supported in sequential workflow scopes") {
		t.Fatalf("nested executor error = %v", err)
	}
	definition.Steps = []Step{once}
	if err := definition.ValidateStructure(); err != nil {
		t.Fatalf("top-level once = %v", err)
	}
}
