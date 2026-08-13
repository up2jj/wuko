package step

import (
	"strings"
	"testing"
)

func TestCompileTextValidator(t *testing.T) {
	tests := []struct {
		name     string
		required bool
		rules    TextValidation
		value    string
		wantErr  string
	}{
		{name: "valid", rules: TextValidation{MinLength: 2, MaxLength: 4, Pattern: `^\p{L}+$`}, value: "Żół", wantErr: ""},
		{name: "required", required: true, value: " ", wantErr: "a value is required"},
		{name: "optional empty", rules: TextValidation{MinLength: 2}, value: "", wantErr: ""},
		{name: "too short", rules: TextValidation{MinLength: 3}, value: "ab", wantErr: "must be at least 3 characters"},
		{name: "too long", rules: TextValidation{MaxLength: 2}, value: "abc", wantErr: "must be at most 2 characters"},
		{name: "pattern", rules: TextValidation{Pattern: `^[a-z]+$`}, value: "123", wantErr: "has an invalid format"},
		{name: "custom message", rules: TextValidation{MinLength: 3, Message: "Use a longer name"}, value: "ab", wantErr: "Use a longer name"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			validate, err := CompileTextValidator(tt.required, tt.rules)
			if err != nil {
				t.Fatal(err)
			}
			err = validate(tt.value)
			if tt.wantErr == "" && err != nil {
				t.Fatalf("Validate() error = %v", err)
			}
			if tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)) {
				t.Fatalf("Validate() error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestCompileTextValidatorRejectsInvalidRules(t *testing.T) {
	tests := []TextValidation{
		{MinLength: -1},
		{MaxLength: -1},
		{MinLength: 3, MaxLength: 2},
		{Pattern: "["},
	}
	for _, rules := range tests {
		if _, err := CompileTextValidator(false, rules); err == nil {
			t.Fatalf("rules %#v: expected error", rules)
		}
	}
}
