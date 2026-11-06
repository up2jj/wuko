package engine

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/up2jj/wuko/internal/steptest"
	"github.com/up2jj/wuko/step"
	assertstep "github.com/up2jj/wuko/steps/assert"
	checkstep "github.com/up2jj/wuko/steps/check"
	setstep "github.com/up2jj/wuko/steps/set"
	"github.com/up2jj/wuko/workflow"
)

func findingStep(id, findingID, expression, severity string) workflow.Step {
	return workflow.Step{ID: id, Type: "check", With: map[string]any{"checks": []any{map[string]any{"id": findingID, "expr": expression, "message": "Problem with " + findingID, "fix": "repair " + findingID, "severity": severity}}}}
}

func checksRegistry(t *testing.T) *step.Registry {
	t.Helper()
	registry := steptest.Registry(t, nil)
	for _, register := range []func(*step.Registry) error{checkstep.Register, assertstep.Register, setstep.Register} {
		if err := register(registry); err != nil {
			t.Fatal(err)
		}
	}
	return registry
}

func checkOptions(t *testing.T, finished *ProgressEvent) Options {
	t.Helper()
	return Options{RunDir: t.TempDir(), Stdout: io.Discard, Stderr: io.Discard, Progress: func(event ProgressEvent) {
		if event.Kind == WorkflowFinished && event.Depth == 0 {
			*finished = event
		}
	}}
}

func TestChecksContinueAndFinalizeAfterFinally(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name       string
		severity   string
		recheck    bool
		wantStatus ExecutionStatus
	}{
		{"unresolved failure", "fail", false, StatusFailed},
		{"warning only", "warn", false, StatusSucceeded},
		{"cleanup clears failure", "fail", true, StatusSucceeded},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			definition := steptest.Definition(t, "doctor", findingStep("first", "tool", "false", test.severity),
				workflow.Step{ID: "continued", Type: "assert", With: map[string]any{"expr": `len(checks) == 1 && checks[0].id == "tool" && len(steps.first.checks) == 1`, "message": "continued"}})
			status := "succeeded"
			if test.severity == "fail" {
				status = "failed"
			}
			definition.Finally = []workflow.Step{{ID: "observe", Type: "assert", With: map[string]any{"expr": `finally.status == "` + status + `" && len(checks) == 1`, "message": "wrong finally status"}}}
			if test.recheck {
				definition.Finally = append(definition.Finally, findingStep("fixed", "tool", "true", "fail"))
			}
			var finished ProgressEvent
			state, err := New(checksRegistry(t)).Run(t.Context(), definition, checkOptions(t, &finished))
			if (err != nil) != (test.wantStatus == StatusFailed) {
				t.Fatalf("error = %v", err)
			}
			if finished.Status != test.wantStatus || finished.Stats.Succeeded != len(definition.Steps)+len(definition.Finally) || len(finished.Checks) != 1 {
				t.Fatalf("finish = %#v", finished)
			}
			if test.recheck && (state.Checks[0].Status != "ok" || finished.Checks[0].Status != "ok") {
				t.Fatalf("checks = %#v", state.Checks)
			}
		})
	}
}

func TestChecksReplaceByIDAndKeepFirstPosition(t *testing.T) {
	t.Parallel()
	definition := steptest.Definition(t, "rechecks", findingStep("first", "tool", "false", "fail"), findingStep("other", "credentials", "false", "warn"), findingStep("fixed", "tool", "true", "fail"))
	definition.Outputs = map[string]workflow.WorkflowOutput{"findings": {Type: "array", Value: "checks"}}
	var finished ProgressEvent
	state, err := New(checksRegistry(t)).Run(t.Context(), definition, checkOptions(t, &finished))
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Checks) != 2 || state.Checks[0].ID != "tool" || state.Checks[0].Status != "ok" || state.Checks[1].ID != "credentials" {
		t.Fatalf("checks = %#v", state.Checks)
	}
	if diff := cmp.Diff(step.CheckValues(state.Checks), state.Outputs["findings"]); diff != "" {
		t.Fatalf("outputs mismatch (-want +got):\n%s", diff)
	}
}

func TestChecksValidateNestedReferencesAndFields(t *testing.T) {
	t.Parallel()
	for _, expression := range []string{`steps.missing.available`, `checks[0].typo == "ok"`, `steps.first.checks[0].typo == "ok"`} {
		t.Run(expression, func(t *testing.T) {
			t.Parallel()
			definition := steptest.Definition(t, "invalid", findingStep("first", "tool", "true", "fail"), findingStep("invalid", "other", expression, "fail"))
			if err := New(checksRegistry(t)).Validate(t.Context(), definition, Options{}); err == nil {
				t.Fatal("accepted invalid reference")
			}
		})
	}
}

