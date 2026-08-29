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
