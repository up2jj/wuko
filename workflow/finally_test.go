package workflow

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadExpandsFinallyRequirementsAndTracksLocations(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	workflowPath := filepath.Join(dir, "workflow.yaml")
	fragmentPath := filepath.Join(dir, "cleanup.yaml")
	writeTestFile(t, workflowPath, `version: 1
name: cleanup
steps:
  - id: work
    type: shell
finally:
  - require: cleanup.yaml
`)
	writeTestFile(t, fragmentPath, `steps:
  - id: cleanup
    type: shell
`)
	definition, err := NewLoader(nil).Load(t.Context(), workflowPath, LoadOptions{RunDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if len(definition.Finally) != 1 || definition.Finally[0].ID != "cleanup" {
		t.Fatalf("finally = %#v", definition.Finally)
	}
	if definition.Finally[0].Location.Source != fragmentPath || definition.Finally[0].Location.Line == 0 {
		t.Fatalf("cleanup location = %#v", definition.Finally[0].Location)
	}
}

func TestLoadRejectsDuplicateIDsAcrossStepsAndFinally(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "workflow.yaml")
	writeTestFile(t, path, `version: 1
name: duplicate
steps:
  - id: shared
    type: shell
finally:
  - id: shared
    type: shell
`)
	_, err := NewLoader(nil).Load(t.Context(), path, LoadOptions{RunDir: dir})
	if err == nil || !strings.Contains(err.Error(), `duplicate step id "shared"`) {
		t.Fatalf("Load() error = %v", err)
	}
}

func TestDecodeActionAcceptsFinallyAndTracksLocation(t *testing.T) {
	t.Parallel()
	action, err := decodeActionPayload([]byte(`version: 1
name: action
steps:
  - id: work
    type: shell
finally:
  - id: cleanup
    type: shell
`), t.TempDir(), "https://example.test/action")
	if err != nil {
		t.Fatal(err)
	}
	if len(action.Finally) != 1 || action.Finally[0].ID != "cleanup" {
		t.Fatalf("finally = %#v", action.Finally)
	}
	if action.Finally[0].Location.Source != "https://example.test/action" || action.Finally[0].Location.Line == 0 {
		t.Fatalf("cleanup location = %#v", action.Finally[0].Location)
	}
}
