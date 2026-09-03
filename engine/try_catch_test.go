package engine

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/workflow"
)

func TestTryCatchSuccessSkipsCatchAndPublishesNamedOutcome(t *testing.T) {
	var catchRuns int
	registry := newTestRegistry(t, map[string]step.Builder{
		"write": func(raw map[string]any) (step.Runner, error) {
			return runnerFunc(func(context.Context, step.Request) (step.Result, error) {
				return step.Result{Outputs: map[string]any{"value": raw["value"]}, Variables: map[string]any{"artifact": raw["value"]}}, nil
			}), nil
		},
		"catch": func(map[string]any) (step.Runner, error) {
			return runnerFunc(func(context.Context, step.Request) (step.Result, error) {
				catchRuns++
				return step.Result{}, nil
			}), nil
		},
	})
	definition := testDefinition(t, "success",
		tryCatchStep([]workflow.Step{{ID: "deploy", Type: "write", With: map[string]any{"value": "dist/app"}}}, []workflow.Step{{ID: "rollback", Type: "catch", With: map[string]any{}}}),
		workflow.Step{ID: "consume", Type: "write", With: map[string]any{"value": "{{ .steps.deployment.try.steps.deploy.outputs.value }}"}},
	)
	state, err := New(registry).Run(t.Context(), definition, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if catchRuns != 0 {
		t.Fatalf("catch runs = %d", catchRuns)
	}
	output := state.Steps["deployment"].(map[string]any)
	if output["ok"] != true || output["recovered"] != false || output["status"] != "succeeded" || output["error"] != nil {
		t.Fatalf("outcome = %#v", output)
	}
	try := output["try"].(map[string]any)
	catch := output["catch"].(map[string]any)
	if try["status"] != "succeeded" || catch["status"] != "skipped" {
		t.Fatalf("phases = try %#v, catch %#v", try, catch)
	}
	if got := state.Steps["consume"].(map[string]any)["value"]; got != "dist/app" {
		t.Fatalf("consumer = %#v", got)
	}
	if _, exists := state.Steps["deploy"]; exists {
		t.Fatalf("try child leaked into outer steps: %#v", state.Steps)
	}
}

func TestTryCatchRecoversAndExposesStructuredError(t *testing.T) {
	registry := newTestRegistry(t, map[string]step.Builder{
		"fail": func(map[string]any) (step.Runner, error) {
			return runnerFunc(func(context.Context, step.Request) (step.Result, error) {
				return step.Result{}, errors.New("release rejected")
			}), nil
		},
		"capture": func(raw map[string]any) (step.Runner, error) {
			return runnerFunc(func(_ context.Context, request step.Request) (step.Result, error) {
				return step.Result{Outputs: map[string]any{"value": raw["value"], "error": request.Bindings["error"]}, Variables: map[string]any{"recovery": raw["value"]}}, nil
			}), nil
		},
	})
	definition := testDefinition(t, "recovery",
		tryCatchStep(
			[]workflow.Step{{ID: "deploy", Type: "fail", With: map[string]any{}}},
			[]workflow.Step{
				{ID: "rollback", Type: "capture", If: `error.step == "deploy" && error.status == "failed"`, With: map[string]any{"value": "{{ .error.step }}: {{ .error.message }}"}},
				{ID: "report", Type: "capture", With: map[string]any{"value": "reported"}},
			},
		),
		workflow.Step{ID: "after", Type: "capture", With: map[string]any{"value": "continued"}},
	)
	state, err := New(registry).Run(t.Context(), definition, Options{})
	if err != nil {
		t.Fatal(err)
	}
	output := state.Steps["deployment"].(map[string]any)
	if output["recovered"] != true || output["status"] != "succeeded" || output["error"] != nil {
		t.Fatalf("outcome = %#v", output)
	}
	try := output["try"].(map[string]any)
	errorValue := try["error"].(map[string]any)
	if errorValue["step"] != "deploy" || errorValue["status"] != "failed" || !strings.Contains(errorValue["message"].(string), "release rejected") {
		t.Fatalf("error = %#v", errorValue)
	}
	rollback := output["catch"].(map[string]any)["steps"].(map[string]any)["rollback"].(map[string]any)["outputs"].(map[string]any)
	if !strings.Contains(rollback["value"].(string), "deploy:") || !reflect.DeepEqual(rollback["error"], errorValue) {
		t.Fatalf("rollback = %#v", rollback)
	}
	if state.Steps["after"].(map[string]any)["value"] != "continued" || output["vars"].(map[string]any)["recovery"] != "reported" {
		t.Fatalf("ordinary execution did not continue: steps=%#v vars=%#v", state.Steps, state.Vars)
	}
}

func TestTryCatchCatchIsBestEffortButPublishesNothingOnFailure(t *testing.T) {
	var mu sync.Mutex
	var ran []string
	var publishedStep bool
	var publishedVariable bool
	registry := newTestRegistry(t, map[string]step.Builder{
		"fail": func(raw map[string]any) (step.Runner, error) {
			return runnerFunc(func(context.Context, step.Request) (step.Result, error) {
				mu.Lock()
				ran = append(ran, raw["name"].(string))
				mu.Unlock()
				return step.Result{}, errors.New(raw["name"].(string))
			}), nil
		},
		"record": func(raw map[string]any) (step.Runner, error) {
			return runnerFunc(func(context.Context, step.Request) (step.Result, error) {
				mu.Lock()
				ran = append(ran, raw["name"].(string))
				mu.Unlock()
				return step.Result{Variables: map[string]any{"leaked": true}}, nil
			}), nil
		},
		"inspect": func(map[string]any) (step.Runner, error) {
			return runnerFunc(func(_ context.Context, request step.Request) (step.Result, error) {
				_, publishedStep = request.Steps["deployment"]
				_, publishedVariable = request.Vars["leaked"]
				return step.Result{}, nil
			}), nil
		},
	})
	definition := testDefinition(t, "failed-recovery", tryCatchStep(
		[]workflow.Step{{ID: "deploy", Type: "fail", With: map[string]any{"name": "deploy"}}},
		[]workflow.Step{{ID: "rollback", Type: "fail", With: map[string]any{"name": "rollback"}}, {ID: "report", Type: "record", With: map[string]any{"name": "report"}}},
	))
	definition.Finally = []workflow.Step{{ID: "inspect", Type: "inspect", With: map[string]any{}}}
	_, err := New(registry).Run(t.Context(), definition, Options{})
	if err == nil || !strings.Contains(err.Error(), "rollback") {
		t.Fatalf("error = %v", err)
	}
	if !reflect.DeepEqual(ran, []string{"deploy", "rollback", "report"}) {
		t.Fatalf("runs = %#v", ran)
	}
	if publishedStep || publishedVariable {
		t.Fatalf("failed control published private state: step=%v variable=%v", publishedStep, publishedVariable)
	}
}

func TestTryCatchRecoversChildTimeout(t *testing.T) {
	registry := newTestRegistry(t, map[string]step.Builder{
		"block": func(map[string]any) (step.Runner, error) {
			return runnerFunc(func(ctx context.Context, _ step.Request) (step.Result, error) {
				<-ctx.Done()
				return step.Result{}, ctx.Err()
			}), nil
		},
		"capture": func(map[string]any) (step.Runner, error) {
			return runnerFunc(func(_ context.Context, request step.Request) (step.Result, error) {
				return step.Result{Outputs: map[string]any{"status": request.Bindings["error"].(map[string]any)["status"]}}, nil
			}), nil
		},
	})
	timeout := workflow.Duration(10 * time.Millisecond)
	definition := testDefinition(t, "timeout", tryCatchStep(
		[]workflow.Step{attemptStep("deploy", attemptTimeout(workflow.AttemptControl{}, &timeout), workflow.Step{Type: "block", With: map[string]any{}})},
		[]workflow.Step{{ID: "rollback", Type: "capture", With: map[string]any{}}},
	))
	state, err := New(registry).Run(t.Context(), definition, Options{})
	if err != nil {
		t.Fatal(err)
	}
	output := state.Steps["deployment"].(map[string]any)
	if output["recovered"] != true || output["try"].(map[string]any)["status"] != "timed_out" {
		t.Fatalf("outcome = %#v", output)
	}
	rollback := output["catch"].(map[string]any)["steps"].(map[string]any)["rollback"].(map[string]any)["outputs"].(map[string]any)
	if rollback["status"] != "timed_out" {
		t.Fatalf("rollback = %#v", rollback)
	}
}

func TestTryCatchRunsLocalDeferAfterRecoveryBeforePublishing(t *testing.T) {
	var cleanupSawError bool
	var cleanupFinally map[string]any
	registry := newTestRegistry(t, map[string]step.Builder{
		"prepare": func(map[string]any) (step.Runner, error) {
			return runnerFunc(func(context.Context, step.Request) (step.Result, error) { return step.Result{}, nil }), nil
		},
		"fail": func(map[string]any) (step.Runner, error) {
			return runnerFunc(func(context.Context, step.Request) (step.Result, error) {
				return step.Result{}, errors.New("deploy failed")
			}), nil
		},
		"recover": func(map[string]any) (step.Runner, error) {
			return runnerFunc(func(context.Context, step.Request) (step.Result, error) { return step.Result{}, nil }), nil
		},
		"cleanup": func(map[string]any) (step.Runner, error) {
			return runnerFunc(func(_ context.Context, request step.Request) (step.Result, error) {
				_, cleanupSawError = request.Bindings["error"]
				cleanupFinally = request.Bindings["finally"].(map[string]any)
				return step.Result{Outputs: map[string]any{"done": true}, Variables: map[string]any{"cleaned": true}}, nil
			}), nil
		},
	})
	definition := testDefinition(t, "cleanup", tryCatchStep(
		[]workflow.Step{
			{ID: "prepare", Type: "prepare", With: map[string]any{}, Defer: []workflow.Step{{ID: "release", Type: "cleanup", With: map[string]any{}}}},
			{ID: "deploy", Type: "fail", With: map[string]any{}},
		},
		[]workflow.Step{{ID: "rollback", Type: "recover", With: map[string]any{}}},
	))
	state, err := New(registry).Run(t.Context(), definition, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if cleanupSawError || cleanupFinally["status"] != "succeeded" || len(cleanupFinally["errors"].([]any)) != 0 {
		t.Fatalf("cleanup bindings: error=%v finally=%#v", cleanupSawError, cleanupFinally)
	}
	output := state.Steps["deployment"].(map[string]any)
	cleanup := output["cleanup"].(map[string]any)
	if cleanup["status"] != "succeeded" || cleanup["steps"].(map[string]any)["release"].(map[string]any)["status"] != "succeeded" || state.Vars["cleaned"] != true {
		t.Fatalf("cleanup outcome = %#v, vars = %#v", cleanup, state.Vars)
	}
}

func TestTryCatchCancellationBypassesCatchAndRunsLocalDefer(t *testing.T) {
	started := make(chan struct{})
	cleaned := make(chan struct{})
	var catchRuns int
	registry := newTestRegistry(t, map[string]step.Builder{
		"prepare": func(map[string]any) (step.Runner, error) {
			return runnerFunc(func(context.Context, step.Request) (step.Result, error) { return step.Result{}, nil }), nil
		},
		"block": func(map[string]any) (step.Runner, error) {
			return runnerFunc(func(ctx context.Context, _ step.Request) (step.Result, error) {
				close(started)
				<-ctx.Done()
				return step.Result{}, ctx.Err()
			}), nil
		},
		"catch": func(map[string]any) (step.Runner, error) {
			return runnerFunc(func(context.Context, step.Request) (step.Result, error) { catchRuns++; return step.Result{}, nil }), nil
		},
		"cleanup": func(map[string]any) (step.Runner, error) {
			return runnerFunc(func(ctx context.Context, _ step.Request) (step.Result, error) {
				if ctx.Err() != nil {
					t.Fatalf("cleanup context remained canceled: %v", ctx.Err())
				}
				close(cleaned)
				return step.Result{}, nil
			}), nil
		},
	})
	control := tryCatchStep(
		[]workflow.Step{
			{ID: "prepare", Type: "prepare", With: map[string]any{}, Defer: []workflow.Step{{ID: "cleanup", Type: "cleanup", With: map[string]any{}}}},
			{ID: "deploy", Type: "block", With: map[string]any{}},
		},
		[]workflow.Step{{ID: "rollback", Type: "catch", With: map[string]any{}}},
	)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := New(registry).Run(ctx, testDefinition(t, "canceled", control), Options{})
		done <- err
	}()
	<-started
	cancel()
	err := <-done
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
	if catchRuns != 0 {
		t.Fatalf("catch runs = %d", catchRuns)
	}
	select {
	case <-cleaned:
	default:
		t.Fatal("registered local defer did not run")
	}
}

func TestTryCatchDryRunShowsBothPhases(t *testing.T) {
	registry := newTestRegistry(t, map[string]step.Builder{
		"run": func(map[string]any) (step.Runner, error) { return countingRunner{}, nil },
	})
	definition := testDefinition(t, "dry-run", tryCatchStep(
		[]workflow.Step{{ID: "deploy", Type: "run", With: map[string]any{}}},
		[]workflow.Step{{ID: "rollback", Type: "run", With: map[string]any{}}},
	))
	definition.Steps[0].If = "vars.enabled"
	definition.Vars = map[string]any{"enabled": false}
	var output bytes.Buffer
	if _, err := New(registry).Run(t.Context(), definition, Options{DryRun: true, Stdout: &output}); err != nil {
		t.Fatal(err)
	}
	want := "1. deployment (try) if: vars.enabled\n   try:\n      1.1 deploy (run)\n   catch:\n      1.1 rollback (run)\n"
	if output.String() != want {
		t.Fatalf("output = %q, want %q", output.String(), want)
	}
}

func tryCatchStep(primary, rescue []workflow.Step) workflow.Step {
	return workflow.Step{ID: "deployment", Try: &workflow.TryBlock{Steps: primary}, Catch: &workflow.CatchBlock{Steps: rescue}}
}

func TestTryCatchSkippedCatchDoesNotRecover(t *testing.T) {
	var catchRuns int
	registry := newTestRegistry(t, map[string]step.Builder{
		"fail": func(map[string]any) (step.Runner, error) {
			return runnerFunc(func(context.Context, step.Request) (step.Result, error) {
				return step.Result{}, errors.New("release rejected")
			}), nil
		},
		"catch": func(map[string]any) (step.Runner, error) {
			return runnerFunc(func(context.Context, step.Request) (step.Result, error) {
				catchRuns++
				return step.Result{}, nil
			}), nil
		},
	})
	definition := testDefinition(t, "unmatched-recovery", tryCatchStep(
		[]workflow.Step{{ID: "deploy", Type: "fail", With: map[string]any{}}},
		[]workflow.Step{{ID: "rollback", Type: "catch", If: `error.status == "timed_out"`, With: map[string]any{}}},
	))
	_, err := New(registry).Run(t.Context(), definition, Options{})
	if err == nil || !strings.Contains(err.Error(), "release rejected") {
		t.Fatalf("error = %v", err)
	}
	if catchRuns != 0 {
		t.Fatalf("catch runs = %d", catchRuns)
	}
}

func TestTryCatchExecutedCatchStepIsNotReportedSkipped(t *testing.T) {
	registry := newTestRegistry(t, map[string]step.Builder{
		"fail": func(map[string]any) (step.Runner, error) {
			return runnerFunc(func(context.Context, step.Request) (step.Result, error) {
				return step.Result{}, errors.New("release rejected")
			}), nil
		},
		"catch": func(map[string]any) (step.Runner, error) {
			return runnerFunc(func(context.Context, step.Request) (step.Result, error) {
				return step.Result{}, nil
			}), nil
		},
	})
	definition := testDefinition(t, "recovery-progress", tryCatchStep(
		[]workflow.Step{{ID: "deploy", Type: "fail", With: map[string]any{}}},
		[]workflow.Step{{ID: "rollback", Type: "catch", With: map[string]any{}}},
	))
	var statuses []ExecutionStatus
	if _, err := New(registry).Run(t.Context(), definition, Options{Progress: func(event ProgressEvent) {
		if event.Kind == StepFinished && event.StepID == "rollback" {
			statuses = append(statuses, event.Status)
		}
	}}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(statuses, []ExecutionStatus{StatusSucceeded}) {
		t.Fatalf("rollback progress statuses = %#v", statuses)
	}
}

func TestTryCatchValidateRejectsInvalidCondition(t *testing.T) {
	registry := newTestRegistry(t, map[string]step.Builder{
		"noop": func(map[string]any) (step.Runner, error) {
			return runnerFunc(func(context.Context, step.Request) (step.Result, error) { return step.Result{}, nil }), nil
		},
	})
	control := tryCatchStep(
		[]workflow.Step{{ID: "deploy", Type: "noop", With: map[string]any{}}},
		[]workflow.Step{{ID: "rollback", Type: "noop", With: map[string]any{}}},
	)
	control.If = "vars.enabled ==="
	definition := testDefinition(t, "invalid-condition", control)
	err := New(registry).Validate(t.Context(), definition, Options{})
	if err == nil || !strings.Contains(err.Error(), `step "deployment": if:`) {
		t.Fatalf("Validate() error = %v", err)
	}
}
