package dashboard

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/tui"
)

type fakeRenderer struct{}

func (fakeRenderer) Validate(string) error                                     { return nil }
func (fakeRenderer) Render(value string) (string, error)                       { return value, nil }
func (fakeRenderer) ValidateContent(string) error                              { return nil }
func (fakeRenderer) RenderContent(value string) (string, error)                { return value, nil }
func (fakeRenderer) RenderWith(value string, _ map[string]any) (string, error) { return value, nil }
func (fakeRenderer) RenderContentWith(value string, data map[string]any) (string, error) {
	return value + ":" + data["refresh"].(map[string]any)["status"].(string), nil
}
func (fakeRenderer) Snapshot() step.DataTemplateRenderer       { return fakeRenderer{} }
func (fakeRenderer) WithoutSecrets() step.DataTemplateRenderer { return fakeRenderer{} }

type fakeInline struct{ calls []step.InlineWorkflowCall }

func (fake *fakeInline) ValidateInlineWorkflow(_ context.Context, call step.InlineWorkflowCall) error {
	fake.calls = append(fake.calls, call)
	return nil
}

func (fake *fakeInline) RunInlineWorkflow(_ context.Context, call step.InlineWorkflowCall) (map[string]any, error) {
	fake.calls = append(fake.calls, call)
	return map[string]any{"value": "ok"}, nil
}

func dashboardRaw() map[string]any {
	return map[string]any{
		"groups": []any{map[string]any{
			"id": "system",
			"refresh": map[string]any{
				"steps":   []any{map[string]any{"id": "read", "type": "shell", "with": map[string]any{"script": "uptime"}}},
				"outputs": map[string]any{"uptime": "steps.read.stdout"},
			},
			"widgets": []any{map[string]any{
				"id": "health", "template": "{{ .data.uptime }}",
				"actions": []any{map[string]any{
					"id": "inspect", "confirm": "Inspect?", "steps": []any{map[string]any{"id": "inspect", "type": "shell", "with": map[string]any{"script": "echo {{ .data.uptime }}"}}},
				}},
			}},
		}},
	}
}

func TestNewAppliesDashboardDefaults(t *testing.T) {
	built, err := New(dashboardRaw())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	runner := built.(*Runner)
	if runner.config.Title != "Dashboard" || runner.config.MaxConcurrency != 4 {
		t.Fatalf("defaults = title %q concurrency %d", runner.config.Title, runner.config.MaxConcurrency)
	}
	if runner.config.Groups[0].Columns != 2 || runner.config.Groups[0].Title != "system" {
		t.Fatalf("group defaults = %#v", runner.config.Groups[0])
	}
}

func TestValidateChecksRefreshAndActionCallbacks(t *testing.T) {
	built, err := New(dashboardRaw())
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeInline{}
	err = built.(*Runner).Validate(t.Context(), step.Request{
		Steps: map[string]any{"before": map[string]any{"value": "ready"}}, TemplateRenderer: fakeRenderer{}, InlineWorkflows: fake,
	})
	if err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if got, want := len(fake.calls), 2; got != want {
		t.Fatalf("validated calls = %d, want %d", got, want)
	}
	if data := fake.calls[0].Bindings["data"].(map[string]any); data["uptime"] != "" {
		t.Fatalf("action validation data = %#v", data)
	}
}

func TestRunRequiresInteractiveTerminal(t *testing.T) {
	built, err := New(dashboardRaw())
	if err != nil {
		t.Fatal(err)
	}
	_, err = built.Run(t.Context(), step.Request{})
	if err == nil || !strings.Contains(err.Error(), "interactive terminal") {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestRunReturnsDashboardSnapshot(t *testing.T) {
	built, err := New(dashboardRaw())
	if err != nil {
		t.Fatal(err)
	}
	runner := built.(*Runner)
	runner.show = func(_ context.Context, _ io.Reader, _ io.Writer, config tui.DashboardConfig) (tui.DashboardResult, error) {
		if got, want := config.Title, "Dashboard"; got != want {
			t.Fatalf("title = %q, want %q", got, want)
		}
		return tui.DashboardResult{Groups: []tui.DashboardGroupResult{{
			ID: "system", State: tui.DashboardState{Status: "succeeded", Data: map[string]any{"uptime": "up"}},
			Widgets: []tui.DashboardWidgetResult{{ID: "health", Actions: map[string]tui.DashboardState{"inspect": {Status: "idle", Data: map[string]any{}}}}},
		}}}, nil
	}
	result, err := runner.Run(t.Context(), step.Request{
		Interactive: true, Stdin: strings.NewReader(""), Stdout: io.Discard,
		TemplateRenderer: fakeRenderer{}, InlineWorkflows: &fakeInline{},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	groups := result.Outputs["groups"].(map[string]any)
	system := groups["system"].(map[string]any)
	if got, want := system["status"], "succeeded"; got != want {
		t.Fatalf("status = %#v, want %#v", got, want)
	}
}
