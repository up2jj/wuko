package workflow

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCoreLoaderKeepsFormOpaque(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "workflow.yaml")
	data := `version: 1
name: opaque-form
form:
  deliberately_unknown_form_field: true
steps:
  - return: {outputs: {ok: "true"}}
`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	definition, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !definition.HasForm() {
		t.Fatal("HasForm() = false")
	}
}
