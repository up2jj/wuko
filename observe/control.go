package observe

import (
	"context"
	"fmt"

	"github.com/up2jj/wuko/engine"
	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/workflow"
)

// Control adapts observe declarations to the engine's generic background-control contract.
type Control struct {
	sources *Registry
}

func NewControl(sources *Registry) *Control {
	if sources == nil {
		sources = NewDefaultRegistry()
	}
	return &Control{sources: sources}
}

func (*Control) Kind() string                            { return "observe" }
func (*Control) Matches(workflowStep workflow.Step) bool { return workflowStep.IsObserve() }
func (*Control) Body(workflowStep workflow.Step) []workflow.Step {
	return workflowStep.Observe.Steps
}
func (*Control) BindingRoot() string { return "observe" }
func (*Control) Configuration(workflowStep workflow.Step) any {
	return workflowStep.Observe.Source.With
}

func (control *Control) Validate(_ context.Context, workflowStep workflow.Step) error {
	group := workflowStep.Observe
	if err := group.Validate(); err != nil {
		return err
	}
	if err := control.sources.Validate(group.Source.Type, group.Source.With); err != nil {
		return fmt.Errorf("observe source %s: %w", group.Source.Type, err)
	}
	return nil
}

func (control *Control) Launch(ctx context.Context, request engine.BackgroundControlRequest) (engine.BackgroundControlProgram, error) {
	group := request.Step.Observe
	rendered, err := request.Render(group.Source.With)
	if err != nil {
		return engine.BackgroundControlProgram{}, fmt.Errorf("rendering source: %w", err)
	}
	if rendered == nil {
		rendered = map[string]any{}
	}
	raw, ok := rendered.(map[string]any)
	if !ok {
		return engine.BackgroundControlProgram{}, fmt.Errorf("rendered source configuration has type %T", rendered)
	}
	source, err := control.sources.Open(ctx, group.Source.Type, OpenRequest{RunDir: request.RunDir, Env: request.Env, Config: raw})
	if err != nil {
		return engine.BackgroundControlProgram{}, err
	}
	metadata := map[string]any{"type": group.Source.Type}
	for key, value := range source.Metadata() {
		metadata[key] = value
	}
	scheduler := Scheduler{
		Source: source, SourceType: group.Source.Type,
		Debounce: group.EffectiveDebounce(), OnChange: group.EffectiveOnChange(), OnError: group.EffectiveOnError(),
	}
	return engine.BackgroundControlProgram{
		Result: step.Result{Outputs: map[string]any{
			"status": "observing", "source": metadata,
			"debounce": group.EffectiveDebounce().String(), "on_change": group.EffectiveOnChange(),
			"on_error": group.EffectiveOnError(),
		}},
		Run:   scheduler.Run,
		Close: source.Close,
	}, nil
}

var _ engine.BackgroundControl = (*Control)(nil)
