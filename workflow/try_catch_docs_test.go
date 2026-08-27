package workflow

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestDocumentedTryCatchWorkflowLoads(t *testing.T) {
	blocks := documentedYAMLBlocks(t)
	example := documentedBlock(t, blocks, "name: recover-deployment")
	path := filepath.Join(t.TempDir(), "workflow.yaml")
	if err := os.WriteFile(path, []byte(example), 0o644); err != nil {
		t.Fatal(err)
	}
	definition, err := NewLoader(nil).Decode(path, LoadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(definition.Steps) != 2 || !definition.Steps[0].IsTryCatch() {
		t.Fatalf("steps = %#v", definition.Steps)
	}
}

func TestDocumentedTryCatchOutputShapes(t *testing.T) {
	var outputs []map[string]any
	for _, block := range documentedYAMLBlocks(t) {
		if !strings.HasPrefix(block, "steps.deployment:\n") {
			continue
		}
		var document map[string]any
		if err := yaml.Unmarshal([]byte(block), &document); err != nil {
			t.Fatalf("decoding documented output: %v", err)
		}
		outputs = append(outputs, document["steps.deployment"].(map[string]any))
	}
	if len(outputs) != 2 {
		t.Fatalf("documented outputs = %d, want 2", len(outputs))
	}
	if outputs[0]["recovered"] != false || outputs[0]["catch"].(map[string]any)["status"] != "skipped" {
		t.Fatalf("successful outcome = %#v", outputs[0])
	}
	if outputs[1]["recovered"] != true || outputs[1]["try"].(map[string]any)["status"] != "failed" || outputs[1]["catch"].(map[string]any)["status"] != "succeeded" {
		t.Fatalf("recovered outcome = %#v", outputs[1])
	}
}
