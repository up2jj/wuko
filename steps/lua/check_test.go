package lua

import (
	"testing"

	"github.com/up2jj/wuko/internal/steptest"
	"github.com/up2jj/wuko/step"
)

func TestLuaChecksAreIndependentSnapshots(t *testing.T) {
	t.Parallel()
	request := step.Request{Checks: []step.CheckFinding{{ID: "tool", Status: "fail", Message: "missing", Fix: "install"}}}
	runner := steptest.Build(t, New, map[string]any{"source": `assert(#wuko.checks == 1); wuko.output("status", wuko.checks[1].status); wuko.checks[1].status = "ok"`})
	result, err := runner.Run(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outputs["status"] != "fail" || request.Checks[0].Status != "fail" {
		t.Fatalf("result = %#v, checks = %#v", result, request.Checks)
	}
	predicate, err := CompilePredicate(`function(value, context) return #context.checks == 1 and context.checks[1].id == value end`)
	if err != nil {
		t.Fatal(err)
	}
	matched, err := predicate.Evaluate(t.Context(), request, "tool")
	if err != nil || !matched {
		t.Fatalf("matched = %v, error = %v", matched, err)
	}
}
