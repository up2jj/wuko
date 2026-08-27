package workflow

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestDocumentedCancelOnWorkflowLoads(t *testing.T) {
	blocks := documentedYAMLBlocks(t)
	example := documentedBlock(t, blocks, "name: monitored-deployment")
	path := filepath.Join(t.TempDir(), "workflow.yaml")
	if err := os.WriteFile(path, []byte(example), 0o644); err != nil {
		t.Fatal(err)
	}
	definition, err := NewLoader(nil).Decode(path, LoadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(definition.Steps) != 1 || !definition.Steps[0].IsCancelOn() {
		t.Fatalf("steps = %#v", definition.Steps)
	}
}

func TestDocumentedCancelOnOutputShapes(t *testing.T) {
	blocks := documentedYAMLBlocks(t)
	var outputs []map[string]any
	for _, block := range blocks {
		if !strings.HasPrefix(block, "steps.deployment_watch:\n") {
			continue
		}
		var document map[string]any
		if err := yaml.Unmarshal([]byte(block), &document); err != nil {
			t.Fatalf("decoding documented output: %v", err)
		}
		output, ok := document["steps.deployment_watch"].(map[string]any)
		if !ok {
			t.Fatalf("documented output = %#v", document)
		}
		outputs = append(outputs, output)
	}
	if len(outputs) != 3 {
		t.Fatalf("documented outputs = %d, want 3", len(outputs))
	}

	bodyWinner := outputs[0]
	if bodyWinner["status"] != "succeeded" || bodyWinner["triggered"] != false {
		t.Fatalf("body winner = %#v", bodyWinner)
	}
	if bodyWinner["winner"].(map[string]any)["kind"] != "body" {
		t.Fatalf("body winner metadata = %#v", bodyWinner["winner"])
	}
	if bodyWinner["steps"].(map[string]any)["deploy"].(map[string]any)["status"] != "succeeded" {
		t.Fatalf("body deploy = %#v", bodyWinner["steps"])
	}
	if bodyWinner["monitors"].(map[string]any)["deployment_finished"].(map[string]any)["status"] != "canceled" {
		t.Fatalf("body monitors = %#v", bodyWinner["monitors"])
	}
	if bodyWinner["vars"].(map[string]any)["artifact"] != "dist/app.tar" {
		t.Fatalf("body vars = %#v", bodyWinner["vars"])
	}

	partial := outputs[1]
	if partial["steps"].(map[string]any)["deploy"].(map[string]any)["outputs"] != nil {
		t.Fatalf("partial deploy = %#v", partial["steps"])
	}
	if partial["monitors"].(map[string]any)["deployment_finished"].(map[string]any)["status"] != "succeeded" {
		t.Fatalf("partial monitors = %#v", partial["monitors"])
	}

	early := outputs[2]
	if early["steps"].(map[string]any)["prepare"].(map[string]any)["status"] != "skipped" || len(early["vars"].(map[string]any)) != 0 {
		t.Fatalf("early body = %#v", early)
	}
}

func documentedYAMLBlocks(t *testing.T) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "docs", "workflow-control.md"))
	if err != nil {
		t.Fatal(err)
	}
	sections := strings.Split(string(data), "```yaml\n")
	blocks := make([]string, 0, len(sections)-1)
	for _, section := range sections[1:] {
		block, _, found := strings.Cut(section, "```")
		if found {
			blocks = append(blocks, block)
		}
	}
	return blocks
}

func documentedBlock(t *testing.T, blocks []string, marker string) string {
	t.Helper()
	for _, block := range blocks {
		if strings.Contains(block, marker) {
			return block
		}
	}
	t.Fatalf("documented YAML block containing %q not found", marker)
	return ""
}
