package engine

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/workflow"
)

func TestCancelOnMonitorWinsAndRecordsPartialBodyState(t *testing.T) {
	prepared := make(chan struct{})
	registry := newTestRegistry(t, map[string]step.Builder{
		"prepare": func(map[string]any) (step.Runner, error) {
			return runnerFunc(func(context.Context, step.Request) (step.Result, error) {
				close(prepared)
				return step.Result{Outputs: map[string]any{}, Variables: map[string]any{"artifact": "dist/app.tar"}}, nil
			}), nil
		},
		"block": func(map[string]any) (step.Runner, error) {
			return runnerFunc(func(ctx context.Context, _ step.Request) (step.Result, error) {
				<-ctx.Done()
				return step.Result{}, ctx.Err()
			}), nil
		},
		"monitor": func(map[string]any) (step.Runner, error) {
			return runnerFunc(func(context.Context, step.Request) (step.Result, error) {
				<-prepared
				return step.Result{Outputs: map[string]any{"matched": true}}, nil
			}), nil
		},
		"capture": func(raw map[string]any) (step.Runner, error) {
			return countingRunner{value: raw["value"]}, nil
		},
	})
	definition := cancelOnDefinition(
		t,
		[]workflow.Step{{ID: "deployment_finished", Type: "monitor", With: map[string]any{}}},
		[]workflow.Step{{ID: "prepare", Type: "prepare", With: map[string]any{}}, {ID: "deploy", Type: "block", With: map[string]any{}}},
		`{"deploy": steps.deploy.status, "artifact": vars.artifact, "monitor": cancel_on.winner.monitor, "monitor_status": monitors.deployment_finished.status}`,
	)
	definition.Steps = append(definition.Steps,
		workflow.Step{ID: "parent_succeeded", Type: "capture", If: `steps.deployment_watch.status == "succeeded"`, With: map[string]any{"value": true}},
		workflow.Step{ID: "deploy_succeeded", Type: "capture", If: `steps.deployment_watch.steps.deploy.status == "succeeded"`, With: map[string]any{"value": true}},
		workflow.Step{ID: "monitor_succeeded", Type: "capture", If: `steps.deployment_watch.monitors.deployment_finished.status == "succeeded"`, With: map[string]any{"value": true}},
		workflow.Step{ID: "artifact", Type: "capture", With: map[string]any{"value": `{{ .steps.deployment_watch.vars.artifact }}`}},
	)
	state, err := New(registry).Run(t.Context(), definition, Options{})
	if err != nil {
		t.Fatal(err)
	}
	output := state.Steps["deployment_watch"].(map[string]any)
	if output["ok"] != true || output["triggered"] != true || output["status"] != "succeeded" {
		t.Fatalf("outcome = %#v", output)
	}
	winner := output["winner"].(map[string]any)
	if winner["monitor"] != "deployment_finished" || winner["kind"] != "monitor" {
		t.Fatalf("winner = %#v", winner)
	}
	bodySteps := output["steps"].(map[string]any)
	if bodySteps["prepare"].(map[string]any)["status"] != "succeeded" || bodySteps["deploy"].(map[string]any)["status"] != "canceled" {
		t.Fatalf("body steps = %#v", bodySteps)
	}
	if bodySteps["deploy"].(map[string]any)["error"] != nil || bodySteps["deploy"].(map[string]any)["outputs"] != nil {
		t.Fatalf("canceled deploy = %#v", bodySteps["deploy"])
	}
	if output["vars"].(map[string]any)["artifact"] != "dist/app.tar" {
		t.Fatalf("vars = %#v", output["vars"])
	}
	result := output["result"].(map[string]any)
	if result["deploy"] != "canceled" || result["artifact"] != "dist/app.tar" || result["monitor"] != "deployment_finished" || result["monitor_status"] != "succeeded" {
		t.Fatalf("collection = %#v", result)
	}
	monitor := output["monitors"].(map[string]any)["deployment_finished"].(map[string]any)
	if monitor["status"] != "succeeded" || monitor["outputs"].(map[string]any)["matched"] != true {
		t.Fatalf("monitor = %#v", monitor)
	}
	if len(monitor["steps"].(map[string]any)) != 0 {
		t.Fatalf("direct monitor nested steps = %#v", monitor["steps"])
	}
	if _, exists := state.Vars["artifact"]; exists {
		t.Fatalf("body variable leaked into outer state: %#v", state.Vars)
	}
	if state.Steps["parent_succeeded"].(map[string]any)["value"] != true || state.Steps["monitor_succeeded"].(map[string]any)["value"] != true || state.Steps["artifact"].(map[string]any)["value"] != "dist/app.tar" {
		t.Fatalf("documented access paths = %#v", state.Steps)
	}
	if _, exists := state.Steps["deploy_succeeded"]; exists {
		t.Fatalf("deploy success condition unexpectedly ran: %#v", state.Steps["deploy_succeeded"])
	}
	if state.Stats.Total != 5 || state.Stats.Succeeded != 4 || state.Stats.Skipped != 1 {
		t.Fatalf("stats = %#v", state.Stats)
	}
}

