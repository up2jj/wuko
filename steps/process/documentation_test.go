package process

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/workflow"
)

func TestDocumentedProcessWorkflowsDecodeAndConfigsBuild(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "docs", "steps-automation.md"))
	if err != nil {
		t.Fatal(err)
	}
	blocks := strings.Split(string(data), "```yaml\n")
	checked := 0
	for index, section := range blocks[1:] {
		block, _, found := strings.Cut(section, "```")
		if !found || !strings.Contains(block, "type: process") || !strings.HasPrefix(block, "version: 1\n") {
			continue
		}
		path := filepath.Join(t.TempDir(), "workflow.yaml")
		if err := os.WriteFile(path, []byte(block), 0o644); err != nil {
			t.Fatal(err)
		}
		definition, err := workflow.NewLoader(nil).Decode(path, workflow.LoadOptions{})
		if err != nil {
			t.Fatalf("documented process YAML block %d: %v\n%s", index+1, err, block)
		}
		if err := buildProcessConfigs(definition.Steps); err != nil {
			t.Fatalf("documented process YAML block %d: %v", index+1, err)
		}
		checked++
	}
	if checked != 22 {
		t.Fatalf("documented complete process examples = %d, want 22", checked)
	}
}

func TestRunnableProcessExamplesDecodeAndBuild(t *testing.T) {
	for _, name := range []string{"process-dag.yaml", "process-pool.yaml", "process-rpc.yaml"} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join("..", "..", "examples", name)
			definition, err := workflow.NewLoader(nil).Decode(path, workflow.LoadOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if err := buildProcessConfigs(definition.Steps); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func buildProcessConfigs(steps []workflow.Step) error {
	registry := step.NewRegistry()
	if err := Register(registry); err != nil {
		return err
	}
	return buildRegisteredProcessConfigs(registry, steps)
}

func buildRegisteredProcessConfigs(registry *step.Registry, steps []workflow.Step) error {
	for _, workflowStep := range steps {
		if workflowStep.Type == "process" || workflowStep.Type == "process_call" {
			if _, err := registry.Build(workflowStep.Type, workflowStep.With); err != nil {
				return err
			}
		}
		for _, children := range workflowStep.ChildSequences() {
			if err := buildRegisteredProcessConfigs(registry, children.Steps); err != nil {
				return err
			}
		}
	}
	return nil
}
