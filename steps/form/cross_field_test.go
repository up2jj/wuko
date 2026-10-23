package form

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/up2jj/wuko/helper"
	"github.com/up2jj/wuko/internal/steptest"
	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/tui"
)

func TestFormValidatorsSeeAllTypedFieldsRegardlessOfOrder(t *testing.T) {
	for _, kind := range []string{"expr", "lua"} {
		for _, reverse := range []bool{false, true} {
			t.Run(kind+map[bool]string{true: "/reverse", false: "/forward"}[reverse], func(t *testing.T) {
				rules := map[string]any{"expr": "value >= fields.minimum && fields.enabled && len(fields.regions) == 1 && fields.label == 'ready' && fields.notes == 'one\\ntwo'"}
				if kind == "lua" {
					rules = map[string]any{"lua": `function(value, context)
    return value >= context.fields.minimum and context.fields.enabled
      and #context.fields.regions == 1 and context.fields.label == "ready"
      and context.fields.notes == "one\ntwo"
   end`}
				}
				fields := []map[string]any{
					{"id": "maximum", "type": "number", "label": "Maximum", "value": "9", "validation": rules},
					{"id": "minimum", "type": "number", "label": "Minimum", "value": "4"},
					{"id": "enabled", "type": "checkbox", "label": "Enabled", "value": true},
					{"id": "regions", "type": "multiselect", "label": "Regions", "value": []any{"west"}, "choices": []any{map[string]any{"label": "West", "value": "west"}}},
					{"id": "label", "type": "select", "label": "Label", "value": "ready", "choices": []any{map[string]any{"label": "Ready", "value": "ready"}}},
					{"id": "notes", "type": "textarea", "label": "Notes", "value": "one\ntwo"},
				}
				if reverse {
					for i, j := 0, len(fields)-1; i < j; i, j = i+1, j-1 {
						fields[i], fields[j] = fields[j], fields[i]
					}
				}
				result, err := steptest.BuildAs[*Runner](t, New, configFor(fields...)).Run(t.Context(), step.Request{})
				if err != nil {
					t.Fatal(err)
				}
				if result.Outputs["maximum"] != int64(9) || len(result.Variables) > 0 {
					t.Fatalf("result=%#v", result)
				}
			})
		}
	}
}

func TestFormCrossFieldRejectionPublishesNothing(t *testing.T) {
	for _, kind := range []string{"expr", "lua"} {
		t.Run(kind, func(t *testing.T) {
			rules := map[string]any{"expr": "value == fields.password", "message": "Passwords must match"}
			if kind == "lua" {
				rules = map[string]any{"lua": "function(value, context) return value == context.fields.password end", "message": "Passwords must match"}
			}
			runner := steptest.BuildAs[*Runner](t, New, configFor(
				map[string]any{"id": "confirmation", "type": "password", "label": "Confirm", "value": "different", "validation": rules},
				map[string]any{"id": "password", "type": "password", "label": "Password", "value": "private-input"},
			))
			result, err := runner.Run(t.Context(), step.Request{})
			if err == nil || !strings.Contains(err.Error(), "Passwords must match") || strings.Contains(err.Error(), "private-input") || len(result.Outputs) > 0 || len(result.Variables) > 0 {
				t.Fatalf("result=%#v error=%v", result, err)
			}
		})
	}
}

func TestFormCrossFieldValidationUsesSubmittedEdits(t *testing.T) {
	for _, kind := range []string{"expr", "lua"} {
		t.Run(kind, func(t *testing.T) {
			rules := map[string]any{"expr": "value == fields.password", "message": "Passwords must match"}
			if kind == "lua" {
				rules = map[string]any{"lua": "function(value, context) return value == context.fields.password end", "message": "Passwords must match"}
			}
			runner := steptest.BuildAs[*Runner](t, New, configFor(
				map[string]any{"id": "password", "type": "password", "label": "Password", "default": "old"},
				map[string]any{"id": "confirmation", "type": "password", "label": "Confirm", "default": "old", "validation": rules},
			))
			runner.read = func(_ context.Context, _ step.Request, config tui.FormConfig) (map[string]any, error) {
				values := map[string]any{"password": "new", "confirmation": "old"}
				draft := tui.FormDraft(config.Fields, values)
				if _, err := config.Fields[1].Validate(values["confirmation"], draft); err == nil {
					t.Fatal("validator used the initial password")
				}
				values["confirmation"] = "new"
				draft = tui.FormDraft(config.Fields, values)
				if _, err := config.Fields[1].Validate(values["confirmation"], draft); err != nil {
					t.Fatal(err)
				}
				return values, nil
			}
			result, err := runner.Run(t.Context(), step.Request{Interactive: true})
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(result.Outputs, map[string]any{"password": "new", "confirmation": "new"}) {
				t.Fatalf("outputs=%#v", result.Outputs)
			}
		})
	}
}

