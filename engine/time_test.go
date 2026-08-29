package engine

import (
	"testing"
	stdtime "time"

	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/steps/set"
	timestep "github.com/up2jj/wuko/steps/time"
	"github.com/up2jj/wuko/workflow"
)

func TestTimeStepDeclaresImplicitVariableAndFeedsExpr(t *testing.T) {
	t.Parallel()
	registry := step.NewRegistry()
	if err := registry.Register("time", func(raw map[string]any) (step.Runner, error) {
		return timestep.NewWithClock(raw, func() stdtime.Time {
			return stdtime.Date(2026, 8, 29, 10, 0, 0, 0, stdtime.UTC)
		})
	}); err != nil {
		t.Fatal(err)
	}
	if err := set.Register(registry); err != nil {
		t.Fatal(err)
	}
	definition := &workflow.Definition{
		Version: 1, Name: "release", Timezone: "Europe/Warsaw", Vars: map[string]any{}, Env: workflow.Environment{},
		Steps: []workflow.Step{
			{ID: "stamp", Type: "time", With: map[string]any{"format": "2006-01-02T15:04:05Z07:00"}},
			{ID: "year", Type: "set", With: map[string]any{"variable": "year", "expr": `formatTime(vars.stamp, "2006", workflow.timezone)`}},
		},
	}
	state, err := New(registry).Run(t.Context(), definition, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if state.Steps["stamp"].(map[string]any)["value"] != "2026-08-29T12:00:00+02:00" || state.Vars["stamp"] != "2026-08-29T12:00:00+02:00" || state.Vars["year"] != "2026" {
		t.Fatalf("steps = %#v, vars = %#v", state.Steps, state.Vars)
	}
}