func TestChecksTemplatesBindingsAndSnapshots(t *testing.T) {
	t.Parallel()
	registry := checksRegistry(t)
	if err := registry.Register("mutate_checks", func(map[string]any) (step.Runner, error) {
		return steptest.RunnerFunc(func(_ context.Context, request step.Request) (step.Result, error) {
			request.Checks[0].Status = "fail"
			request.ExpressionEnvironment(nil)["checks"].([]any)[0].(map[string]any)["status"] = "fail"
			return step.Result{}, nil
		}), nil
	}); err != nil {
		t.Fatal(err)
	}
	first := findingStep("first", "{{ .vars.tool }}", "vars.ready", "{{ .vars.severity }}")
	entry := first.With["checks"].([]any)[0].(map[string]any)
	entry["message"] = map[string]any{"expr": `"Inspect " + vars.tool`}
	definition := steptest.Definition(t, "snapshot", first, workflow.Step{ID: "mutate", Type: "mutate_checks"}, findingStep("observe", "snapshot", `checks[0].status == "ok" && len(checks) == 1`, "fail"))
	definition.Vars = map[string]any{"tool": "node", "ready": true, "severity": "fail"}
	var finished ProgressEvent
	state, err := New(registry).Run(t.Context(), definition, checkOptions(t, &finished))
	if err != nil {
		t.Fatal(err)
	}
	if state.Checks[0].ID != "node" || state.Checks[0].Message != "Inspect node" {
		t.Fatalf("checks = %#v", state.Checks)
	}
}

func TestChecksPropagateThroughSuccessfulGroups(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"action", "concurrent", "foreach", "attempt", "try", "once"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			check := findingStep("inside", "tool", "false", "fail")
			var group workflow.Step
			switch kind {
			case "action":
				group = workflow.Step{ID: "group", Uses: workflow.ActionSource{Path: "action.yaml"}, Action: testAction(t, "action", check)}
			case "concurrent":
				group = workflow.Step{Concurrent: &workflow.ConcurrentGroup{Steps: []workflow.Step{check, findingStep("other", "other", "true", "fail")}, MaxConcurrency: 2}}
			case "foreach":
				group = workflow.Step{ID: "group", Foreach: &workflow.ForeachGroup{Items: `[1,2]`, Steps: []workflow.Step{check, findingStep("other", "other", "true", "fail")}, MaxConcurrency: 2}}
			case "attempt":
				group = attemptStep("group", immediateRetry(2), check)
			case "try":
				group = tryCatchStep([]workflow.Step{check}, []workflow.Step{{ID: "unused", Type: "assert", With: map[string]any{"expr": "true", "message": "unused"}}})
			case "once":
				group = workflow.Step{ID: "group", Once: &workflow.OnceGroup{Key: "check", Scope: "local", OnBusy: "error", Steps: []workflow.Step{check}}}
			}
			definition := steptest.Definition(t, "grouped", group, findingStep("recheck", "tool", `len(checks) >= 1 && checks[0].status == "fail"`, "fail"))
			var finished ProgressEvent
			options := checkOptions(t, &finished)
			options.LocalValueDir = t.TempDir()
			state, err := New(checksRegistry(t)).Run(t.Context(), definition, options)
			if err != nil {
				t.Fatal(err)
			}
			if len(state.Checks) < 1 || state.Checks[0].Status != "ok" {
				t.Fatalf("checks = %#v", state.Checks)
			}
		})
	}
}

func TestChecksConcurrentAndIterationReplacementIsOrdered(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"concurrent", "foreach"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			var group workflow.Step
			if kind == "concurrent" {
				group = workflow.Step{Concurrent: &workflow.ConcurrentGroup{MaxConcurrency: 2, Steps: []workflow.Step{findingStep("bad", "same", "false", "fail"), findingStep("good", "same", "true", "fail")}}}
			} else {
				group = workflow.Step{ID: "group", Foreach: &workflow.ForeachGroup{Items: `[false,true]`, MaxConcurrency: 2, Steps: []workflow.Step{findingStep("check", "same", "foreach.item", "fail")}}}
			}
			var finished ProgressEvent
			state, err := New(checksRegistry(t)).Run(t.Context(), steptest.Definition(t, "ordered", group), checkOptions(t, &finished))
			if err != nil {
				t.Fatal(err)
			}
			if len(state.Checks) != 1 || state.Checks[0].Status != "ok" {
				t.Fatalf("checks = %#v", state.Checks)
			}
		})
	}
}