func TestCancelOnBodyWinsAndCanceledMonitorHasNullError(t *testing.T) {
	monitorStarted := make(chan struct{})
	registry := newTestRegistry(t, map[string]step.Builder{
		"body": func(map[string]any) (step.Runner, error) {
			return runnerFunc(func(context.Context, step.Request) (step.Result, error) {
				<-monitorStarted
				return step.Result{Outputs: map[string]any{"stdout": "deployed\n"}}, nil
			}), nil
		},
		"monitor": func(map[string]any) (step.Runner, error) {
			return runnerFunc(func(ctx context.Context, _ step.Request) (step.Result, error) {
				close(monitorStarted)
				<-ctx.Done()
				return step.Result{}, ctx.Err()
			}), nil
		},
	})
	definition := cancelOnDefinition(t, []workflow.Step{{ID: "deployment_finished", Type: "monitor", With: map[string]any{}}}, []workflow.Step{{ID: "deploy", Type: "body", With: map[string]any{}}}, "")
	state, err := New(registry).Run(t.Context(), definition, Options{})
	if err != nil {
		t.Fatal(err)
	}
	output := state.Steps["deployment_watch"].(map[string]any)
	winner := output["winner"].(map[string]any)
	if output["ok"] != true || output["triggered"] != false || output["status"] != "succeeded" || output["error"] != nil || winner["monitor"] != "" || winner["kind"] != "body" {
		t.Fatalf("outcome = %#v", output)
	}
	deploy := output["steps"].(map[string]any)["deploy"].(map[string]any)
	if !reflect.DeepEqual(deploy["outputs"], map[string]any{"stdout": "deployed\n"}) {
		t.Fatalf("deploy = %#v", deploy)
	}
	monitor := output["monitors"].(map[string]any)["deployment_finished"].(map[string]any)
	if monitor["status"] != "canceled" || monitor["error"] != nil || monitor["outputs"] != nil {
		t.Fatalf("monitor = %#v", monitor)
	}
}

func TestCancelOnMonitorWinsAfterSkippedBody(t *testing.T) {
	registry := newTestRegistry(t, map[string]step.Builder{
		"done": func(map[string]any) (step.Runner, error) {
			return runnerFunc(func(context.Context, step.Request) (step.Result, error) {
				return step.Result{Outputs: map[string]any{"ready": true}}, nil
			}), nil
		},
		"never": func(map[string]any) (step.Runner, error) {
			return runnerFunc(func(context.Context, step.Request) (step.Result, error) {
				return step.Result{}, errors.New("skipped body step ran")
			}), nil
		},
	})
	definition := cancelOnDefinition(t,
		[]workflow.Step{{ID: "ready", Type: "done", With: map[string]any{}}},
		[]workflow.Step{{If: "false", Steps: []workflow.Step{{ID: "prepare", Type: "never", With: map[string]any{}}, {ID: "deploy", Type: "never", With: map[string]any{}}}}},
		"",
	)
	state, err := New(registry).Run(t.Context(), definition, Options{})
	if err != nil {
		t.Fatal(err)
	}
	steps := state.Steps["deployment_watch"].(map[string]any)["steps"].(map[string]any)
	for _, id := range []string{"prepare", "deploy"} {
		record := steps[id].(map[string]any)
		if record["status"] != "skipped" || record["error"] != nil || record["outputs"] != nil {
			t.Fatalf("%s = %#v", id, record)
		}
	}
}

