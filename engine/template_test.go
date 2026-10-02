package engine

import (
	"testing"

	"github.com/up2jj/wuko/workflow"
)

func TestBoundTemplateRendererBuildsDataOnFirstRender(t *testing.T) {
	renderer, err := workflow.NewRenderer(nil)
	if err != nil {
		t.Fatal(err)
	}
	builds := 0
	bound := newBoundTemplateRenderer(renderer, func() map[string]any {
		builds++
		return map[string]any{"vars": map[string]any{"name": "billing"}}
	})
	if err := bound.Validate("{{ .vars.name }}"); err != nil {
		t.Fatal(err)
	}
	if builds != 0 {
		t.Fatalf("builds after Validate = %d, want 0", builds)
	}
	for range 2 {
		got, err := bound.Render("{{ .vars.name }}")
		if err != nil {
			t.Fatal(err)
		}
		if got != "billing" {
			t.Fatalf("rendered = %q", got)
		}
	}
	if builds != 1 {
		t.Fatalf("builds after two renders = %d, want 1", builds)
	}
}

func TestBoundTemplateRendererSnapshotFreezesDataAndOverlaysRequestRoots(t *testing.T) {
	renderer, err := workflow.NewRenderer(nil)
	if err != nil {
		t.Fatal(err)
	}
	data := map[string]any{"vars": map[string]any{"name": "before"}}
	bound := newBoundTemplateRenderer(renderer, func() map[string]any { return data })
	snapshot := bound.Snapshot()
	data["vars"].(map[string]any)["name"] = "after"

	got, err := snapshot.RenderContentWith(`{{ .vars.name }}:{{ .request.path }}:{{ .state.count }}`, map[string]any{
		"request": map[string]any{"path": "/users"}, "state": map[string]any{"count": 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != "before:/users:2" {
		t.Fatalf("rendered = %q", got)
	}
}

func TestDashboardStepDefersLiveTemplatesAndCallbacks(t *testing.T) {
	renderer, err := workflow.NewRenderer(nil)
	if err != nil {
		t.Fatal(err)
	}
	configured := map[string]any{
		"title": "{{ .vars.title }}",
		"groups": []any{map[string]any{
			"refresh": map[string]any{
				"steps":   []any{map[string]any{"with": map[string]any{"script": "echo {{ .data.value }}"}}},
				"outputs": map[string]any{"value": "steps.read.stdout"},
			},
			"widgets": []any{map[string]any{
				"template": "{{ .data.value }}",
				"actions":  []any{map[string]any{"confirm": "Run for {{ .data.value }}?"}},
			}},
		}},
	}

	rendered, err := renderStepValue(renderer, configured, map[string]any{"vars": map[string]any{"title": "Live"}}, "dashboard")
	if err != nil {
		t.Fatalf("renderStepValue() error = %v", err)
	}
	result := rendered.(map[string]any)
	if got := result["title"]; got != "Live" {
		t.Fatalf("title = %#v, want Live", got)
	}
	group := result["groups"].([]any)[0].(map[string]any)
	widget := group["widgets"].([]any)[0].(map[string]any)
	if got := widget["template"]; got != "{{ .data.value }}" {
		t.Fatalf("widget template rendered early: %#v", got)
	}
	action := widget["actions"].([]any)[0].(map[string]any)
	if got := action["confirm"]; got != "Run for {{ .data.value }}?" {
		t.Fatalf("confirmation rendered early: %#v", got)
	}
	callback := group["refresh"].(map[string]any)["steps"].([]any)[0].(map[string]any)
	if got := callback["with"].(map[string]any)["script"]; got != "echo {{ .data.value }}" {
		t.Fatalf("callback rendered early: %#v", got)
	}
}
