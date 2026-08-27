package multiplexer

import (
	"context"
	"strings"
	"testing"

	mux "github.com/up2jj/wuko/multiplexer"
	"github.com/up2jj/wuko/step"
)

type fakeController struct {
	request mux.Request
	result  mux.Result
	err     error
}

func (controller *fakeController) Execute(_ context.Context, _ map[string]string, request mux.Request) (mux.Result, error) {
	controller.request = request
	return controller.result, controller.err
}

func TestConfigValidation(t *testing.T) {
	tests := []struct {
		name string
		raw  map[string]any
		want string
	}{
		{name: "title", raw: map[string]any{"operation": "title", "title": "Build"}},
		{name: "missing title", raw: map[string]any{"operation": "title"}, want: "title is required"},
		{name: "clear title rejects value", raw: map[string]any{"operation": "clear_title", "title": "old"}, want: "title is not allowed"},
		{name: "invalid provider", raw: map[string]any{"operation": "clear_title", "provider": "screen"}, want: "provider must be"},
		{name: "zoom", raw: map[string]any{"operation": "zoom", "mode": "on"}},
		{name: "invalid zoom", raw: map[string]any{"operation": "zoom", "mode": "yes"}, want: "mode must be"},
		{name: "progress zero", raw: map[string]any{"operation": "progress", "progress": float64(0)}},
		{name: "progress missing", raw: map[string]any{"operation": "progress"}, want: "progress is required"},
		{name: "progress high", raw: map[string]any{"operation": "progress", "progress": 1.1}, want: "between 0 and 1"},
		{name: "unsafe title", raw: map[string]any{"operation": "title", "title": "bad\x1btitle"}, want: "control characters"},
		{name: "metadata", raw: map[string]any{"operation": "metadata", "tokens": map[string]any{"stage": "test"}}},
		{name: "empty metadata", raw: map[string]any{"operation": "metadata"}, want: "set or clear"},
		{name: "zero metadata ttl", raw: map[string]any{"operation": "metadata", "title": "Build", "ttl_ms": 0}, want: "ttl_ms must be"},
		{name: "metadata set and clear", raw: map[string]any{"operation": "metadata", "title": "Build", "clear_title": true}, want: "cannot be combined"},
		{name: "unknown field", raw: map[string]any{"operation": "notify", "title": "Done", "mode": "on"}, want: "mode is not allowed"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := newRunner(test.raw, &fakeController{})
			if test.want == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestRunnerReturnsPortableOutputsAndDefaultsMetadataSource(t *testing.T) {
	controller := &fakeController{result: mux.Result{
		Active: true, Provider: mux.ProviderHerdr, Operation: mux.OperationMetadata, Target: "pane-3", Changed: true,
	}}
	runner, err := newRunner(map[string]any{"operation": "metadata", "title": "Testing"}, controller)
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(t.Context(), step.Request{WorkflowName: "release", StepID: "label", Env: map[string]string{"HERDR_ENV": "1"}})
	if err != nil {
		t.Fatal(err)
	}
	if controller.request.Provider != mux.ProviderAuto || controller.request.Source != "wuko.release.label" {
		t.Fatalf("request = %#v", controller.request)
	}
	if result.Outputs["active"] != true || result.Outputs["provider"] != "herdr" || result.Outputs["target"] != "pane-3" || result.Outputs["changed"] != true {
		t.Fatalf("outputs = %#v", result.Outputs)
	}
	if _, ok := any(runner).(step.ExecutorAware); ok {
		t.Fatal("multiplexer runner unexpectedly implements ExecutorAware")
	}
}
