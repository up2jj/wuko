package workflow

import (
	"path/filepath"
	"testing"

	"github.com/up2jj/wuko/internal/testfs"
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
	testfs.WriteFile(t, path, []byte(data), 0o600)
	definition, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !definition.HasForm() {
		t.Fatal("HasForm() = false")
	}
}
