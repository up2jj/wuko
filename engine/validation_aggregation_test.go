package engine

import (
	"fmt"
	"testing"

	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/validation"
	"github.com/up2jj/wuko/workflow"
)

func TestValidateCollectsIndependentTopLevelStepFailures(t *testing.T) {
	registry := step.NewRegistry()
	if err := registry.RegisterDefinition("broken", step.Registration{
		Builder: func(raw map[string]any) (step.Runner, error) { return nil, fmt.Errorf("%v is invalid", raw["value"]) },
		Outputs: step.ClosedOutputs(),
	}); err != nil {
		t.Fatal(err)
	}
	definition := testDefinition(t, "multiple",
		workflow.Step{ID: "first", Type: "broken", With: map[string]any{"value": "one"}},
		workflow.Step{ID: "second", Type: "broken", With: map[string]any{"value": "two"}},
	)
	err := New(registry).Validate(t.Context(), definition, Options{})
	issues := validation.Issues(err)
	if len(issues) != 2 {
		t.Fatalf("issues = %#v, error = %v", issues, err)
	}
	if issues[0].Step != "first" || issues[1].Step != "second" {
		t.Fatalf("issues = %#v", issues)
	}
}

func TestValidateGatesStepPreparationAfterStructuralFailure(t *testing.T) {
	builds := 0
	registry := step.NewRegistry()
	if err := registry.Register("capture", func(map[string]any) (step.Runner, error) { builds++; return nil, nil }); err != nil {
		t.Fatal(err)
	}
	definition := testDefinition(t, "invalid", workflow.Step{ID: "bad", Type: "capture", Uses: workflow.ActionSource{URL: "https://example.test/action"}})
	if err := New(registry).Validate(t.Context(), definition, Options{}); err == nil {
		t.Fatal("expected structural failure")
	}
	if builds != 0 {
		t.Fatalf("builder called %d times", builds)
	}
}
