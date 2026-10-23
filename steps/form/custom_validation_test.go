package form

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/up2jj/wuko/helper"
	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/tui"
)

func TestFormCustomValidatorsReceiveNormalizedValues(t *testing.T) {
	tests := []struct {
		name, kind string
		value      any
		rules      map[string]any
		extra      map[string]any
		valid      bool
	}{
		{"text expression", "text", "Ada", map[string]any{"expr": "len(value) >= 3"}, nil, true},
		{"text rejection", "text", "Al", map[string]any{"expr": "len(value) >= 3"}, nil, false},
		{"workflow context", "text", "Ada", map[string]any{"expr": "value == vars.expected"}, nil, true},
		{"normalized number", "number", "3", map[string]any{"expr": "value > 2 && value < 4"}, nil, true},
		{"optional number", "number", nil, map[string]any{"expr": "value == nil"}, nil, true},
		{"checkbox", "checkbox", true, map[string]any{"expr": "value"}, nil, true},
		{"choice", "select", 2, map[string]any{"expr": "value == 2"}, map[string]any{"choices": []any{map[string]any{"label": "Two", "value": 2}}}, true},
		{"multiple", "multiselect", []any{1}, map[string]any{"expr": "len(value) == 1"}, map[string]any{"choices": []any{map[string]any{"label": "One", "value": 1}}}, true},
		{"Lua function", "text", "Ada", map[string]any{"lua": `function(value) return #value >= 3 end`}, nil, true},
		{"Lua rejection", "text", "Al", map[string]any{"lua": `function(value) return #value >= 3 end`}, nil, false},
		{"Lua context", "text", "Ada", map[string]any{"lua": `function(value, context) return value == context.vars.expected end`}, nil, true},
		{"Lua normalized number", "number", "3", map[string]any{"lua": `function(value) return type(value) == "number" and value == 3 end`}, nil, true},
		{"Lua optional number", "number", nil, map[string]any{"lua": `function(value) return value == nil end`}, nil, true},
		{"path validation", "path", "", map[string]any{"expr": "value == ''"}, nil, true},
		{"multiline validation", "textarea", "one\ntwo", map[string]any{"lua": `function(value) return string.find(value, "\n") ~= nil end`}, nil, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			field := fieldFor(test.kind, test.value)
			test.rules["message"] = "Use an allowed value"
			field["validation"] = test.rules
			for key, value := range test.extra {
				field[key] = value
			}
			result, err := buildForm(t, configFor(field)).Run(t.Context(), step.Request{RunDir: t.TempDir(), Vars: map[string]any{"expected": "Ada"}})
			if test.valid {
				if err != nil {
					t.Fatal(err)
				}
				if len(result.Variables) > 0 {
					t.Fatal("validator wrote variables")
				}
			} else if err == nil || !strings.Contains(err.Error(), "Use an allowed value") || len(result.Outputs) > 0 {
				t.Fatalf("result=%#v error=%v", result, err)
			}
		})
	}
}

func TestFormRejectsInvalidCustomValidation(t *testing.T) {
	for _, rules := range []map[string]any{
		{"expr": "true", "lua": "function(value) return true end"},
		{"expr": " "},
		{"lua": " "},
		{"expr": "value >"},
		{"expr": "'not boolean'"},
		{"lua": "function(value) return"},
	} {
		field := fieldFor("text", "initial")
		field["validation"] = rules
		if _, err := New(configFor(field)); err == nil {
			t.Fatalf("accepted rules=%#v", rules)
		}
	}
}

func TestFormCustomValidationDoesNotBypassBuiltInRules(t *testing.T) {
	field := fieldFor("text", "short")
	field["validation"] = map[string]any{"min_length": 10, "expr": "true"}
	if _, err := buildForm(t, configFor(field)).Run(t.Context(), step.Request{}); err == nil {
		t.Fatal("custom rule bypassed min_length")
	}
}

func TestFormCustomValidationErrorsDoNotExposePasswords(t *testing.T) {
	for _, rules := range []map[string]any{
		{"lua": `function(value) error(value) end`},
		{"lua": `function(value) return value end`},
		{"expr": "int(value) > 0"},
	} {
		field := fieldFor("password", "private-input")
		field["validation"] = rules
		result, err := buildForm(t, configFor(field)).Run(t.Context(), step.Request{})
		if err == nil || strings.Contains(err.Error(), "private-input") || len(result.Outputs) > 0 {
			t.Fatalf("result=%#v error=%v", result, err)
		}
	}
}

func TestFormInteractiveCustomValidationCanBeCorrected(t *testing.T) {
	field := fieldFor("text", "short")
	field["validation"] = map[string]any{"expr": "len(value) >= 8", "message": "Use eight characters"}
	runner := buildForm(t, configFor(field))
	runner.read = func(_ context.Context, _ step.Request, config tui.FormConfig) (map[string]any, error) {
		validator := config.Fields[0].Validate
		if _, err := validator("short", map[string]any{"answer": "short"}); err == nil || err.Error() != "Use eight characters" {
			t.Fatalf("error=%v", err)
		}
		if _, err := validator("corrected", map[string]any{"answer": "corrected"}); err != nil {
			t.Fatal(err)
		}
		return map[string]any{"answer": "corrected"}, nil
	}
	result, err := runner.Run(t.Context(), step.Request{Interactive: true})
	if err != nil || result.Outputs["answer"] != "corrected" {
		t.Fatalf("result=%#v error=%v", result, err)
	}
}

func TestFormExpressionValidatorUsesCancelableHelpers(t *testing.T) {
	field := fieldFor("text", "Ada")
	field["validation"] = map[string]any{"expr": "custom_name(value)"}
	ctx, cancel := context.WithCancel(t.Context())
	request := step.Request{Helpers: helper.Set{"custom_name": func(callContext context.Context, args []any) (any, error) {
		if callContext != ctx {
			t.Fatal("helper did not receive form context")
		}
		cancel()
		return nil, callContext.Err()
	}}}
	result, err := buildForm(t, configFor(field)).Run(ctx, request)
	if !errors.Is(err, context.Canceled) || len(result.Outputs) > 0 {
		t.Fatalf("result=%#v error=%v", result, err)
	}
}
