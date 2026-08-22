package engine_test

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/up2jj/wuko/engine"
	"github.com/up2jj/wuko/step"
	importvarsstep "github.com/up2jj/wuko/steps/import_vars"
	setstep "github.com/up2jj/wuko/steps/set"
	"github.com/up2jj/wuko/workflow"
)

func TestImportedVariablesAreVisibleToLaterSteps(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "variables.json"), []byte(`{"target":"linux"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	definition := &workflow.Definition{
		Version: 1, Name: "imports", Dir: root, Vars: map[string]any{"target": "initial"},
		Steps: []workflow.Step{
			{ID: "load", Type: "import_vars", With: map[string]any{"files": []any{"variables.json"}}},
			{ID: "artifact", Type: "set", With: map[string]any{"variable": "artifact", "expr": `vars.target + "-archive"`}},
		},
	}
	registry := newTestRegistry(t, nil)
	for _, register := range []func(*step.Registry) error{importvarsstep.Register, setstep.Register} {
		if err := register(registry); err != nil {
			t.Fatal(err)
		}
	}
	state, err := engine.New(registry).Run(t.Context(), definition, engine.Options{RunDir: root, Stdout: io.Discard, Stderr: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if state.Vars["target"] != "linux" || state.Vars["artifact"] != "linux-archive" {
		t.Fatalf("variables = %#v", state.Vars)
	}
	if state.Steps["load"].(map[string]any)["count"] != 1 {
		t.Fatalf("load outputs = %#v", state.Steps["load"])
	}
}
