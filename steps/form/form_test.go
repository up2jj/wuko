package form

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/up2jj/wuko/internal/steptest"
	"github.com/up2jj/wuko/internal/testfs"
	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/tui"
)

func configFor(fields ...map[string]any) map[string]any {
	items := make([]any, len(fields))
	for i, field := range fields {
		items[i] = field
	}
	return map[string]any{"title": "Settings", "fields": items}
}

func fieldFor(kind string, value any) map[string]any {
	return map[string]any{"id": "answer", "type": kind, "label": "Answer", "value": value}
}

func TestFormRejectsInvalidConfiguration(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(map[string]any)
		want   string
	}{
		{"missing title", func(c map[string]any) { delete(c, "title") }, "title is required"},
		{"empty fields", func(c map[string]any) { c["fields"] = []any{} }, "fields must not be empty"},
		{"unknown top field", func(c map[string]any) { c["variable"] = "oops" }, "field variable not found"},
		{"missing id", func(c map[string]any) { delete(c["fields"].([]any)[0].(map[string]any), "id") }, "literal identifier"},
		{"templated id", func(c map[string]any) { c["fields"].([]any)[0].(map[string]any)["id"] = "{{ .vars.name }}" }, "literal identifier"},
		{"duplicate id", func(c map[string]any) { c["fields"] = append(c["fields"].([]any), fieldFor("text", "two")) }, "duplicate field id"},
		{"missing label", func(c map[string]any) { delete(c["fields"].([]any)[0].(map[string]any), "label") }, "label is required"},
		{"unknown widget", func(c map[string]any) { c["fields"].([]any)[0].(map[string]any)["type"] = "calendar" }, "unsupported type"},
		{"wrong option", func(c map[string]any) { c["fields"].([]any)[0].(map[string]any)["root"] = "." }, "not supported"},
		{"bad text pattern", func(c map[string]any) {
			c["fields"].([]any)[0].(map[string]any)["validation"] = map[string]any{"pattern": "["}
		}, "validation pattern"},
		{"empty expression", func(c map[string]any) { c["fields"].([]any)[0].(map[string]any)["value"] = map[string]any{"expr": ""} }, "non-empty"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			raw := configFor(fieldFor("text", "hello"))
			test.mutate(raw)
			_, err := New(raw)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("New error=%v, want %q", err, test.want)
			}
		})
	}
}

func TestFormTypedOutputsDoNotWriteVariables(t *testing.T) {
	root := t.TempDir()
	testfs.WriteFile(t, filepath.Join(root, "app.yaml"), []byte("name: app"), 0o600)
	fields := []map[string]any{
		{"id": "name", "type": "text", "label": "Name", "value": "Ada"},
		{"id": "notes", "type": "textarea", "label": "Notes", "value": "one\ntwo\n"},
		{"id": "credential", "type": "password", "label": "Credential", "value": "private"},
		{"id": "notify", "type": "checkbox", "label": "Notify", "value": false},
		{"id": "region", "type": "select", "label": "Region", "value": 2, "choices": []any{map[string]any{"label": "Two", "value": 2}}},
		{"id": "regions", "type": "multiselect", "label": "Regions", "value": []any{false, "west"}, "choices": []any{map[string]any{"label": "West", "value": "west"}, map[string]any{"label": "No", "value": false}}},
		{"id": "replicas", "type": "number", "label": "Replicas", "value": 3, "validation": map[string]any{"integer": true, "min": 1, "max": 10}},
		{"id": "ratio", "type": "number", "label": "Ratio", "value": "1.25"},
		{"id": "manifest", "type": "path", "label": "Manifest", "value": "app.yaml", "patterns": []any{"*.yaml"}},
		{"id": "files", "type": "path", "label": "Files", "multiple": true, "value": []any{"app.yaml"}},
	}
	runner := steptest.BuildAs[*Runner](t, New, configFor(fields...))
	request := step.Request{RunDir: root, Vars: map[string]any{"name": "untouched"}}
	result, err := runner.Run(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"name": "Ada", "notes": "one\ntwo\n", "credential": "private", "notify": false, "region": 2, "regions": []any{false, "west"}, "replicas": int64(3), "ratio": 1.25, "manifest": "app.yaml", "files": []any{"app.yaml"}}
	if !reflect.DeepEqual(result.Outputs, want) {
		t.Fatalf("outputs=%#v, want %#v", result.Outputs, want)
	}
	if len(result.Variables) != 0 || request.Vars["name"] != "untouched" {
		t.Fatalf("variables were changed: %#v %#v", result.Variables, request.Vars)
	}
	if err := step.ValidateOutputKeys(outputSchema(configFor(fields...)), result.Outputs); err != nil {
		t.Fatal(err)
	}
}

