package assert

import (
	"bytes"
	"testing"

	"github.com/up2jj/wuko/internal/steptest"
	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/useroutput"
)

func TestPlainAssertionKeepsDiagnosticErrorAndPrintsOnce(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		message any
		code    int
	}{{"fix setup", 1}, {42, 3}, {map[string]any{"expr": `"computed message"`}, 2}, {map[string]any{"literal": "{{ unchanged }}"}, 255}} {
		raw := map[string]any{"expr": "false", "message": test.message, "plain": true, "exit_code": test.code}
		runner := steptest.Build(t, New, raw)
		var output bytes.Buffer
		_, err := runner.Run(t.Context(), step.Request{Stderr: &output})
		view := useroutput.Inspect(err)
		if view.Ordinary != nil || len(view.Failures) != 1 || view.Failures[0].Code != test.code || !view.Failures[0].Reported {
			t.Fatalf("view=%#v", view)
		}
		if output.String() != view.Failures[0].Message+"\n" || err.Error() != "assertion failed: "+view.Failures[0].Message {
			t.Fatalf("output=%q err=%v", output.String(), err)
		}
	}
}

func TestPlainAssertionDoesNotChangePassingOrEvaluationErrors(t *testing.T) {
	t.Parallel()
	passing := steptest.Build(t, New, map[string]any{"expr": "true", "message": map[string]any{"expr": `vars.unknown.value`}, "plain": true})
	var output bytes.Buffer
	if _, err := passing.Run(t.Context(), step.Request{Stderr: &output}); err != nil || output.Len() != 0 {
		t.Fatalf("passing output=%q err=%v", output.String(), err)
	}
	failing := steptest.Build(t, New, map[string]any{"expr": "false", "message": map[string]any{"expr": `vars.unknown.value`}, "plain": true})
	_, err := failing.Run(t.Context(), step.Request{Stderr: &output})
	if err == nil || useroutput.OnlyPlain(err) || output.Len() != 0 {
		t.Fatalf("evaluation error=%v output=%q", err, output.String())
	}
}

func TestPlainAssertionValidatesExitCode(t *testing.T) {
	t.Parallel()
	for _, raw := range []map[string]any{
		{"expr": "false", "message": "stop", "exit_code": 2},
		{"expr": "false", "message": "stop", "plain": true, "exit_code": 0},
		{"expr": "false", "message": "stop", "plain": true, "exit_code": 256},
		{"expr": "false", "message": "stop", "plain": true, "exit_code": -1},
		{"expr": "false", "message": "stop", "plain": true, "exit_code": 1.5},
	} {
		if _, err := New(raw); err == nil {
			t.Fatalf("accepted %#v", raw)
		}
	}
}
