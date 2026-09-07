package workflow

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/up2jj/wuko/helper"
)

type fakePluginHelpers struct{ loads int }

func (loader *fakePluginHelpers) LoadHelpers(_ context.Context, plugins map[string]PluginSource) (helper.Set, error) {
	loader.loads++
	return helper.Set{"acme_slug": func(_ context.Context, args []any) (any, error) {
		return strings.ToLower(strings.ReplaceAll(args[0].(string), " ", "-")), nil
	}}, nil
}

func TestLoaderInitializesPluginHelpersBeforeTemplateValidation(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "workflow.yaml")
	writeTestFile(t, path, `version: 1
name: helpers
plugins:
  acme:
    source: https://example.test/plugin.json
    sha256: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
templates:
  slug: '{{ acme_slug .vars.name }}'
vars:
  name: Hello World
steps:
  - id: run
    type: shell
`)
	provider := &fakePluginHelpers{}
	loader := NewLoader(nil, WithPluginHelpers(provider))
	definition, err := loader.DecodeContext(t.Context(), path, LoadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if provider.loads != 1 {
		t.Fatalf("helper loads = %d, want 1", provider.loads)
	}
	renderer, err := NewRendererWithHelpers(t.Context(), definition.Templates, nil, definition.Helpers())
	if err != nil {
		t.Fatal(err)
	}
	value, err := renderer.Render(`{{ template "slug" . }}`, map[string]any{"vars": map[string]any{"name": "Hello World"}})
	if err != nil {
		t.Fatal(err)
	}
	if value != "hello-world" {
		t.Fatalf("rendered helper = %q", value)
	}
}

func TestDiscoveryAcceptsPluginHelperTemplatesWithoutAHelperLoader(t *testing.T) {
	directory := t.TempDir()
	writeTestFile(t, filepath.Join(directory, "demo.yaml"), `version: 1
name: demo
plugins:
  acme:
    source: https://example.test/plugin.json
    sha256: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
templates:
  slug: '{{ acme_slug .vars.name }}'
vars:
  name: Hello World
steps:
  - id: run
    type: shell
`)
	sources, err := DiscoverDirectory(directory, "local")
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 1 || sources[0].Name != "demo" {
		t.Fatalf("discovered sources = %#v", sources)
	}
}
