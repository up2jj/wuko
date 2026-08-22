package workflow

import (
	"strings"
	"testing"
)

func TestValidateBlock(t *testing.T) {
	tests := []struct {
		name string
		step Step
		want string
	}{
		{name: "ordinary step", step: Step{ID: "run", Type: "shell"}},
		{name: "valid conditional", step: Step{If: "true", Steps: []Step{{ID: "run", Type: "shell"}}}},
		{name: "conditional missing if", step: Step{Steps: []Step{{ID: "run", Type: "shell"}}}, want: "must set if"},
		{name: "conditional mixed fields", step: Step{ID: "block", If: "true", Steps: []Step{{ID: "run", Type: "shell"}}}, want: "cannot be combined"},
		{name: "valid working directory", step: Step{WorkingDirectory: "build", Steps: []Step{{ID: "run", Type: "shell"}}}},
		{name: "working directory empty children", step: Step{WorkingDirectory: "build", Steps: []Step{}}, want: "at least one step"},
		{name: "working directory mixed fields", step: Step{ID: "block", WorkingDirectory: "build", Steps: []Step{{ID: "run", Type: "shell"}}}, want: "cannot be combined"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.step.ValidateBlock()
			if test.want == "" {
				if err != nil {
					t.Fatalf("ValidateBlock() error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ValidateBlock() error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestDefinitionValidateStructure(t *testing.T) {
	zeroTimeout := Duration(0)
	step := func(id string) Step { return Step{ID: id, Type: "shell"} }
	tests := []struct {
		name    string
		steps   []Step
		finally []Step
		want    string
	}{
		{name: "ordinary step", steps: []Step{step("run")}},
		{name: "action", steps: []Step{{ID: "run", Uses: ActionSource{URL: "https://example.test/action"}}}},
		{name: "conditional", steps: []Step{{If: "true", Steps: []Step{step("run")}}}},
		{name: "working directory", steps: []Step{{WorkingDirectory: "build", Steps: []Step{step("run")}}}},
		{name: "concurrent", steps: []Step{{Concurrent: &ConcurrentGroup{Steps: []Step{step("one"), step("two")}, MaxConcurrency: 2}}}},
		{name: "foreach", steps: []Step{{ID: "loop", Foreach: &ForeachGroup{Items: "[]", Steps: []Step{step("run")}, MaxConcurrency: 1}}}},
		{name: "matrix", steps: []Step{{ID: "loop", Matrix: &MatrixGroup{Axes: MatrixAxes{{Name: "os", Values: []any{"linux"}}}, Steps: []Step{step("run")}, MaxConcurrency: 1}}}},
		{name: "executor", steps: []Step{{Executor: &ExecutorScope{Type: "docker"}, Steps: []Step{step("run")}}}},
		{name: "return", steps: []Step{{Return: &ReturnControl{Outputs: map[string]string{}}}}},
		{name: "finally", steps: []Step{step("run")}, finally: []Step{step("cleanup")}},
		{name: "duplicate ids", steps: []Step{step("run"), step("run")}, want: `duplicate step id "run"`},
		{name: "nested conditional", steps: []Step{{If: "true", Steps: []Step{{If: "true", Steps: []Step{step("run")}}}}}, want: "nested conditional"},
		{name: "nested concurrent", steps: []Step{{Concurrent: &ConcurrentGroup{MaxConcurrency: 2, Steps: []Step{{Concurrent: &ConcurrentGroup{MaxConcurrency: 2, Steps: []Step{step("one"), step("two")}}}, step("three")}}}}, want: "nested concurrent"},
		{
			name: "nested fanout",
			steps: []Step{{ID: "outer", Foreach: &ForeachGroup{
				Items: "[]", MaxConcurrency: 1,
				Steps: []Step{{ID: "inner", Matrix: &MatrixGroup{
					Axes: MatrixAxes{{Name: "os", Values: []any{"linux"}}}, MaxConcurrency: 1,
					Steps: []Step{step("run")},
				}}},
			}}},
			want: "nested matrix",
		},
		{name: "return in finally", steps: []Step{step("run")}, finally: []Step{{Return: &ReturnControl{Outputs: map[string]string{}}}}, want: "not supported inside finally"},
		{name: "invalid execution policy", steps: []Step{{ID: "run", Type: "shell", Timeout: &zeroTimeout}}, want: "timeout must be greater than zero"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			definition := &Definition{Version: 1, Name: "test", Steps: test.steps, Finally: test.finally}
			err := definition.ValidateStructure()
			if test.want == "" {
				if err != nil {
					t.Fatalf("ValidateStructure() error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ValidateStructure() error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestValidateStructureDoesNotParseTemplates(t *testing.T) {
	definition := &Definition{
		Version: 1,
		Name:    "templates",
		Steps:   []Step{{ID: "run", Type: "shell"}},
		Templates: map[string]TemplateDefinition{
			"broken": {Inline: "{{"},
		},
	}
	if err := definition.ValidateStructure(); err != nil {
		t.Fatalf("ValidateStructure() error = %v", err)
	}
	if _, err := NewRenderer(definition.Templates); err == nil {
		t.Fatal("expected renderer validation error")
	}
}
