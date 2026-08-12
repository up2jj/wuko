package workflow

import (
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
