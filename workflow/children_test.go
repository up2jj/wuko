package workflow

import "testing"

func TestStepChildSequences(t *testing.T) {
	t.Parallel()
	step := func(id string) Step { return Step{ID: id, Type: "shell"} }
	tests := []struct {
		name string
		step Step
		want []ChildSequence
	}{
		{name: "ordinary", step: step("run")},
		{name: "defer", step: Step{ID: "run", Type: "shell", Defer: []Step{step("cleanup")}}, want: []ChildSequence{{Role: ChildDefer, Steps: []Step{step("cleanup")}}}},
		{name: "resolved action", step: Step{ID: "call", Action: &Action{Steps: []Step{step("internal")}}}},
		{
			name: "executor",
			step: Step{Executor: &ExecutorScope{Type: "docker"}, Steps: []Step{step("run")}, Finally: []Step{step("cleanup")}},
			want: []ChildSequence{{Role: ChildSteps, Steps: []Step{step("run")}}, {Role: ChildFinally, Steps: []Step{step("cleanup")}}},
		},
		{name: "environment", step: Step{Env: Environment{"MODE": "test"}, Steps: []Step{step("run")}}, want: []ChildSequence{{Role: ChildSteps, Steps: []Step{step("run")}}}},
		{name: "working directory", step: Step{WorkingDirectory: "build", Steps: []Step{step("run")}}, want: []ChildSequence{{Role: ChildSteps, Steps: []Step{step("run")}}}},
		{name: "conditional", step: Step{If: "true", Steps: []Step{step("run")}}, want: []ChildSequence{{Role: ChildSteps, Steps: []Step{step("run")}}}},
		{name: "concurrent", step: Step{Concurrent: &ConcurrentGroup{Steps: []Step{step("run")}}}, want: []ChildSequence{{Role: ChildSteps, Steps: []Step{step("run")}}}},
		{name: "batch", step: Step{Batch: &BatchGroup{Steps: []Step{step("run")}}}, want: []ChildSequence{{Role: ChildSteps, Steps: []Step{step("run")}}}},
		{name: "foreach", step: Step{Foreach: &ForeachGroup{Steps: []Step{step("run")}}}, want: []ChildSequence{{Role: ChildSteps, Steps: []Step{step("run")}}}},
		{name: "matrix", step: Step{Matrix: &MatrixGroup{Steps: []Step{step("run")}}}, want: []ChildSequence{{Role: ChildSteps, Steps: []Step{step("run")}}}},
		{name: "once", step: Step{Once: &OnceGroup{Steps: []Step{step("run")}}}, want: []ChildSequence{{Role: ChildSteps, Steps: []Step{step("run")}}}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := test.step.ChildSequences()
			if len(got) != len(test.want) {
				t.Fatalf("ChildSequences() = %#v, want %#v", got, test.want)
			}
			for i := range got {
				if got[i].Role != test.want[i].Role || len(got[i].Steps) != len(test.want[i].Steps) {
					t.Fatalf("ChildSequences()[%d] = %#v, want %#v", i, got[i], test.want[i])
				}
				for j := range got[i].Steps {
					if got[i].Steps[j].ID != test.want[i].Steps[j].ID {
						t.Fatalf("ChildSequences()[%d].Steps[%d].ID = %q, want %q", i, j, got[i].Steps[j].ID, test.want[i].Steps[j].ID)
					}
				}
			}
		})
	}
}

func TestTransformChildSequencesReplacesDifferentLengthSlices(t *testing.T) {
	t.Parallel()
	workflowStep := Step{Executor: &ExecutorScope{Type: "docker"}, Steps: []Step{{ID: "body"}}, Finally: []Step{{ID: "cleanup"}}}
	err := workflowStep.transformChildSequences(func(role ChildRole, steps []Step) ([]Step, error) {
		if role == ChildFinally {
			return nil, nil
		}
		return append(steps, Step{ID: "expanded"}), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(workflowStep.Steps) != 2 || workflowStep.Steps[1].ID != "expanded" || workflowStep.Finally != nil {
		t.Fatalf("transformed step = %#v", workflowStep)
	}
}
