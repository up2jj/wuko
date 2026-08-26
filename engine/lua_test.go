package engine

import (
	"context"
	"testing"

	"github.com/up2jj/wuko/step"
	luastep "github.com/up2jj/wuko/steps/lua"
	"github.com/up2jj/wuko/workflow"
)

func TestLuaConsumesTypedStepOutputThroughArgumentExpression(t *testing.T) {
	registry := newTestRegistry(t, map[string]step.Builder{
		"inventory": func(map[string]any) (step.Runner, error) {
			return runnerFunc(func(context.Context, step.Request) (step.Result, error) {
				return step.Result{Outputs: map[string]any{"value": []any{
					map[string]any{"name": "api", "replicas": 2},
					map[string]any{"name": "worker", "replicas": 1},
				}}}, nil
			}), nil
		},
	})
	if err := luastep.Register(registry); err != nil {
		t.Fatal(err)
	}
	definition := testDefinition(t, "deploy",
		workflow.Step{ID: "decode_deployments", Type: "inventory"},
		workflow.Step{ID: "inspect", Type: "lua", With: map[string]any{
			"source": `wuko.output("selected", {name = wuko.args.inventory[2].name, replicas = wuko.args.inventory[2].replicas})`,
			"args": map[string]any{
				"inventory": map[string]any{"expr": "steps.decode_deployments.value"},
			},
		}},
	)
	state, err := New(registry).Run(t.Context(), definition, Options{})
	if err != nil {
		t.Fatal(err)
	}
	selected := state.Steps["inspect"].(map[string]any)["selected"].(map[string]any)
	if selected["name"] != "worker" || selected["replicas"] != float64(1) {
		t.Fatalf("selected = %#v", selected)
	}
}