func TestCancelOnSkippedBodyEndsTheRace(t *testing.T) {
	registry := newTestRegistry(t, map[string]step.Builder{
		"block": func(map[string]any) (step.Runner, error) {
			return runnerFunc(func(ctx context.Context, _ step.Request) (step.Result, error) {
				<-ctx.Done()
				return step.Result{}, ctx.Err()
			}), nil
		},
		"never": func(map[string]any) (step.Runner, error) {
			return runnerFunc(func(context.Context, step.Request) (step.Result, error) {
				return step.Result{}, errors.New("skipped body step ran")
			}), nil
		},
	})
	definition := cancelOnDefinition(t,
		[]workflow.Step{{ID: "deadline", Type: "block", With: map[string]any{}}},
		[]workflow.Step{{If: "false", Steps: []workflow.Step{{ID: "deploy", Type: "never", With: map[string]any{}}}}},
		"",
	)
	type run struct {
		state *State
		err   error
	}
	done := make(chan run, 1)
	go func() {
		state, err := New(registry).Run(context.Background(), definition, Options{})
		done <- run{state: state, err: err}
	}()

	select {
	case result := <-done:
		if result.err != nil {
			t.Fatal(result.err)
		}
		output := result.state.Steps["deployment_watch"].(map[string]any)
		if output["ok"] != true || output["triggered"] != false || output["status"] != "succeeded" {
			t.Fatalf("outcome = %#v", output)
		}
		if winner := output["winner"].(map[string]any); winner["monitor"] != "" || winner["kind"] != "body" {
			t.Fatalf("winner = %#v", winner)
		}
		deadline := output["monitors"].(map[string]any)["deadline"].(map[string]any)
		if deadline["status"] != "canceled" || deadline["error"] != nil {
			t.Fatalf("deadline = %#v", deadline)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("cancel_on did not cancel its monitors after the body was skipped")
	}
}

func TestCancelOnSkippedByConditionCommitsNothing(t *testing.T) {
	registry := newTestRegistry(t, map[string]step.Builder{
		"never": func(map[string]any) (step.Runner, error) {
			return runnerFunc(func(context.Context, step.Request) (step.Result, error) {
				return step.Result{}, errors.New("skipped cancel_on ran")
			}), nil
		},
		"capture": func(raw map[string]any) (step.Runner, error) {
			return countingRunner{value: raw["value"]}, nil
		},
	})
	definition := cancelOnDefinition(t,
		[]workflow.Step{{ID: "deadline", Type: "never", With: map[string]any{}}},
		[]workflow.Step{{ID: "deploy", Type: "never", With: map[string]any{}}},
		"",
	)
	definition.Steps[0].If = "false"
	definition.Steps = append(definition.Steps,
		workflow.Step{ID: "after", Type: "capture", With: map[string]any{"value": true}},
	)
	state, err := New(registry).Run(t.Context(), definition, Options{})
	if err != nil {
		t.Fatal(err)
	}
	// A skipped control commits nothing, so later conditions see an absent step
	// rather than an empty record, exactly as for every other skipped step.
	if record, exists := state.Steps["deployment_watch"]; exists {
		t.Fatalf("skipped cancel_on committed %#v", record)
	}
	if state.Steps["after"] == nil {
		t.Fatalf("steps = %#v", state.Steps)
	}
}

func TestCancelOnMultipleMonitorsAndAnonymousMonitorChildren(t *testing.T) {
	registry := newTestRegistry(t, map[string]step.Builder{
		"done": func(map[string]any) (step.Runner, error) {
			return runnerFunc(func(_ context.Context, request step.Request) (step.Result, error) {
				return step.Result{Outputs: map[string]any{"id": request.StepID}}, nil
			}), nil
		},
		"block": func(map[string]any) (step.Runner, error) {
			return runnerFunc(func(ctx context.Context, _ step.Request) (step.Result, error) {
				<-ctx.Done()
				return step.Result{}, ctx.Err()
			}), nil
		},
	})
	definition := cancelOnDefinition(t,
		[]workflow.Step{
			{ID: "service_checks", Concurrent: &workflow.ConcurrentGroup{MaxConcurrency: 2, Steps: []workflow.Step{{ID: "api", Type: "done", With: map[string]any{}}, {ID: "worker", Type: "done", With: map[string]any{}}}}},
			{ID: "deadline", Type: "block", With: map[string]any{}},
		},
		[]workflow.Step{{ID: "deploy", Type: "block", With: map[string]any{}}},
		"",
	)
	state, err := New(registry).Run(t.Context(), definition, Options{})
	if err != nil {
		t.Fatal(err)
	}
	output := state.Steps["deployment_watch"].(map[string]any)
	winner := output["winner"].(map[string]any)
	if winner["monitor"] != "service_checks" || winner["kind"] != "concurrent" {
		t.Fatalf("winner = %#v", winner)
	}
	monitors := output["monitors"].(map[string]any)
	if len(monitors) != 2 {
		t.Fatalf("monitors = %#v", monitors)
	}
	checks := monitors["service_checks"].(map[string]any)
	if checks["status"] != "succeeded" || checks["outputs"] != nil || len(checks["steps"].(map[string]any)) != 2 {
		t.Fatalf("service checks = %#v", checks)
	}
	deadline := monitors["deadline"].(map[string]any)
	if deadline["status"] != "canceled" || deadline["error"] != nil || deadline["outputs"] != nil {
		t.Fatalf("deadline = %#v", deadline)
	}
}

func TestCancelOnWinnerStatuses(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		status string
		ok     bool
	}{
		{name: "success", status: "succeeded", ok: true},
		{name: "failure", err: errors.New("failed"), status: "failed"},
		{name: "timeout", err: context.DeadlineExceeded, status: "timed_out"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			group := &workflow.CancelOnGroup{Monitors: []workflow.Step{{ID: "monitor", Type: "check"}}, Steps: []workflow.Step{{ID: "body", Type: "work"}}}
			participants := []cancelOnParticipant{
				{kind: "body", declaration: group.Steps},
				{kind: "check", err: test.err, declaration: []workflow.Step{group.Monitors[0]}},
			}
			output := cancelOnOutputs(group, participants, 1)
			if output["status"] != test.status || output["ok"] != test.ok {
				t.Fatalf("output = %#v", output)
			}
		})
	}
}

