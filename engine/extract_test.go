package engine_test

import (
	"context"
	"io"
	"testing"

	"github.com/up2jj/wuko/engine"
	"github.com/up2jj/wuko/step"
	extractstep "github.com/up2jj/wuko/steps/extract"
	"github.com/up2jj/wuko/workflow"
)

type extractFixtureRunner struct {
	outputs map[string]any
}

func (r extractFixtureRunner) Run(context.Context, step.Request) (step.Result, error) {
	return step.Result{Outputs: r.outputs}, nil
}

func TestExtractRendersConfigurationAndCommitsTypedResults(t *testing.T) {
	registry := newTestRegistry(t, map[string]step.Builder{
		"extract_fixture": func(raw map[string]any) (step.Runner, error) {
			return extractFixtureRunner{outputs: raw}, nil
		},
	})
	if err := extractstep.Register(registry); err != nil {
		t.Fatal(err)
	}
	definition := testDefinition(t, "extract",
		workflow.Step{ID: "build", Type: "extract_fixture", With: map[string]any{"stdout": "Release 1.4.2 build 42"}},
		workflow.Step{ID: "release", Type: "extract", With: map[string]any{
			"from": "{{ .vars.source }}", "format": "{{ .vars.format }}",
			"variables": map[string]any{"build": "build_number"},
		}},
		workflow.Step{ID: "consume", Type: "extract_fixture", If: "steps.release.build == 42 && vars.build_number == 42", With: map[string]any{
			"value": "{{ .steps.release.version }}-{{ .vars.build_number }}",
		}},
	)
	definition.Vars = map[string]any{
		"source": "steps.build.stdout", "format": "Release {version:string} build {build:integer}",
	}
	state, err := engine.New(registry).Run(t.Context(), definition, engine.Options{
		RunDir: t.TempDir(), Stdout: io.Discard, Stderr: io.Discard,
	})
	if err != nil {
		t.Fatal(err)
	}
	release := state.Steps["release"].(map[string]any)
	if release["version"] != "1.4.2" || release["build"] != int64(42) || state.Vars["build_number"] != int64(42) {
		t.Fatalf("release = %#v, variables = %#v", release, state.Vars)
	}
	if state.Steps["consume"].(map[string]any)["value"] != "1.4.2-42" {
		t.Fatalf("consume = %#v", state.Steps["consume"])
	}
}

func TestExtractStaticConfigurationFailsValidation(t *testing.T) {
	registry := newTestRegistry(t, nil)
	if err := extractstep.Register(registry); err != nil {
		t.Fatal(err)
	}
	definition := testDefinition(t, "invalid-extract", workflow.Step{ID: "extract", Type: "extract", With: map[string]any{
		"text": "value", "pattern": "[",
	}})
	if _, err := engine.New(registry).Run(t.Context(), definition, engine.Options{
		RunDir: t.TempDir(), Stdout: io.Discard, Stderr: io.Discard,
	}); err == nil {
		t.Fatal("invalid static pattern passed workflow validation")
	}
}

func TestExtractFieldsReadProducerStdoutAndCommitMarkerOutputs(t *testing.T) {
	registry := newTestRegistry(t, map[string]step.Builder{
		"extract_fixture": func(raw map[string]any) (step.Runner, error) {
			return extractFixtureRunner{outputs: raw}, nil
		},
	})
	if err := extractstep.Register(registry); err != nil {
		t.Fatal(err)
	}
	stdout := "starting\n" +
		`WUKO_OUTPUT_V1 {"event":"begin","key":"action_test_binary"}` + "\n" +
		"binary=dist/action.test\n" +
		`WUKO_OUTPUT_V1 {"event":"end","key":"action_test_binary"}` + "\n" +
		"warning: first\nwarning: second\n"
	definition := testDefinition(t, "extract-fields",
		workflow.Step{ID: "build", Type: "extract_fixture", With: map[string]any{"stdout": stdout}},
		workflow.Step{ID: "build_outputs", Type: "extract", With: map[string]any{
			"from": "steps.build.stdout",
			"fields": map[string]any{
				"binary_path": map[string]any{
					"marker": "{{ .vars.marker }}", "regex": "{{ .vars.binary_regex }}",
				},
				"warnings": map[string]any{
					"regex": `warning: (?P<value>[^\r\n]+)`, "match": "{{ .vars.cardinality }}",
				},
			},
			"variables": map[string]any{"binary_path": "test_binary"},
		}},
		workflow.Step{ID: "consume", Type: "extract_fixture", If: `steps.build_outputs.binary_path == "dist/action.test" && len(steps.build_outputs.warnings) == 2 && vars.test_binary == "dist/action.test"`, With: map[string]any{
			"value": "{{ .steps.build_outputs.binary_path }}",
		}},
	)
	definition.Vars = map[string]any{
		"marker": "action_test_binary", "binary_regex": `binary=(?P<value>[^\r\n]+)`, "cardinality": "all",
	}
	state, err := engine.New(registry).Run(t.Context(), definition, engine.Options{
		RunDir: t.TempDir(), Stdout: io.Discard, Stderr: io.Discard,
	})
	if err != nil {
		t.Fatal(err)
	}
	outputs := state.Steps["build_outputs"].(map[string]any)
	if outputs["binary_path"] != "dist/action.test" || state.Vars["test_binary"] != "dist/action.test" {
		t.Fatalf("outputs = %#v, variables = %#v", outputs, state.Vars)
	}
	warnings := outputs["warnings"].([]any)
	if len(warnings) != 2 || warnings[0] != "first" || warnings[1] != "second" {
		t.Fatalf("warnings = %#v", warnings)
	}
}
