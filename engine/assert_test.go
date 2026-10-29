package engine_test

import (
	"io"
	"strings"
	"testing"

	"github.com/up2jj/wuko/engine"
	"github.com/up2jj/wuko/internal/steptest"
	assertstep "github.com/up2jj/wuko/steps/assert"
	"github.com/up2jj/wuko/workflow"
)

func TestAssertRendersMessageOnlyWhenItFails(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		expr    string
		message string
		want    string
	}{
		{"passing with an empty message", `vars.problem == ""`, "{{ .vars.problem }}", ""},
		{"passing with an unrenderable message", `vars.problem == ""`, "{{ .vars.problem.detail }}", ""},
		{"failing renders the message", `vars.problem != ""`, "problem: {{ .vars.problem }}!", "assertion failed: problem: !"},
		{"failing with an empty message names the expression", `vars.problem != ""`, "{{ .vars.problem }}", `assertion failed: vars.problem != ""`},
		{"failing with an unrenderable message", `vars.problem != ""`, "{{ .vars.problem.detail }}", `assertion failed: vars.problem != "" (rendering message:`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			definition := steptest.Definition(t, "assert-message", workflow.Step{ID: "check", Type: "assert", With: map[string]any{
				"expr": test.expr, "message": test.message,
			}})
			definition.Vars = map[string]any{"problem": ""}
			_, err := runAssert(t, definition)
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

func TestAssertMessageReferencesAreValidatedBeforeTheRun(t *testing.T) {
	t.Parallel()
	definition := steptest.Definition(t, "assert-message", workflow.Step{ID: "check", Type: "assert", With: map[string]any{
		"expr": "true", "message": "{{ .steps.missing.value }}",
	}})
	if _, err := runAssert(t, definition); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("error = %v", err)
	}
}

func runAssert(t *testing.T, definition *workflow.Definition) (*engine.State, error) {
	t.Helper()
	registry := steptest.Registry(t, nil)
	if err := assertstep.Register(registry); err != nil {
		t.Fatal(err)
	}
	return engine.New(registry).Run(t.Context(), definition, engine.Options{RunDir: t.TempDir(), Stdout: io.Discard, Stderr: io.Discard})
}