func TestChecksDiscardFailedGroupWrites(t *testing.T) {
	t.Parallel()
	registry := checksRegistry(t)
	if err := registry.Register("fail", func(map[string]any) (step.Runner, error) {
		return steptest.RunnerFunc(func(context.Context, step.Request) (step.Result, error) { return step.Result{}, errors.New("failure") }), nil
	}); err != nil {
		t.Fatal(err)
	}
	group := workflow.Step{Concurrent: &workflow.ConcurrentGroup{MaxConcurrency: 2, Steps: []workflow.Step{findingStep("discarded", "discarded", "false", "fail"), {ID: "fail", Type: "fail"}}}}
	definition := steptest.Definition(t, "rollback", group)
	var finished ProgressEvent
	_, err := New(registry).Run(t.Context(), definition, checkOptions(t, &finished))
	if err == nil || len(finished.Checks) != 0 {
		t.Fatalf("error = %v, checks = %#v", err, finished.Checks)
	}
}

func TestChecksDryRunDoesNotProduceFindings(t *testing.T) {
	t.Parallel()
	definition := steptest.Definition(t, "dry", findingStep("first", "tool", "false", "fail"))
	state, err := New(checksRegistry(t)).Run(t.Context(), definition, Options{DryRun: true, Stdout: io.Discard})
	if err != nil || len(state.Checks) != 0 {
		t.Fatalf("state = %#v, error = %v", state, err)
	}
}

func TestChecksRedactReportedFindings(t *testing.T) {
	t.Parallel()
	registry := checksRegistry(t)
	if err := registry.Register("sensitive", func(map[string]any) (step.Runner, error) {
		return steptest.RunnerFunc(func(context.Context, step.Request) (step.Result, error) {
			return step.Result{Sensitive: []string{"private-value"}}, nil
		}), nil
	}); err != nil {
		t.Fatal(err)
	}
	definition := steptest.Definition(t, "redacted", workflow.Step{ID: "secret", Type: "sensitive"}, findingStep("first", "private-value", "false", "fail"))
	var finished ProgressEvent
	_, err := New(registry).Run(t.Context(), definition, checkOptions(t, &finished))
	if err == nil || len(finished.Checks) != 1 || strings.Contains(finished.Checks[0].ID+finished.Checks[0].Message+finished.Checks[0].Fix, "private-value") {
		t.Fatalf("error = %v, checks = %#v", err, finished.Checks)
	}
}

