package form

import (
	"net/url"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestResolveDynamicChoicesAndSubmitTypedValue(t *testing.T) {
	node := decodeFormNode(t, `
title: Select a pod
fields:
  - variable: pod
    label: Pod
    type: string
    required: true
    from: data.pods
    label_field: name
    value_field: uid
    description_field: status
`)
	definition, err := Decode(node, map[string]any{"pod": nil})
	if err != nil {
		t.Fatal(err)
	}
	fields, err := definition.Resolve(map[string]any{"pod": nil}, map[string]any{"pods": []any{
		map[string]any{"name": "api-123", "uid": "pod-a", "status": "Running"},
		map[string]any{"name": "api-456", "uid": "pod-b", "status": "Pending"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(fields[0].Options) != 2 || fields[0].Options[1].Description != "Pending" {
		t.Fatalf("options = %#v", fields[0].Options)
	}
	values, fieldErrors := Submit(fields, url.Values{"field_0": {"1"}})
	if len(fieldErrors) != 0 {
		t.Fatalf("errors = %#v", fieldErrors)
	}
	if values["pod"] != "pod-b" {
		t.Fatalf("pod = %#v", values["pod"])
	}
}

func TestSecretKeepsExistingValueWithoutRenderingItAsSubmission(t *testing.T) {
	node := decodeFormNode(t, `
title: Credentials
fields:
  - variable: token
    label: Token
    type: string
    required: true
    secret: true
`)
	definition, err := Decode(node, map[string]any{"token": nil})
	if err != nil {
		t.Fatal(err)
	}
	fields, err := definition.Resolve(map[string]any{"token": "existing-secret"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	values, fieldErrors := Submit(fields, url.Values{"field_0": {""}})
	if len(fieldErrors) != 0 || values["token"] != "existing-secret" {
		t.Fatalf("values = %#v, errors = %#v", values, fieldErrors)
	}
}

func TestRejectsStepChoiceSource(t *testing.T) {
	node := decodeFormNode(t, `
title: Invalid
fields:
  - variable: pod
    label: Pod
    from: steps.pods.values
`)
	_, err := Decode(node, map[string]any{"pod": nil})
	if err == nil || !strings.Contains(err.Error(), "vars. or data.") {
		t.Fatalf("error = %v", err)
	}
}

func TestArrayChoiceSubmissionPreservesTypedOrder(t *testing.T) {
	node := decodeFormNode(t, `
title: Targets
fields:
  - variable: targets
    label: Targets
    type: array
    choices:
      - {label: One, value: 1}
      - {label: Two, value: 2}
`)
	definition, err := Decode(node, map[string]any{"targets": []any{}})
	if err != nil {
		t.Fatal(err)
	}
	fields, err := definition.Resolve(map[string]any{"targets": []any{}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	values, fieldErrors := Submit(fields, url.Values{"field_0": {"1", "0"}})
	if len(fieldErrors) != 0 || !reflect.DeepEqual(values["targets"], []any{2, 1}) {
		t.Fatalf("values = %#v, errors = %#v", values, fieldErrors)
	}
}

func TestSubmitRejectsTrailingJSONValues(t *testing.T) {
	fields := []ResolvedField{
		{Field: Field{Variable: "count", Type: TypeNumber}},
		{Field: Field{Variable: "items", Type: TypeArray}},
		{Field: Field{Variable: "config", Type: TypeObject}},
	}
	_, fieldErrors := Submit(fields, url.Values{
		"field_0": {"1 true"},
		"field_1": {`[1] {}`},
		"field_2": {`{} []`},
	})
	for _, variable := range []string{"count", "items", "config"} {
		if fieldErrors[variable] == "" {
			t.Fatalf("errors = %#v, want error for %s", fieldErrors, variable)
		}
	}
}

func decodeFormNode(t *testing.T, body string) *yaml.Node {
	t.Helper()
	var document yaml.Node
	if err := yaml.Unmarshal([]byte(body), &document); err != nil {
		t.Fatal(err)
	}
	return document.Content[0]
}
