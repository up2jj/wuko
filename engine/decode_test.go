package engine_test

import (
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/up2jj/wuko/engine"
	"github.com/up2jj/wuko/step"
	decodestep "github.com/up2jj/wuko/steps/decode"
	jsonpathstep "github.com/up2jj/wuko/steps/jsonpath"
	"github.com/up2jj/wuko/steps/shell"
	"github.com/up2jj/wuko/workflow"
)

func TestDecodeShellOutputFeedsJSONPath(t *testing.T) {
	registry := newTestRegistry(t, nil)
	for _, register := range []func(*step.Registry) error{shell.Register, decodestep.Register, jsonpathstep.Register} {
		if err := register(registry); err != nil {
			t.Fatal(err)
		}
	}
	definition := testDefinition(t, "decode",
		workflow.Step{ID: "deployments", Type: "shell", With: map[string]any{
			"script": `printf '%s' '{"items":[{"metadata":{"name":"api"}},{"metadata":{"name":"worker"}}]}'`,
		}},
		workflow.Step{ID: "deployment_data", Type: "decode", With: map[string]any{
			"format": "json", "from": "steps.deployments.stdout",
		}},
		workflow.Step{ID: "deployment_names", Type: "jsonpath", With: map[string]any{
			"from": "steps.deployment_data.value", "query": "$.items[*].metadata.name", "result": "all",
		}},
	)
	state, err := engine.New(registry).Run(t.Context(), definition, engine.Options{
		RunDir: t.TempDir(), Stdout: io.Discard, Stderr: io.Discard,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := state.Steps["deployment_names"].(map[string]any)["value"]; !reflect.DeepEqual(got, []any{"api", "worker"}) {
		t.Fatalf("deployment names = %#v", got)
	}
}

func TestDecodePathUsesWorkingDirectoryScope(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	if err := os.Mkdir(project, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "data.yaml"), []byte("name: scoped\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	definition := &workflow.Definition{
		Version: 1, Name: "decode-path", Dir: root,
		Steps: []workflow.Step{{WorkingDirectory: "project", Steps: []workflow.Step{{
			ID: "data", Type: "decode", With: map[string]any{"format": "yaml", "path": "data.yaml"},
		}}}},
	}
	registry := newTestRegistry(t, nil)
	if err := decodestep.Register(registry); err != nil {
		t.Fatal(err)
	}
	state, err := engine.New(registry).Run(t.Context(), definition, engine.Options{
		RunDir: root, Stdout: io.Discard, Stderr: io.Discard,
	})
	if err != nil {
		t.Fatal(err)
	}
	value := state.Steps["data"].(map[string]any)["value"].(map[string]any)
	if value["name"] != "scoped" {
		t.Fatalf("value = %#v", value)
	}
}