func TestChecksOnlyWinningAttemptPublishesFindings(t *testing.T) {
	t.Parallel()
	registry := checksRegistry(t)
	if err := registry.Register("attempt_number", func(map[string]any) (step.Runner, error) {
		return steptest.RunnerFunc(func(_ context.Context, request step.Request) (step.Result, error) {
			return step.Result{Variables: map[string]any{"pass": request.Attempt}}, nil
		}), nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register("fail_first", func(map[string]any) (step.Runner, error) {
		return steptest.RunnerFunc(func(_ context.Context, request step.Request) (step.Result, error) {
			if request.Attempt == 1 {
				return step.Result{}, errors.New("retry")
			}
			return step.Result{}, nil
		}), nil
	}); err != nil {
		t.Fatal(err)
	}
	control := immediateRetry(2)
	control.Steps = []workflow.Step{{ID: "number", Type: "attempt_number"}, findingStep("observe", "pass-{{ .vars.pass }}", "true", "fail"), {ID: "failure", Type: "fail_first"}}
	definition := steptest.Definition(t, "attempts", workflow.Step{ID: "attempt", Attempt: &control})
	definition.Vars = map[string]any{"pass": 0}
	var finished ProgressEvent
	state, err := New(registry).Run(t.Context(), definition, checkOptions(t, &finished))
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Checks) != 1 || state.Checks[0].ID != "pass-2" {
		t.Fatalf("checks = %#v", state.Checks)
	}
}

func TestChecksInlineCallbackReadsParentWithoutFinalizingItsFindings(t *testing.T) {
	t.Parallel()
	registry := checksRegistry(t)
	definition := steptest.Definition(t, "parent")
	state, err := initialState(definition, Options{})
	if err != nil {
		t.Fatal(err)
	}
	state.Checks = []step.CheckFinding{{ID: "parent", Status: "fail", Message: "pending"}}
	runner := boundInlineWorkflowRunner{engine: New(registry), definition: definition, state: state}
	_, err = runner.RunInlineWorkflow(t.Context(), step.InlineWorkflowCall{Name: "observe", Steps: []workflow.Step{{ID: "observe", Type: "assert", With: map[string]any{"expr": `len(checks) == 1 && checks[0].status == "fail"`, "message": "parent findings missing"}}}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.RunInlineWorkflow(t.Context(), step.InlineWorkflowCall{Name: "check", Steps: []workflow.Step{findingStep("local", "local", "false", "fail")}})
	if err == nil || len(state.Checks) != 1 {
		t.Fatalf("error = %v, parent checks = %#v", err, state.Checks)
	}
}

func TestChecksOnceReplaysFindings(t *testing.T) {
	t.Parallel()
	definition := steptest.Definition(t, "once", workflow.Step{ID: "once", Once: &workflow.OnceGroup{Key: "probe", Scope: "local", OnBusy: "error", Steps: []workflow.Step{findingStep("probe", "cached", "false", "warn")}}})
	var finished ProgressEvent
	options := checkOptions(t, &finished)
	options.LocalValueDir = t.TempDir()
	engine := New(checksRegistry(t))
	for range 2 {
		state, err := engine.Run(t.Context(), definition, options)
		if err != nil {
			t.Fatal(err)
		}
		if len(state.Checks) != 1 || state.Checks[0].Status != "warn" {
			t.Fatalf("checks = %#v", state.Checks)
		}
	}
}

func TestChecksAcceptTypedListAndEntryBindings(t *testing.T) {
	t.Parallel()
	for _, wholeList := range []bool{false, true} {
		t.Run(map[bool]string{false: "entry", true: "list"}[wholeList], func(t *testing.T) {
			t.Parallel()
			entry := map[string]any{"id": "tool", "expr": "true", "message": "missing"}
			var value any = []any{map[string]any{"expr": "vars.entry"}}
			if wholeList {
				value = map[string]any{"expr": "vars.entries"}
			}
			definition := steptest.Definition(t, "bindings", workflow.Step{ID: "check", Type: "check", With: map[string]any{"checks": value}})
			definition.Vars = map[string]any{"entry": entry, "entries": []any{entry}}
			var finished ProgressEvent
			state, err := New(checksRegistry(t)).Run(t.Context(), definition, checkOptions(t, &finished))
			if err != nil {
				t.Fatal(err)
			}
			if len(state.Checks) != 1 || state.Checks[0].ID != "tool" || state.Checks[0].Status != "ok" {
				t.Fatalf("checks = %#v", state.Checks)
			}
		})
	}
}

func TestChecksLiteralExpressionCanContainTemplateDelimiters(t *testing.T) {
	t.Parallel()
	declaration := findingStep("check", "template", "true", "fail")
	entry := declaration.With["checks"].([]any)[0].(map[string]any)
	entry["expr"] = map[string]any{"literal": `len("{{") == 2`}
	var finished ProgressEvent
	state, err := New(checksRegistry(t)).Run(t.Context(), steptest.Definition(t, "literal", declaration), checkOptions(t, &finished))
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Checks) != 1 || state.Checks[0].Status != "ok" {
		t.Fatalf("checks = %#v", state.Checks)
	}
}

func TestChecksPendingFailureDoesNotFailNestedCleanup(t *testing.T) {
	t.Parallel()
	succeeded := map[string]any{"expr": `finally.status == "succeeded"`, "message": "nested cleanup saw a pending check failure"}
	definition := steptest.Definition(t, "doctor", findingStep("first", "tool", "false", "fail"), tryCatchStep(
		[]workflow.Step{{ID: "prepare", Type: "set", With: map[string]any{"variables": map[string]any{"ready": true}}, Defer: []workflow.Step{{ID: "release", Type: "assert", With: succeeded}}}},
		[]workflow.Step{{ID: "recover", Type: "set", With: map[string]any{"variables": map[string]any{"recovered": true}}}},
	))
	var finished ProgressEvent
	_, err := New(checksRegistry(t)).Run(t.Context(), definition, checkOptions(t, &finished))
	if err == nil || !strings.Contains(err.Error(), "1 unresolved check(s) failed") || strings.Contains(err.Error(), "nested cleanup") {
		t.Fatalf("error = %v", err)
	}
}
