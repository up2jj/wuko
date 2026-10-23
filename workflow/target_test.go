package workflow

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTargetSelectionSuggestsCloseMatches(t *testing.T) {
	t.Parallel()
	definition := &Definition{Name: "deploy", Targets: map[string]TargetDefinition{"staging": {}, "production": {}}}
	for _, test := range []struct {
		name string
		want string
	}{
		{name: "stagng", want: `did you mean "staging"?`},
		{name: "prodution", want: `did you mean "production"?`},
		{name: "unrelated-environment"},
	} {
		t.Run(test.name, func(t *testing.T) {
			selected, err := definition.SelectTarget(test.name)
			if selected != nil || err == nil || !strings.Contains(err.Error(), "available: production, staging") {
				t.Fatalf("SelectTarget() = %#v, %v; want rejection with sorted available targets", selected, err)
			}
			if test.want != "" && !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
			if test.want == "" && strings.Contains(err.Error(), "did you mean") {
				t.Fatalf("unexpected weak suggestion: %v", err)
			}
		})
	}
	if selected, err := definition.SelectTarget("staging"); err != nil || selected == nil {
		t.Fatalf("exact target = %#v, %v", selected, err)
	}
}

func TestLoadSelectsTargetAndAppliesOverrides(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	path := filepath.Join(directory, "deploy.yaml")
	data := `version: 1
name: deploy
description: shared description
vars:
  app: web
  environment: shared
env:
  LOG_LEVEL: info
  REGION: default
outputs:
  shared: {type: string, value: steps.run.stdout}
cron: "0 9 * * *"
targets:
  production:
    description: production deployment
    vars:
      environment: production
    env:
      REGION: eu-west-1
    outputs:
      deployed: {type: string, value: steps.run.stdout}
    cron: "0 10 * * *"
    steps:
      - id: run
        type: shell
        with: {script: echo production}
    finally:
      - id: cleanup
        type: shell
        with: {script: echo cleanup}
`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}

	definition, err := NewLoader(nil).Load(t.Context(), path, LoadOptions{
		Target:  "production",
		RunDir:  directory,
		BaseEnv: map[string]string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if definition.HasTargets() {
		t.Fatal("selected definition still declares targets")
	}
	if definition.Description != "production deployment" || definition.Cron != "0 10 * * *" {
		t.Fatalf("selected metadata = %#v", definition)
	}
	if definition.Vars["app"] != "web" || definition.Vars["environment"] != "production" {
		t.Fatalf("vars = %#v", definition.Vars)
	}
	if definition.Env["LOG_LEVEL"] != "info" || definition.Env["REGION"] != "eu-west-1" {
		t.Fatalf("env = %#v", definition.Env)
	}
	if len(definition.Outputs) != 1 || definition.Outputs["deployed"].Type != "string" {
		t.Fatalf("outputs = %#v", definition.Outputs)
	}
	if len(definition.Steps) != 1 || definition.Steps[0].ID != "run" || len(definition.Finally) != 1 {
		t.Fatalf("steps/finally = %#v / %#v", definition.Steps, definition.Finally)
	}
}

func TestTargetSelectionRequiresDeclaredTarget(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		data string
		load LoadOptions
		want string
	}{
		{
			name: "missing target",
			data: `version: 1
name: deploy
targets:
  production:
    steps:
      - id: run
        type: shell
        with: {script: true}
`,
			want: `workflow "deploy" requires a target`,
		},
		{
			name: "unknown target",
			data: `version: 1
name: deploy
targets:
  production:
    steps:
      - id: run
        type: shell
        with: {script: true}
`,
			load: LoadOptions{Target: "staging"},
			want: `workflow "deploy" has no target "staging"`,
		},
		{
			name: "target on legacy workflow",
			data: `version: 1
name: deploy
steps:
  - id: run
    type: shell
    with: {script: true}
`,
			load: LoadOptions{Target: "production"},
			want: `workflow "deploy" does not declare targets`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "workflow.yaml")
			if err := os.WriteFile(path, []byte(tt.data), 0o600); err != nil {
				t.Fatal(err)
			}
			tt.load.RunDir = filepath.Dir(path)
			_, err := NewLoader(nil).Load(t.Context(), path, tt.load)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

func TestTargetWorkflowRejectsLegacyStepsAndInvalidNames(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		data string
		want string
	}{
		{
			name: "legacy steps",
			data: `version: 1
name: deploy
steps:
  - id: run
    type: shell
    with: {script: true}
targets:
  production:
    steps:
      - id: deploy
        type: shell
        with: {script: true}
`,
			want: "steps and finally cannot be combined with targets",
		},
		{
			name: "invalid target name",
			data: `version: 1
name: deploy
targets:
  production-east:
    steps:
      - id: deploy
        type: shell
        with: {script: true}
`,
			want: `invalid target name "production-east"`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "workflow.yaml")
			if err := os.WriteFile(path, []byte(tt.data), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := NewLoader(nil).Load(t.Context(), path, LoadOptions{RunDir: filepath.Dir(path)})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want substring %q", err, tt.want)
			}
		})
	}
}
