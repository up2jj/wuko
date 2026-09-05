package engine

import (
	"context"
	"io"
	"reflect"
	"sync"
	"testing"

	"github.com/up2jj/wuko/provider"
	"github.com/up2jj/wuko/step"
	luastep "github.com/up2jj/wuko/steps/lua"
	setstep "github.com/up2jj/wuko/steps/set"
	"github.com/up2jj/wuko/workflow"
)

type testExecutionProvider struct{}

func (testExecutionProvider) Name() string { return "acme" }
func (testExecutionProvider) Schema() provider.Schema {
	return provider.Object(map[string]provider.Schema{
		"region": provider.Scalar(), "enabled": provider.Scalar(), "count": provider.Scalar(),
		"payload": provider.OpenObject(),
	})
}
func (testExecutionProvider) Load(context.Context, map[string]string) (map[string]any, bool, error) {
	return map[string]any{
		"region": "eu-central", "enabled": true, "count": int64(2),
		"payload": map[string]any{"arbitrary": map[string]any{"value": "open"}},
	}, true, nil
}

func testProviderSet(t *testing.T) provider.Set {
	t.Helper()
	var registry provider.Registry
	if err := registry.Register(testExecutionProvider{}); err != nil {
		t.Fatal(err)
	}
	set, err := registry.Load(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	return set
}

func TestRegisteredProviderReachesEveryEvaluationSurface(t *testing.T) {
	registry := newTestRegistry(t, map[string]step.Builder{"capture": func(raw map[string]any) (step.Runner, error) {
		return countingRunner{value: raw["value"]}, nil
	}})
	for _, register := range []func(*step.Registry) error{setstep.Register, luastep.Register} {
		if err := register(registry); err != nil {
			t.Fatal(err)
		}
	}
	action := testAction(t, "provider-action", workflow.Step{
		ID: "inner", Type: "capture", With: map[string]any{"value": `{{ .inputs.target }}`},
	})
	action.Inputs = map[string]workflow.ActionInput{"target": {Type: "string", Required: true}}
	action.Outputs = map[string]workflow.ActionOutput{
		"value": {Value: `steps.inner.value + ":" + acme.region`},
	}
	definition := testDefinition(t, "provider-context",
		workflow.Step{ID: "template", Type: "capture", With: map[string]any{"value": `{{ template "provider" . }}`}},
		workflow.Step{ID: "expression", Type: "set", With: map[string]any{"variable": "count", "expr": `acme.count + 1`}},
		workflow.Step{ID: "condition", Type: "capture", If: `acme.enabled`, With: map[string]any{"value": `{{ .env.PROVIDER_REGION }}`}},
		workflow.Step{ID: "lua", Type: "lua", With: map[string]any{"source": `wuko.output("value", wuko.acme.payload.arbitrary.value)`}},
		workflow.Step{ID: "action", Uses: workflow.ActionSource{URL: "https://example.test/provider"}, Action: action, With: map[string]any{
			"target": map[string]any{"expr": `acme.region`},
		}},
	)
	definition.Templates = map[string]workflow.TemplateDefinition{
		"provider": {Inline: `{{ .acme.region }}`},
	}
	definition.Env = workflow.Environment{"PROVIDER_REGION": `{{ .acme.region }}`}
	providers := testProviderSet(t)
	state, err := New(registry).Run(t.Context(), definition, Options{
		RunDir: t.TempDir(), Stdout: io.Discard, Stderr: io.Discard, Providers: providers,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := state.Steps["template"].(map[string]any)["value"]; got != "eu-central" {
		t.Fatalf("named template value = %#v", got)
	}
	if got := state.Steps["expression"].(map[string]any)["value"]; got != 3 {
		t.Fatalf("Expr value = %#v (%T)", got, got)
	}
	if got := state.Steps["condition"].(map[string]any)["value"]; got != "eu-central" {
		t.Fatalf("condition/environment value = %#v", got)
	}
	if got := state.Steps["lua"].(map[string]any)["value"]; got != "open" {
		t.Fatalf("Lua value = %#v", got)
	}
	if got := state.Steps["action"].(map[string]any)["value"]; got != "eu-central:eu-central" {
		t.Fatalf("nested action value = %#v", got)
	}
	if !reflect.DeepEqual(state.Providers.Values, providers.Values) {
		t.Fatalf("state provider snapshot = %#v", state.Providers.Values)
	}
}

func TestProviderValuesAreIsolatedAcrossConcurrentAndNestedExecution(t *testing.T) {
	var mutex sync.Mutex
	seen := make([]string, 0, 3)
	registry := newTestRegistry(t, map[string]step.Builder{
		"mutate_provider": func(map[string]any) (step.Runner, error) {
			return runnerFunc(func(_ context.Context, request step.Request) (step.Result, error) {
				root := request.Providers.Values["acme"]
				mutex.Lock()
				seen = append(seen, root["region"].(string))
				mutex.Unlock()
				root["region"] = "mutated"
				root["payload"].(map[string]any)["branch"] = request.StepID
				return step.Result{Outputs: map[string]any{"value": "done"}}, nil
			}), nil
		},
	})
	action := testAction(t, "nested", workflow.Step{ID: "inner", Type: "mutate_provider"})
	definition := testDefinition(t, "isolation",
		workflow.Step{Concurrent: &workflow.ConcurrentGroup{MaxConcurrency: 2, Steps: []workflow.Step{
			{ID: "left", Type: "mutate_provider"},
			{ID: "right", Type: "mutate_provider"},
		}}},
		workflow.Step{ID: "nested", Uses: workflow.ActionSource{URL: "https://example.test/nested"}, Action: action},
	)
	providers := testProviderSet(t)
	state, err := New(registry).Run(t.Context(), definition, Options{
		RunDir: t.TempDir(), Stdout: io.Discard, Stderr: io.Discard, Providers: providers,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(seen) != 3 {
		t.Fatalf("provider observations = %v", seen)
	}
	for _, value := range seen {
		if value != "eu-central" {
			t.Fatalf("provider observations = %v", seen)
		}
	}
	if got := state.Providers.Values["acme"]["region"]; got != "eu-central" {
		t.Fatalf("state provider mutated to %#v", got)
	}
	if _, exists := state.Providers.Values["acme"]["payload"].(map[string]any)["branch"]; exists {
		t.Fatal("nested provider mutation escaped its request")
	}
}
