package workflow

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestConditionUnmarshalYAML(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		yaml    string
		want    Condition
		wantErr bool
	}{
		{name: "expression", yaml: "if: vars.enabled\n", want: "vars.enabled"},
		{name: "true", yaml: "if: true\n", want: "true"},
		{name: "false", yaml: "if: false\n", want: "false"},
		{name: "empty", yaml: "if: ''\n", wantErr: true},
		{name: "number", yaml: "if: 1\n", wantErr: true},
		{name: "list", yaml: "if: [true]\n", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var value struct {
				If Condition `yaml:"if"`
			}
			err := yaml.Unmarshal([]byte(tt.yaml), &value)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if value.If != tt.want {
				t.Fatalf("if = %q, want %q", value.If, tt.want)
			}
		})
	}
}

func TestDefinitionScheduleValidation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		cron      string
		timezone  string
		wantError string
	}{
		{name: "unscheduled"},
		{name: "five fields", cron: "0 9 * * *"},
		{name: "seconds and timezone", cron: "0 0 9 * * *", timezone: "Europe/Warsaw"},
		{name: "timezone without cron", timezone: "UTC"},
		{name: "invalid cron", cron: "0 25 * * *", wantError: "invalid cron"},
		{name: "invalid timezone", cron: "0 9 * * *", timezone: "Mars/Olympus", wantError: "invalid timezone"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			definition := &Definition{
				Version: 1, Name: "scheduled", Cron: tt.cron, Timezone: tt.timezone,
				Steps: []Step{{ID: "run", Type: "shell"}},
			}
			err := validateDefinitionHeader(definition)
			if tt.wantError == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("error = %v, want %q", err, tt.wantError)
			}
		})
	}
}

func TestDefinitionInvokableDefaultsAndValidation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		field     string
		want      bool
		wantError string
	}{
		{name: "omitted", want: true},
		{name: "true", field: "invokable: true\n", want: true},
		{name: "false", field: "invokable: false\n"},
		{name: "non boolean", field: "invokable: dependency-only\n", wantError: "cannot unmarshal"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "workflow.yaml")
			data := "version: 1\nname: workflow\n" + tt.field + "steps:\n  - return: {outputs: {}}\n"
			if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
				t.Fatal(err)
			}
			definition, err := NewLoader(nil).Decode(path, LoadOptions{})
			if tt.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantError) {
					t.Fatalf("error = %v, want %q", err, tt.wantError)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := definition.IsInvokable(); got != tt.want {
				t.Fatalf("IsInvokable() = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestDefinitionLifecycleHooksLoadAndValidate(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "workflow.yaml")
	data := `version: 1
name: lifecycle
steps:
  - id: run
    type: shell
    with: {command: true}
install:
  - id: install
    type: shell
    with: {command: true}
uninstall:
  - id: uninstall
    type: shell
    with: {command: true}
`
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	definition, err := NewLoader(nil).Load(t.Context(), path, LoadOptions{RunDir: filepath.Dir(path)})
	if err != nil {
		t.Fatal(err)
	}
	if len(definition.Install) != 1 || definition.Install[0].ID != "install" {
		t.Fatalf("install hook = %#v", definition.Install)
	}
	if len(definition.Uninstall) != 1 || definition.Uninstall[0].ID != "uninstall" {
		t.Fatalf("uninstall hook = %#v", definition.Uninstall)
	}
}

func TestDefinitionLifecycleHookRejectsInvalidStep(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "workflow.yaml")
	data := `version: 1
name: lifecycle
steps:
  - id: run
    type: shell
    with: {command: true}
install:
  - type: shell
    with: {command: true}
`
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := NewLoader(nil).Load(t.Context(), path, LoadOptions{RunDir: filepath.Dir(path)}); err == nil || !strings.Contains(err.Error(), "install") {
		t.Fatalf("error = %v, want install hook validation error", err)
	}
}

func TestDefinitionLifecycleHooksExpandRequiredSteps(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	fragment := filepath.Join(directory, "install.yaml")
	if err := os.WriteFile(fragment, []byte("- id: setup\n  type: shell\n  with: {command: true}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "workflow.yaml")
	data := `version: 1
name: lifecycle
steps:
  - id: run
    type: shell
    with: {command: true}
install:
  - require: install.yaml
`
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	definition, err := NewLoader(nil).Load(t.Context(), path, LoadOptions{RunDir: directory})
	if err != nil {
		t.Fatal(err)
	}
	if len(definition.Install) != 1 || definition.Install[0].ID != "setup" {
		t.Fatalf("install hook = %#v, want expanded setup step", definition.Install)
	}
}
