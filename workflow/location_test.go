package workflow

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/up2jj/wuko/diagnostic"
)

func TestRequiredStepsPreserveSourceLocations(t *testing.T) {
	dir := t.TempDir()
	fragment := filepath.Join(dir, "steps.yaml")
	if err := os.WriteFile(fragment, []byte(`steps:
  - concurrent:
      steps:
        - id: first
          type: shell
        - id: second
          type: shell
`), 0o644); err != nil {
		t.Fatal(err)
	}
	workflowPath := filepath.Join(dir, "workflow.yaml")
	if err := os.WriteFile(workflowPath, []byte(`version: 1
name: located
steps:
  - require: steps.yaml
`), 0o644); err != nil {
		t.Fatal(err)
	}

	definition, err := loadLocal(workflowPath)
	if err != nil {
		t.Fatal(err)
	}
	if definition.Location.Source != workflowPath || definition.Location.Line != 1 {
		t.Fatalf("definition location = %#v", definition.Location)
	}
	children := definition.Steps[0].Concurrent.Steps
	if len(children) != 2 || children[0].Location.Source != fragment || children[0].Location.Line != 4 || children[1].Location.Line != 6 {
		t.Fatalf("child locations = %#v", []any{children[0].Location, children[1].Location})
	}
}

func TestRemapDefinitionLocationsUsesLogicalRemoteSources(t *testing.T) {
	root := t.TempDir()
	definition := &Definition{
		Location: location(filepath.Join(root, "wuko.yaml"), 1),
		Steps:    []Step{{Location: location(filepath.Join(root, "steps", "build.yaml"), 4)}},
	}
	remapDefinitionLocations(definition, root, "https://example.test/release.zip")
	if definition.Location.Source != "https://example.test/release.zip" {
		t.Fatalf("definition source = %q", definition.Location.Source)
	}
	if definition.Steps[0].Location.Source != "https://example.test/release.zip::steps/build.yaml" {
		t.Fatalf("step source = %q", definition.Steps[0].Location.Source)
	}
}

func TestSchemaDiagnosticUsesFailingStepLocation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workflow.yaml")
	if err := os.WriteFile(path, []byte("version: 1\nname: invalid\nsteps:\n  - id: broken\n    with: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var events []diagnostic.Event
	_, err := loadLocalWithDiagnostics(path, func(event diagnostic.Event) { events = append(events, event) }, "", "")
	if err == nil {
		t.Fatal("expected schema error")
	}
	for _, event := range events {
		if event.Phase == diagnostic.PhaseValidation && event.Status == diagnostic.StatusFailed {
			if event.Location.Source != path || event.Location.Line != 4 {
				t.Fatalf("failure location = %#v", event.Location)
			}
			return
		}
	}
	t.Fatalf("missing validation failure: %#v", events)
}

func TestSchemaDiagnosticUsesDeferredStepLocation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workflow.yaml")
	if err := os.WriteFile(path, []byte(`version: 1
name: invalid
steps:
  - id: create
    type: shell
    defer:
      - id: remove
        with: {}
`), 0o644); err != nil {
		t.Fatal(err)
	}
	var events []diagnostic.Event
	_, err := loadLocalWithDiagnostics(path, func(event diagnostic.Event) { events = append(events, event) }, "", "")
	if err == nil {
		t.Fatal("expected schema error")
	}
	for _, event := range events {
		if event.Phase == diagnostic.PhaseValidation && event.Status == diagnostic.StatusFailed {
			if event.Location.Source != path || event.Location.Line != 7 {
				t.Fatalf("failure location = %#v", event.Location)
			}
			return
		}
	}
	t.Fatalf("missing validation failure: %#v", events)
}

func location(source string, line int) diagnostic.Location {
	return diagnostic.Location{Source: source, Line: line, Column: 1}
}
