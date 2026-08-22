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
