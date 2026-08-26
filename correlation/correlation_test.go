package correlation

import (
	"strings"
	"testing"
)

func TestGeneratedIDsAreTypedPrefixedAndUnique(t *testing.T) {
	invocation := NewInvocationID()
	run := NewRunID()
	step := NewStepRunID()
	if !strings.HasPrefix(string(invocation), "inv_") {
		t.Fatalf("invocation ID = %q", invocation)
	}
	if !strings.HasPrefix(string(run), "run_") {
		t.Fatalf("run ID = %q", run)
	}
	if !strings.HasPrefix(string(step), "step_") {
		t.Fatalf("step run ID = %q", step)
	}
	if invocation == NewInvocationID() || run == NewRunID() || step == NewStepRunID() {
		t.Fatal("generated duplicate correlation ID")
	}
}

func TestIDZeroValuesAreEmpty(t *testing.T) {
	var invocation InvocationID
	var run RunID
	var step StepRunID
	var sequence Sequence
	if invocation != "" || run != "" || step != "" || sequence != 0 {
		t.Fatalf("zero values = %q, %q, %q, %d", invocation, run, step, sequence)
	}
}
