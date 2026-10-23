//go:build fuzz

package workflow

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/up2jj/wuko/internal/testfs"
	"go.yaml.in/yaml/v3"
)

// FuzzDecodeAndValidate checks that no workflow document, however malformed, panics while it is
// decoded, structurally validated, or has its step conditions rendered.
func FuzzDecodeAndValidate(f *testing.F) {
	for _, pattern := range []string{"../examples/*.yaml", "../examples/*/*.yaml"} {
		paths, err := filepath.Glob(pattern)
		if err != nil {
			f.Fatal(err)
		}
		for _, path := range paths {
			data, err := os.ReadFile(path)
			if err != nil {
				f.Fatal(err)
			}
			f.Add(data)
		}
	}
	f.Add([]byte("version: 1\nname: x\nsteps:\n  - ~\n  - []\n  - 5\n"))
	f.Add([]byte("version: 1\nname: x\nsteps:\n  - id: a\n    loop: {until: 'true', steps: [{id: b, type: shell, with: {command: x}}]}\n"))
	f.Fuzz(func(t *testing.T, data []byte) {
		var definition Definition
		if err := yaml.Unmarshal(data, &definition); err != nil {
			return
		}
		_ = definition.ValidateStructure()
		renderer, err := NewRenderer(definition.Templates)
		if err != nil {
			return
		}
		for _, workflowStep := range definition.Steps {
			_, _ = renderer.Render(string(workflowStep.If), map[string]any{"vars": map[string]any{}, "env": map[string]any{}})
		}
	})
}

// FuzzLoadWithRequiredFragment covers require expansion, which decodes a second document and
// reports errors with the context of the enclosing block.
func FuzzLoadWithRequiredFragment(f *testing.F) {
	f.Add([]byte("version: 1\nname: x\nsteps:\n  - require: fragment.yaml\n"), []byte("- id: a\n  type: shell\n  with: {command: x}\n"))
	f.Add([]byte("version: 1\nname: x\nsteps:\n  - id: l\n    loop:\n      until: 'true'\n      steps:\n        - require: missing.yaml\n"), []byte("[]\n"))
	f.Add([]byte("version: 1\nname: x\nsteps:\n  - require: fragment.yaml\n"), []byte("- require: fragment.yaml\n"))
	f.Fuzz(func(t *testing.T, main, fragment []byte) {
		dir := t.TempDir()
		testfs.WriteFile(t, filepath.Join(dir, "workflow.yaml"), main, 0o644)
		testfs.WriteFile(t, filepath.Join(dir, "fragment.yaml"), fragment, 0o644)
		_, _ = Load(filepath.Join(dir, "workflow.yaml"))
	})
}

// FuzzRender checks template rendering against arbitrary sources and data values.
func FuzzRender(f *testing.F) {
	for _, source := range []string{
		`{{ .vars.x | upper }}`, `{{ indent 2 .vars.x }}`, `{{ range $i, $v := .vars.list }}{{ $v }}{{ end }}`,
		`{{ toJSON .vars }}`, `{{ fromJSON .vars.x }}`, `{{ repeat 3 .vars.x }}`, `{{ index .vars.list 5 }}`,
		`{{ toYAML .vars }}`, `{{ define "t" }}{{ template "t" . }}{{ end }}{{ template "t" . }}`,
	} {
		f.Add(source, "abc")
	}
	renderer, err := NewRenderer(nil)
	if err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, source, value string) {
		data := map[string]any{
			"vars":  map[string]any{"x": value, "list": []any{value, 1, nil}, "n": 3},
			"env":   map[string]any{},
			"steps": map[string]any{},
		}
		_, _ = renderer.RenderUncached(source, data)
	})
}
