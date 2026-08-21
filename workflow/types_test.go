package workflow

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestConditionUnmarshalYAML(t *testing.T) {
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
	tests := []struct {
		name      string
		cron      string
		timezone  string
		wantError string
	}{
		{name: "unscheduled"},
		{name: "five fields", cron: "0 9 * * *"},
		{name: "seconds and timezone", cron: "0 0 9 * * *", timezone: "Europe/Warsaw"},
		{name: "timezone without cron", timezone: "UTC", wantError: "timezone requires cron"},
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
