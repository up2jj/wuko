package runworkflow

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/up2jj/wuko/step"
)

type fakeWorkflowRunner struct {
	validated []step.WorkflowCall
	run       []step.WorkflowCall
	outputs   map[string]any
	err       error
}

type cancelWorkflowRunner struct{}

func (cancelWorkflowRunner) ValidateWorkflow(context.Context, step.WorkflowCall) error { return nil }

func (cancelWorkflowRunner) RunWorkflow(ctx context.Context, _ step.WorkflowCall) (map[string]any, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func (runner *fakeWorkflowRunner) ValidateWorkflow(_ context.Context, call step.WorkflowCall) error {
	runner.validated = append(runner.validated, call)
	return runner.err
}

func (runner *fakeWorkflowRunner) RunWorkflow(_ context.Context, call step.WorkflowCall) (map[string]any, error) {
	runner.run = append(runner.run, call)
	return runner.outputs, runner.err
}

func TestRunWorkflowReturnsDirectOutputsAndResolvesVars(t *testing.T) {
	runner, err := New(map[string]any{
		"workflow": "build-artifacts",
		"target":   "linux",
		"vars": map[string]any{
			"revision": map[string]any{"expr": "steps.revision.sha"},
			"nested":   []any{map[string]any{"expr": "vars.count + 1"}},
			"literal":  map[string]any{"literal": map[string]any{"expr": "keep"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	host := &fakeWorkflowRunner{outputs: map[string]any{"artifact": "dist/app.tar.gz", "count": 3}}
	result, err := runner.Run(t.Context(), step.Request{
		Vars:      map[string]any{"count": 2},
		Steps:     map[string]any{"revision": map[string]any{"sha": "abc123"}},
		Workflows: host,
	})
	if err != nil {
		t.Fatal(err)
	}
	wantOutputs := map[string]any{"artifact": "dist/app.tar.gz", "count": 3}
	if !reflect.DeepEqual(result.Outputs, wantOutputs) {
		t.Fatalf("outputs = %#v, want %#v", result.Outputs, wantOutputs)
	}
	wantVars := map[string]any{
		"revision": "abc123",
		"nested":   []any{3},
		"literal":  map[string]any{"expr": "keep"},
	}
	if len(host.run) != 1 || host.run[0].Workflow != "build-artifacts" || host.run[0].Target != "linux" || !reflect.DeepEqual(host.run[0].Vars, wantVars) {
		t.Fatalf("calls = %#v, want vars %#v", host.run, wantVars)
	}
}

func TestRunWorkflowValidationResolvesOnlyStaticReference(t *testing.T) {
	built, err := New(map[string]any{
		"workflow": "build",
		"vars": map[string]any{
			"dynamic":       map[string]any{"expr": "steps.previous.value"},
			"revision":      "abc123",
			"partly":        map[string]any{"inner": map[string]any{"expr": "steps.previous.value"}},
			"nestedLiteral": []any{1, map[string]any{"literal": "keep"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	host := &fakeWorkflowRunner{}
	validator := built.(step.Validator)
	if err := validator.Validate(t.Context(), step.Request{Workflows: host}); err != nil {
		t.Fatal(err)
	}
	// Values known before the run reach validation so the child is checked against what the
	// call actually supplies. A value any expression contributes to is withheld, and its name
	// is reported instead so the child may still declare its use.
	wantVars := map[string]any{
		"revision":      "abc123",
		"nestedLiteral": []any{1, "keep"},
	}
	wantDeferred := []string{"dynamic", "partly"}
	if len(host.validated) != 1 || host.validated[0].Workflow != "build" {
		t.Fatalf("validation calls = %#v", host.validated)
	}
	if !reflect.DeepEqual(host.validated[0].Vars, wantVars) {
		t.Fatalf("validated vars = %#v, want %#v", host.validated[0].Vars, wantVars)
	}
	if !reflect.DeepEqual(host.validated[0].DeferredVars, wantDeferred) {
		t.Fatalf("deferred vars = %#v, want %#v", host.validated[0].DeferredVars, wantDeferred)
	}
}

func TestRunWorkflowWrapsChildFailure(t *testing.T) {
	built, err := New(map[string]any{"workflow": "build", "target": "linux"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = built.Run(t.Context(), step.Request{Workflows: &fakeWorkflowRunner{err: errors.New("compile failed")}})
	if err == nil || err.Error() != `running workflow "build" target "linux": compile failed` {
		t.Fatalf("error = %v", err)
	}
}

func TestRunWorkflowPropagatesCancellation(t *testing.T) {
	built, err := New(map[string]any{"workflow": "build"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = built.Run(ctx, step.Request{Workflows: cancelWorkflowRunner{}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context canceled", err)
	}
}

func TestRunWorkflowRejectsInvalidConfiguration(t *testing.T) {
	tests := []struct {
		name string
		raw  map[string]any
		want string
	}{
		{name: "missing workflow", raw: map[string]any{}, want: "static discovered workflow name"},
		{name: "dynamic workflow", raw: map[string]any{"workflow": "{{ .vars.workflow }}"}, want: "must be static"},
		{name: "dynamic target", raw: map[string]any{"workflow": "build", "target": "{{ .vars.target }}"}, want: "must be static"},
		{name: "unknown field", raw: map[string]any{"workflow": "build", "wait": true}, want: "field wait not found"},
		{name: "invalid expression", raw: map[string]any{"workflow": "build", "vars": map[string]any{"value": map[string]any{"expr": "("}}}, want: "compiling vars.value.expr"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := New(test.raw)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want containing %q", err, test.want)
			}
		})
	}
}