func TestFormUnattendedRequiresEveryExplicitValue(t *testing.T) {
	raw := configFor(
		map[string]any{"id": "name", "type": "text", "label": "Name", "default": "Ada"},
		map[string]any{"id": "notify", "type": "checkbox", "label": "Notify"},
	)
	runner := steptest.BuildAs[*Runner](t, New, raw)
	_, err := runner.Run(t.Context(), step.Request{Vars: map[string]any{"name": "supplied", "notify": true}})
	if err == nil || !strings.Contains(err.Error(), "name, notify") {
		t.Fatalf("error=%v", err)
	}
}

func TestFormInteractiveValuesAndDefaultsRemainEditable(t *testing.T) {
	raw := configFor(
		map[string]any{"id": "name", "type": "text", "label": "Name", "default": "fallback", "value": map[string]any{"expr": "vars.person"}},
		map[string]any{"id": "notes", "type": "textarea", "label": "Notes", "default": "initial"},
		map[string]any{"id": "notify", "type": "checkbox", "label": "Notify"},
	)
	runner := steptest.BuildAs[*Runner](t, New, raw)
	calls := 0
	runner.read = func(_ context.Context, _ step.Request, config tui.FormConfig) (map[string]any, error) {
		calls++
		if config.Fields[0].Value != "Ada" || config.Fields[1].Value != "initial" || config.Fields[2].Value != false {
			t.Fatalf("prefill=%#v", config.Fields)
		}
		return map[string]any{"name": "Grace", "notes": "edited\nnotes", "notify": true}, nil
	}
	result, err := runner.Run(t.Context(), step.Request{Interactive: true, Vars: map[string]any{"person": "Ada", "notes": "must not be read"}})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || result.Outputs["name"] != "Grace" || len(result.Variables) != 0 {
		t.Fatalf("result=%#v calls=%d", result, calls)
	}
}

func TestFormNilExpressionDefaultsUseZeroValue(t *testing.T) {
	raw := configFor(
		map[string]any{"id": "name", "type": "text", "label": "Name", "default": map[string]any{"expr": "vars.missing"}},
		map[string]any{"id": "notify", "type": "checkbox", "label": "Notify", "default": map[string]any{"expr": "vars.missing"}},
		map[string]any{"id": "tags", "type": "multiselect", "label": "Tags", "choices": []any{map[string]any{"label": "A", "value": "a"}}, "default": map[string]any{"expr": "vars.missing"}},
	)
	runner := steptest.BuildAs[*Runner](t, New, raw)
	runner.read = func(_ context.Context, _ step.Request, config tui.FormConfig) (map[string]any, error) {
		if config.Fields[0].Value != "" || config.Fields[1].Value != false || !reflect.DeepEqual(config.Fields[2].Value, []any{}) {
			t.Fatalf("prefill=%#v", config.Fields)
		}
		return map[string]any{"name": "Ada", "notify": false, "tags": []any{}}, nil
	}
	if _, err := runner.Run(t.Context(), step.Request{Interactive: true}); err != nil {
		t.Fatal(err)
	}
}

