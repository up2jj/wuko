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
