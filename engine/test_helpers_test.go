package engine

import (
	"context"
	"testing"

	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/workflow"
)

type runnerFunc func(context.Context, step.Request) (step.Result, error)

func (run runnerFunc) Run(ctx context.Context, request step.Request) (step.Result, error) {
	return run(ctx, request)
}

func testDefinition(t *testing.T, name string, steps ...workflow.Step) *workflow.Definition {
	t.Helper()
	return &workflow.Definition{
		Version: 1,
		Name:    name,
		Dir:     t.TempDir(),
		Steps:   steps,
	}
}

func testAction(t *testing.T, name string, steps ...workflow.Step) *workflow.Action {
	t.Helper()
	return &workflow.Action{
		Version: 1,
		Name:    name,
		Dir:     t.TempDir(),
		Steps:   steps,
	}
}

func newTestRegistry(t *testing.T, builders map[string]step.Builder) *step.Registry {
	t.Helper()
	registry := step.NewRegistry()
	for name, builder := range builders {
		if err := registry.Register(name, builder); err != nil {
			t.Fatal(err)
		}
	}
	return registry
}

// immediateRetry builds a repeat policy with no backoff, the way most tests want it.
func immediateRetry(maximum int) workflow.AttemptControl {
	return workflow.AttemptControl{
		MaxAttempts:       workflow.LiteralCount(maximum),
		BackoffMultiplier: workflow.LiteralFactor(1),
	}
}

// attemptStep wraps one leaf step in an attempt control. Bounding and repeating are properties of
// the control now, so a test that used to set Timeout or Retry on the step wraps it instead. The
// control takes the id the assertions already use; the body step gets id+"_body", and its outputs
// are read at steps.<id>.steps.<id>_body.
func attemptStep(id string, control workflow.AttemptControl, body workflow.Step) workflow.Step {
	if body.ID == "" {
		body.ID = id + "_body"
	}
	if body.With == nil && body.Attempt == nil && body.Steps == nil {
		body.With = map[string]any{}
	}
	if control.MaxAttempts.Literal == 0 && control.MaxAttempts.Expression == "" {
		control.MaxAttempts.Literal = 1
	}
	if control.BackoffMultiplier.Literal == 0 && control.BackoffMultiplier.Expression == "" {
		control.BackoffMultiplier.Literal = 1
	}
	control.Steps = []workflow.Step{body}
	return workflow.Step{ID: id, Attempt: &control}
}

// attemptTimeout adds a per-pass bound to a policy.
func attemptTimeout(control workflow.AttemptControl, timeout *workflow.Duration) workflow.AttemptControl {
	if timeout != nil {
		control.Timeout = workflow.LiteralDuration(*timeout)
	}
	return control
}

// attemptBody returns the outputs a wrapped body step published, from the control's output.
func attemptBody(state *State, controlID, bodyID string) map[string]any {
	control, ok := state.Steps[controlID].(map[string]any)
	if !ok {
		return nil
	}
	steps, ok := control["steps"].(map[string]any)
	if !ok {
		return nil
	}
	outputs, _ := steps[bodyID].(map[string]any)
	return outputs
}

// attemptVars returns the variables a wrapped body wrote. The body is isolated, so they never
// reach state.Vars.
func attemptVars(state *State, controlID string) map[string]any {
	control, ok := state.Steps[controlID].(map[string]any)
	if !ok {
		return nil
	}
	vars, _ := control["vars"].(map[string]any)
	return vars
}