func TestFormDraftNormalizesNumbersWithoutRequiringValidBounds(t *testing.T) {
	runner := steptest.BuildAs[*Runner](t, New, configFor(
		map[string]any{"id": "limit", "type": "number", "label": "Limit", "default": 10, "validation": map[string]any{"min": 5}},
		map[string]any{"id": "amount", "type": "number", "label": "Amount", "default": 20, "validation": map[string]any{"expr": "fields.limit == nil || value > fields.limit"}},
	))
	runner.read = func(_ context.Context, _ step.Request, config tui.FormConfig) (map[string]any, error) {
		raw := map[string]any{"limit": "2", "amount": "20"}
		draft := tui.FormDraft(config.Fields, raw)
		if draft["limit"] != int64(2) {
			t.Fatalf("out-of-bounds number lost its type: %#v", draft)
		}
		raw["limit"] = "invalid"
		draft = tui.FormDraft(config.Fields, raw)
		if draft["limit"] != nil {
			t.Fatalf("invalid number=%#v", draft["limit"])
		}
		if _, err := config.Fields[0].Validate(raw["limit"], draft); err == nil {
			t.Fatal("invalid input was accepted as an optional null")
		}
		if _, err := config.Fields[1].Validate(raw["amount"], draft); err != nil {
			t.Fatal(err)
		}
		return map[string]any{"limit": "10", "amount": "20"}, nil
	}
	if _, err := runner.Run(t.Context(), step.Request{Interactive: true}); err != nil {
		t.Fatal(err)
	}
}

func TestFormRejectsUnknownFieldReferences(t *testing.T) {
	field := fieldFor("text", "initial")
	field["validation"] = map[string]any{"expr": "value == fields.typo"}
	if _, err := New(configFor(field)); err == nil || !strings.Contains(err.Error(), "typo") {
		t.Fatalf("error=%v", err)
	}
}

func TestFormValidatorsCannotMutateSharedDraft(t *testing.T) {
	field := fieldFor("text", "initial")
	field["validation"] = map[string]any{"expr": "mutate_draft(fields)"}
	request := step.Request{Helpers: helper.Set{"mutate_draft": func(_ context.Context, args []any) (any, error) {
		fields := args[0].(map[string]any)
		fields["other"] = false
		return true, nil
	}}}
	runner := steptest.BuildAs[*Runner](t, New, configFor(field, map[string]any{"id": "other", "type": "checkbox", "label": "Other", "value": true, "validation": map[string]any{"expr": "fields.other"}}))
	result, err := runner.Run(t.Context(), request)
	if err != nil || result.Outputs["other"] != true {
		t.Fatalf("result=%#v error=%v", result, err)
	}
}

func TestFormCrossFieldValidationErrorsDoNotExposeSiblingPassword(t *testing.T) {
	for _, rules := range []map[string]any{
		{"expr": "int(fields.password) > 0"},
		{"lua": "function(value, context) error(context.fields.password) end"},
	} {
		runner := steptest.BuildAs[*Runner](t, New, configFor(
			map[string]any{"id": "answer", "type": "text", "label": "Answer", "value": "Ada", "validation": rules},
			map[string]any{"id": "password", "type": "password", "label": "Password", "value": "private-sibling-value"},
		))
		result, err := runner.Run(t.Context(), step.Request{})
		if err == nil || strings.Contains(err.Error(), "private-sibling-value") || len(result.Outputs) > 0 {
			t.Fatalf("result=%#v error=%v", result, err)
		}
	}
}
