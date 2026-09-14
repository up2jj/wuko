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
