package engine

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/up2jj/wuko/diagnostic"
	"github.com/up2jj/wuko/step"
	changedstep "github.com/up2jj/wuko/steps/changed"
	"github.com/up2jj/wuko/workflow"
)

func TestChangedStepGuardsDownstreamWorkAndRendersValues(t *testing.T) {
	registry, runs := changedRegistry(t)
	root := t.TempDir()
	localDir := filepath.Join(root, ".wuko", "values")
	definition := &workflow.Definition{
		Version: 1, Name: "changed-integration", Dir: root, Vars: map[string]any{"target": "linux"},
		Location: diagnostic.Location{Source: filepath.Join(root, "workflow.yaml")},
		Steps: []workflow.Step{
			{ID: "detect", Type: "changed", With: map[string]any{"values": map[string]any{"target": "{{ .vars.target }}"}}},
			{ID: "build", Type: "changed_capture", If: "steps.detect.changed", With: map[string]any{"value": "{{ .vars.target }}"}},
		},
	}
	options := Options{RunDir: root, LocalValueDir: localDir, Stdout: io.Discard, Stderr: io.Discard}
	state, err := New(registry).Run(t.Context(), definition, options)
	if err != nil {
		t.Fatal(err)
	}
	if *runs != 1 || state.Steps["detect"].(map[string]any)["changed"] != true {
		t.Fatalf("runs = %d, state = %#v", *runs, state.Steps)
	}
	state, err = New(registry).Run(t.Context(), definition, options)
	if err != nil {
		t.Fatal(err)
	}
	if *runs != 1 || state.Steps["detect"].(map[string]any)["changed"] != false {
		t.Fatalf("runs = %d, state = %#v", *runs, state.Steps)
	}
	if _, exists := state.Steps["build"]; exists {
		t.Fatalf("guarded build was committed: %#v", state.Steps)
	}
	options.Vars = map[string]any{"target": "darwin"}
	state, err = New(registry).Run(t.Context(), definition, options)
	if err != nil {
		t.Fatal(err)
	}
	if *runs != 2 || state.Steps["build"].(map[string]any)["value"] != "darwin" {
		t.Fatalf("runs = %d, state = %#v", *runs, state.Steps)
	}
}

func TestChangedStepSupportsTemplatedForeachKeys(t *testing.T) {
	registry, runs := changedRegistry(t)
	root := t.TempDir()
	definition := &workflow.Definition{
		Version: 1, Name: "changed-foreach", Dir: root, Vars: map[string]any{"targets": []any{"linux", "darwin"}},
		Location: diagnostic.Location{Source: filepath.Join(root, "workflow.yaml")},
		Steps: []workflow.Step{{ID: "targets", Foreach: &workflow.ForeachGroup{
			Items: "vars.targets", Collect: "steps.detect.changed", MaxConcurrency: 1, MaxIterations: 10, FailFast: true,
			Steps: []workflow.Step{
				{ID: "detect", Type: "changed", With: map[string]any{
					"key": "target-{{ .foreach.item }}", "values": map[string]any{"target": "{{ .foreach.item }}"},
				}},
				{ID: "build", Type: "changed_capture", If: "steps.detect.changed", With: map[string]any{"value": "{{ .foreach.item }}"}},
			},
		}}},
	}
	options := Options{RunDir: root, LocalValueDir: filepath.Join(root, ".wuko", "values"), Stdout: io.Discard, Stderr: io.Discard}
	if _, err := New(registry).Run(t.Context(), definition, options); err != nil {
		t.Fatal(err)
	}
	if *runs != 2 {
		t.Fatalf("first run count = %d, want 2", *runs)
	}
	state, err := New(registry).Run(t.Context(), definition, options)
	if err != nil {
		t.Fatal(err)
	}
	if *runs != 2 {
		t.Fatalf("unchanged run count = %d, want 2", *runs)
	}
	results := state.Steps["targets"].(map[string]any)["results"].([]any)
	for _, result := range results {
		if result != false {
			t.Fatalf("collected result = %#v", result)
		}
	}
}

func TestChangedStepSupportsCompositeActionInputsInKey(t *testing.T) {
	registry, runs := changedRegistry(t)
	root := t.TempDir()
	action := &workflow.Action{
		Version: 1, Name: "changed-action", Dir: root,
		Location: diagnostic.Location{Source: "https://actions.example.test/changed@v1"},
		Inputs:   map[string]workflow.ActionInput{"target": {Type: "string", Required: true}},
		Outputs:  map[string]workflow.ActionOutput{"changed": {Value: "steps.detect.changed"}},
		Steps: []workflow.Step{
			{ID: "detect", Type: "changed", With: map[string]any{
				"key": "target-{{ .inputs.target }}", "values": map[string]any{"target": "{{ .inputs.target }}"},
			}},
			{ID: "build", Type: "changed_capture", If: "steps.detect.changed", With: map[string]any{"value": "{{ .inputs.target }}"}},
		},
	}
	definition := &workflow.Definition{
		Version: 1, Name: "caller", Dir: root, Location: diagnostic.Location{Source: filepath.Join(root, "workflow.yaml")},
		Steps: []workflow.Step{{ID: "call", Uses: workflow.ActionSource{URL: "https://actions.example.test/changed@v1"}, Action: action, With: map[string]any{"target": "linux"}}},
	}
	options := Options{RunDir: root, LocalValueDir: filepath.Join(root, ".wuko", "values"), Stdout: io.Discard, Stderr: io.Discard}
	if _, err := New(registry).Run(t.Context(), definition, options); err != nil {
		t.Fatal(err)
	}
	state, err := New(registry).Run(t.Context(), definition, options)
	if err != nil {
		t.Fatal(err)
	}
	if *runs != 1 || state.Steps["call"].(map[string]any)["changed"] != false {
		t.Fatalf("runs = %d, state = %#v", *runs, state.Steps)
	}
}

func TestChangedValidationAndDryRunDoNotCreateSnapshot(t *testing.T) {
	registry, _ := changedRegistry(t)
	root := t.TempDir()
	localDir := filepath.Join(root, ".wuko", "values")
	definition := &workflow.Definition{
		Version: 1, Name: "changed-dry-run", Dir: root, Location: diagnostic.Location{Source: filepath.Join(root, "workflow.yaml")},
		Steps: []workflow.Step{{ID: "detect", Type: "changed", With: map[string]any{"root": "missing", "files": []any{"**"}}}},
	}
	options := Options{RunDir: root, LocalValueDir: localDir, Stdout: io.Discard, Stderr: io.Discard}
	if err := New(registry).Validate(t.Context(), definition, options); err != nil {
		t.Fatal(err)
	}
	options.DryRun = true
	if _, err := New(registry).Run(t.Context(), definition, options); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(localDir, "changed.json")); !os.IsNotExist(err) {
		t.Fatalf("snapshot exists after validation/dry-run: %v", err)
	}
}

func changedRegistry(t *testing.T) (*step.Registry, *int) {
	t.Helper()
	registry := newTestRegistry(t, nil)
	if err := changedstep.Register(registry); err != nil {
		t.Fatal(err)
	}
	runs := new(int)
	if err := registry.Register("changed_capture", func(raw map[string]any) (step.Runner, error) {
		return countingRunner{value: raw["value"], runs: runs}, nil
	}); err != nil {
		t.Fatal(err)
	}
	return registry, runs
}