func TestCancelOnCapturesMonitorFailure(t *testing.T) {
	registry := newTestRegistry(t, map[string]step.Builder{
		"fail": func(map[string]any) (step.Runner, error) {
			return runnerFunc(func(context.Context, step.Request) (step.Result, error) { return step.Result{}, errors.New("offline") }), nil
		},
		"block": func(map[string]any) (step.Runner, error) {
			return runnerFunc(func(ctx context.Context, _ step.Request) (step.Result, error) {
				<-ctx.Done()
				return step.Result{}, ctx.Err()
			}), nil
		},
	})
	definition := cancelOnDefinition(t, []workflow.Step{{ID: "health", Type: "fail", With: map[string]any{}}}, []workflow.Step{{ID: "deploy", Type: "block", With: map[string]any{}}}, "")
	state, err := New(registry).Run(t.Context(), definition, Options{})
	if err != nil {
		t.Fatal(err)
	}
	output := state.Steps["deployment_watch"].(map[string]any)
	if output["ok"] != false || output["status"] != "failed" || output["error"] == nil {
		t.Fatalf("outcome = %#v", output)
	}
	if state.Stats.Succeeded != 1 || state.Stats.Failed != 0 {
		t.Fatalf("stats = %#v", state.Stats)
	}
}

func TestCancelOnCollectsRecordedOutcome(t *testing.T) {
	registry := newTestRegistry(t, map[string]step.Builder{
		"done": func(map[string]any) (step.Runner, error) {
			return runnerFunc(func(context.Context, step.Request) (step.Result, error) {
				return step.Result{Outputs: map[string]any{"value": 7}}, nil
			}), nil
		},
		"block": func(map[string]any) (step.Runner, error) {
			return runnerFunc(func(ctx context.Context, _ step.Request) (step.Result, error) {
				<-ctx.Done()
				return step.Result{}, ctx.Err()
			}), nil
		},
	})
	definition := cancelOnDefinition(t, []workflow.Step{{ID: "ready", Type: "done", With: map[string]any{}}}, []workflow.Step{{ID: "deploy", Type: "block", With: map[string]any{}}}, `{"monitor": cancel_on.winner.monitor, "value": monitors.ready.outputs.value}`)
	state, err := New(registry).Run(t.Context(), definition, Options{})
	if err != nil {
		t.Fatal(err)
	}
	result := state.Steps["deployment_watch"].(map[string]any)["result"].(map[string]any)
	if result["monitor"] != "ready" || result["value"] != 7 {
		t.Fatalf("result = %#v", result)
	}
}

func TestCancelOnCollectionFailureFailsParent(t *testing.T) {
	registry := newTestRegistry(t, map[string]step.Builder{
		"done": func(map[string]any) (step.Runner, error) {
			return runnerFunc(func(context.Context, step.Request) (step.Result, error) {
				return step.Result{Outputs: map[string]any{}}, nil
			}), nil
		},
		"block": func(map[string]any) (step.Runner, error) {
			return runnerFunc(func(ctx context.Context, _ step.Request) (step.Result, error) {
				<-ctx.Done()
				return step.Result{}, ctx.Err()
			}), nil
		},
	})
	definition := cancelOnDefinition(t, []workflow.Step{{ID: "ready", Type: "done", With: map[string]any{}}}, []workflow.Step{{ID: "deploy", Type: "block", With: map[string]any{}}}, `steps.missing.value`)
	var events []ProgressEvent
	_, err := New(registry).Run(t.Context(), definition, Options{Progress: func(event ProgressEvent) { events = append(events, event) }})
	if err == nil || !strings.Contains(err.Error(), "collecting result") {
		t.Fatalf("error = %v", err)
	}
	var parentFailed bool
	for _, event := range events {
		if event.Kind == StepFinished && event.StepID == "deployment_watch" && event.Status == StatusFailed {
			parentFailed = true
		}
	}
	if !parentFailed {
		t.Fatalf("events = %#v", events)
	}
}

func cancelOnDefinition(t *testing.T, monitors, body []workflow.Step, collect string) *workflow.Definition {
	t.Helper()
	return testDefinition(t, "cancel-on", workflow.Step{
		ID: "deployment_watch", CancelOn: &workflow.CancelOnGroup{Monitors: monitors, Steps: body, Collect: collect},
	})
}
