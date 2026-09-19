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
		{name: "read visible", raw: map[string]any{"operation": "read"}},
		{name: "read scrollback", raw: map[string]any{"operation": "read", "scrollback": true, "lines": 200}},
		{name: "scrollback requires lines", raw: map[string]any{"operation": "read", "scrollback": true}, want: "lines is required"},
		{name: "lines require scrollback", raw: map[string]any{"operation": "read", "lines": 20}, want: "requires scrollback"},
		{name: "too many lines", raw: map[string]any{"operation": "read", "scrollback": true, "lines": 10_001}, want: "between 1 and 10000"},
		{name: "send text", raw: map[string]any{"operation": "send_text", "text": "echo hello"}},
		{name: "send text rejects enter", raw: map[string]any{"operation": "send_text", "text": "echo hello\n"}, want: "control characters"},
		{name: "send keys", raw: map[string]any{"operation": "send_keys", "keys": []any{"enter", "ctrl+c"}}},
		{name: "empty keys", raw: map[string]any{"operation": "send_keys", "keys": []any{}}, want: "keys must not be empty"},
		{name: "unknown key", raw: map[string]any{"operation": "send_keys", "keys": []any{"super+x"}}, want: "unsupported key"},
		{name: "split", raw: map[string]any{"operation": "split", "direction": "right"}},
		{name: "invalid split direction", raw: map[string]any{"operation": "split", "direction": "left"}, want: "direction must be right, down"},
		{name: "resize", raw: map[string]any{"operation": "resize", "direction": "up", "amount": 2.0}},
		{name: "invalid amount", raw: map[string]any{"operation": "resize", "direction": "up", "amount": 0.0}, want: "greater than zero"},
		{name: "focus pane", raw: map[string]any{"operation": "focus_pane", "target": "pane:2"}},
		{name: "focus pane requires target", raw: map[string]any{"operation": "focus_pane"}, want: "target is required"},
		{name: "focus direction", raw: map[string]any{"operation": "focus_direction", "direction": "left"}},
		{name: "close pane", raw: map[string]any{"operation": "close_pane", "target": "%4"}},
		{name: "close surface requires target", raw: map[string]any{"operation": "close_surface"}, want: "target is required"},
		{name: "empty target", raw: map[string]any{"operation": "send_text", "text": "x", "target": ""}, want: "target is required"},
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

func TestRunnerReturnsPaneControlOutputs(t *testing.T) {
	controller := &fakeController{result: mux.Result{
		Active: true, Provider: mux.ProviderCmux, Operation: mux.OperationSplit, Target: "surface:1", Changed: true,
		CreatedTarget: "surface:2", CreatedPane: "pane:2",
	}}
	runner, err := newRunner(map[string]any{"operation": "split", "direction": "down", "target": "surface:1"}, controller)
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(t.Context(), step.Request{Env: map[string]string{"CMUX_SURFACE_ID": "surface:1"}})
	if err != nil {
		t.Fatal(err)
	}
	if controller.request.Provider != mux.ProviderAuto || controller.request.Target != "surface:1" || controller.request.Direction != "down" {
		t.Fatalf("request = %#v", controller.request)
	}
	if result.Outputs["created_target"] != "surface:2" || result.Outputs["created_pane"] != "pane:2" || result.Outputs["text"] != "" {
		t.Fatalf("outputs = %#v", result.Outputs)
	}
}
