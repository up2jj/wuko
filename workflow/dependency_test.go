package workflow

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestLoadWorkflowDependenciesAndTypedOutputs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "release.yaml")
	data := `version: 1
name: release
depends_on:
  build: build-artifacts
outputs:
  artifact:
    type: string
    description: Release artifact
    value: dependencies.build.artifact
steps:
  - return:
      outputs:
        artifact: dependencies.build.artifact
`
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	definition, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if definition.DependsOn["build"] != "build-artifacts" || definition.Outputs["artifact"].Type != "string" {
		t.Fatalf("definition = %#v", definition)
	}
}

func TestWorkflowDependencyAndOutputValidation(t *testing.T) {
	tests := []struct {
		name    string
		depends map[string]string
		outputs map[string]WorkflowOutput
		want    string
	}{
		{name: "alias", depends: map[string]string{"bad-alias": "build"}, want: "invalid dependency alias"},
		{name: "workflow name", depends: map[string]string{"build": "dir/build"}, want: "invalid workflow name"},
		{name: "output name", outputs: map[string]WorkflowOutput{"bad-name": {Type: "string", Value: `"x"`}}, want: "invalid output name"},
		{name: "output type", outputs: map[string]WorkflowOutput{"value": {Type: "integer", Value: "1"}}, want: "unsupported type"},
		{name: "output value", outputs: map[string]WorkflowOutput{"value": {Type: "number"}}, want: "value is required"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			definition := &Definition{Version: 1, Name: "root", DependsOn: test.depends, Outputs: test.outputs, Steps: []Step{{Return: &ReturnControl{Outputs: map[string]string{}}}}}
			err := definition.ValidateStructure()
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestResolveDependencyPlanOrdersChainsAndDeduplicatesDiamonds(t *testing.T) {
	base := dependencyDefinition("base", "/base.yaml")
	left := dependencyDefinition("left", "/left.yaml")
	left.DependsOn = map[string]string{"base": "base"}
	right := dependencyDefinition("right", "/right.yaml")
	right.DependsOn = map[string]string{"base": "base"}
	root := dependencyDefinition("root", "/root.yaml")
	root.DependsOn = map[string]string{"right": "right", "left": "left"}
	definitions := map[string]*Definition{"base": base, "left": left, "right": right}

	plan, err := ResolveDependencyPlan(t.Context(), root, func(_ context.Context, name string) (*Definition, error) {
		return definitions[name], nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, node := range plan.Order {
		names = append(names, node.Definition.Name)
	}
	if want := []string{"base", "left", "right", "root"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("order = %v, want %v", names, want)
	}
	if plan.Root.Dependencies["left"].Dependencies["base"] != plan.Root.Dependencies["right"].Dependencies["base"] {
		t.Fatal("shared dependency was not deduplicated")
	}
}

func TestResolveDependencyPlanRejectsCyclesAndUnknownOutputs(t *testing.T) {
	a := dependencyDefinition("a", "/a.yaml")
	b := dependencyDefinition("b", "/b.yaml")
	a.DependsOn = map[string]string{"b": "b"}
	b.DependsOn = map[string]string{"a": "a"}
	_, err := ResolveDependencyPlan(t.Context(), a, func(_ context.Context, name string) (*Definition, error) {
		return map[string]*Definition{"a": a, "b": b}[name], nil
	})
	if err == nil || !strings.Contains(err.Error(), "a -> b -> a") {
		t.Fatalf("cycle error = %v", err)
	}

	producer := dependencyDefinition("producer", "/producer.yaml")
	consumer := dependencyDefinition("consumer", "/consumer.yaml")
	consumer.DependsOn = map[string]string{"build": "producer"}
	consumer.Steps = []Step{{Return: &ReturnControl{Outputs: map[string]string{"value": "dependencies.build.missing"}}}}
	_, err = ResolveDependencyPlan(t.Context(), consumer, func(context.Context, string) (*Definition, error) { return producer, nil })
	if err == nil || !strings.Contains(err.Error(), `does not declare output "missing"`) {
		t.Fatalf("output error = %v", err)
	}
}

func TestResolveDependencyPlanChecksOnlySemanticDependencyReferences(t *testing.T) {
	producer := dependencyDefinition("producer", "/producer.yaml")
	consumer := dependencyDefinition("consumer", "/consumer.yaml")
	consumer.DependsOn = map[string]string{"build": "producer"}
	consumer.Description = "literal dependencies.build.missing"
	consumer.Steps = []Step{{
		ID: "literal", Type: "shell",
		With: map[string]any{"command": "echo", "args": []any{"dependencies.build.missing"}},
	}}
	consumer.Outputs = map[string]WorkflowOutput{
		"label": {Type: "string", Value: `"dependencies.build.missing"`},
	}

	if _, err := ResolveDependencyPlan(t.Context(), consumer, func(context.Context, string) (*Definition, error) { return producer, nil }); err != nil {
		t.Fatalf("literal dependency text rejected: %v", err)
	}

	consumer.Steps[0].With["args"] = []any{"{{ .dependencies.build.missing }}"}
	_, err := ResolveDependencyPlan(t.Context(), consumer, func(context.Context, string) (*Definition, error) { return producer, nil })
	if err == nil || !strings.Contains(err.Error(), `does not declare output "missing"`) {
		t.Fatalf("template reference error = %v", err)
	}

	consumer.Steps[0].With["args"] = nil
	consumer.Outputs["label"] = WorkflowOutput{Type: "string", Value: `dependencies["build"]["missing"]`}
	_, err = ResolveDependencyPlan(t.Context(), consumer, func(context.Context, string) (*Definition, error) { return producer, nil })
	if err == nil || !strings.Contains(err.Error(), `does not declare output "missing"`) {
		t.Fatalf("bracket reference error = %v", err)
	}
}

func TestLoaderRejectsDependencyOutputsInLoadTimeFields(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{name: "environment", body: "env:\n  TOKEN: '{{ .dependencies.auth.token }}'\nsteps:\n  - return: {outputs: {}}\n", want: "available only at runtime"},
		{name: "remote action source", body: "steps:\n  - id: call\n    uses: https://example.test/{{ .dependencies.build.version }}\n", want: "action sources are resolved while loading"},
		{name: "local action source", body: "steps:\n  - id: call\n    uses: ./actions/{{ .dependencies.build.version }}\n", want: "action sources are resolved while loading"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "workflow.yaml")
			data := "version: 1\nname: root\ndepends_on: {build: build}\n" + test.body
			if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
				t.Fatal(err)
			}
			_, err := NewLoader(nil).Load(t.Context(), path, LoadOptions{RunDir: t.TempDir()})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestLoaderAcceptsLiteralDependencyTextInLoadTimeFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workflow.yaml")
	data := `version: 1
name: root
env:
  HOST: dependencies.internal
steps:
  - return: {outputs: {}}
`
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	definition, err := NewLoader(nil).Load(t.Context(), path, LoadOptions{RunDir: t.TempDir(), BaseEnv: map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
	if definition.Env["HOST"] != "dependencies.internal" {
		t.Fatalf("environment = %#v", definition.Env)
	}
	if dependencyReference("https://dependencies.example.com/action.yaml") {
		t.Fatal("literal action URL was treated as a dependency reference")
	}
}

func dependencyDefinition(name, path string) *Definition {
	return &Definition{Version: 1, Name: name, Path: path, Steps: []Step{{Return: &ReturnControl{Outputs: map[string]string{}}}}}
}