func TestFormOptionalValues(t *testing.T) {
	fields := []map[string]any{
		fieldFor("text", ""),
		{"id": "number", "type": "number", "label": "Number", "value": nil},
		{"id": "selection", "type": "select", "label": "Selection", "value": nil, "choices": []any{map[string]any{"label": "One", "value": 1}}},
		{"id": "multi", "type": "multiselect", "label": "Multi", "value": []any{}, "choices": []any{map[string]any{"label": "One", "value": 1}}},
	}
	result, err := steptest.BuildAs[*Runner](t, New, configFor(fields...)).Run(t.Context(), step.Request{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outputs["answer"] != "" || result.Outputs["number"] != nil || result.Outputs["selection"] != nil || !reflect.DeepEqual(result.Outputs["multi"], []any{}) {
		t.Fatalf("outputs=%#v", result.Outputs)
	}
}

func TestFormRejectsInvalidValuesAtomically(t *testing.T) {
	tests := []struct {
		name, kind string
		value      any
		extra      map[string]any
	}{
		{"required text", "text", " ", map[string]any{"required": true}},
		{"text type", "text", 4, nil},
		{"unicode length", "text", "ą", map[string]any{"validation": map[string]any{"min_length": 2}}},
		{"required checkbox", "checkbox", false, map[string]any{"required": true}},
		{"boolean type", "checkbox", "true", nil},
		{"not a number", "number", "NaN", nil},
		{"overflow", "number", "1e400", nil},
		{"trailing number", "number", "2 3", nil},
		{"fractional", "number", 1.5, map[string]any{"validation": map[string]any{"integer": true}}},
		{"numeric bound", "number", 20, map[string]any{"validation": map[string]any{"max": 10}}},
		{"unknown selection", "select", "missing", map[string]any{"choices": []any{map[string]any{"label": "One", "value": "one"}}}},
		{"disabled selection", "select", "one", map[string]any{"choices": []any{map[string]any{"label": "One", "value": "one", "disabled": true, "reason": "Unavailable"}}}},
		{"duplicate selection", "multiselect", []any{1, 1}, map[string]any{"choices": []any{map[string]any{"label": "One", "value": 1}}}},
		{"selection minimum", "multiselect", []any{}, map[string]any{"choices": []any{map[string]any{"label": "One", "value": 1}}, "min_selected": 1}},
		{"path escape", "path", "../outside", nil},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			field := fieldFor(test.kind, test.value)
			for key, value := range test.extra {
				field[key] = value
			}
			runner := steptest.BuildAs[*Runner](t, New, configFor(field, map[string]any{"id": "valid", "type": "text", "label": "Valid", "value": "ok"}))
			result, err := runner.Run(t.Context(), step.Request{RunDir: t.TempDir()})
			if err == nil || !strings.Contains(err.Error(), "answer") || len(result.Outputs) != 0 || len(result.Variables) != 0 {
				t.Fatalf("result=%#v error=%v", result, err)
			}
		})
	}
}

func TestFormDynamicChoicesAreFrozenAndRevalidated(t *testing.T) {
	raw := configFor(map[string]any{"id": "region", "type": "select", "label": "Region", "from": "steps.fetch.value", "label_field": "name", "value_field": "id", "default": 2})
	request := step.Request{Interactive: true, Steps: map[string]any{"fetch": map[string]any{"value": []any{map[string]any{"name": "West", "id": 2}}}}}
	runner := steptest.BuildAs[*Runner](t, New, raw)
	runner.read = func(_ context.Context, _ step.Request, config tui.FormConfig) (map[string]any, error) {
		if len(config.Fields[0].Options) != 1 || config.Fields[0].Options[0].Label != "West" {
			t.Fatalf("options=%#v", config.Fields[0].Options)
		}
		request.Steps["fetch"] = map[string]any{"value": []any{}}
		return map[string]any{"region": 2}, nil
	}
	if _, err := runner.Run(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	runner.read = func(context.Context, step.Request, tui.FormConfig) (map[string]any, error) {
		return map[string]any{"region": "injected"}, nil
	}
	request.Steps["fetch"] = map[string]any{"value": []any{map[string]any{"name": "West", "id": 2}}}
	if result, err := runner.Run(t.Context(), request); err == nil || len(result.Outputs) > 0 {
		t.Fatalf("result=%#v error=%v", result, err)
	}
}

func TestFormCancellationPublishesNothing(t *testing.T) {
	runner := steptest.BuildAs[*Runner](t, New, configFor(fieldFor("text", "initial")))
	runner.read = func(context.Context, step.Request, tui.FormConfig) (map[string]any, error) {
		return map[string]any{"answer": "partial"}, context.Canceled
	}
	result, err := runner.Run(t.Context(), step.Request{Interactive: true})
	if !errors.Is(err, context.Canceled) || len(result.Outputs) > 0 || len(result.Variables) > 0 {
		t.Fatalf("result=%#v error=%v", result, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := runner.Run(ctx, step.Request{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v", err)
	}
}

func TestFormPathRejectsSymlinkOutsideRoot(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "private.yaml")
	testfs.WriteFile(t, outside, []byte("private"), 0o600)
	if err := os.Symlink(outside, filepath.Join(root, "link.yaml")); err != nil {
		t.Fatal(err)
	}
	result, err := steptest.BuildAs[*Runner](t, New, configFor(fieldFor("path", "link.yaml"))).Run(t.Context(), step.Request{RunDir: root})
	if err == nil || !strings.Contains(err.Error(), "outside root") || len(result.Outputs) > 0 {
		t.Fatalf("result=%#v error=%v", result, err)
	}
}

func TestFormRequiredMultiselectRejectsImpossibleMaximum(t *testing.T) {
	field := fieldFor("multiselect", []any{})
	field["choices"] = []any{map[string]any{"label": "One", "value": 1}}
	field["required"] = true
	field["max_selected"] = 0
	if _, err := New(configFor(field)); err == nil {
		t.Fatal("required multiselect accepted a zero maximum")
	}
}
