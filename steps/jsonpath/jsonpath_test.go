package jsonpath

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/up2jj/wuko/step"
)

func TestAllReturnsValuesPathsAndVariable(t *testing.T) {
	runner, err := New(map[string]any{
		"from": "steps.fetch.value", "query": `$.projects[?@.active == true].id`,
		"variable": "project_ids",
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(t.Context(), step.Request{Steps: map[string]any{
		"fetch": map[string]any{"value": map[string]any{"projects": []any{
			map[string]any{"id": "one", "active": true},
			map[string]any{"id": "two", "active": false},
			map[string]any{"id": "three", "active": true},
		}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	wantValues := []any{"one", "three"}
	wantPaths := []any{"$['projects'][0]['id']", "$['projects'][2]['id']"}
	if !reflect.DeepEqual(result.Outputs["value"], wantValues) {
		t.Fatalf("value = %#v", result.Outputs["value"])
	}
	if !reflect.DeepEqual(result.Outputs["paths"], wantPaths) {
		t.Fatalf("paths = %#v", result.Outputs["paths"])
	}
	if result.Outputs["count"] != 2 || !reflect.DeepEqual(result.Variables["project_ids"], wantValues) {
		t.Fatalf("result = %#v", result)
	}
}

func TestOneReturnsScalar(t *testing.T) {
	runner, err := New(map[string]any{
		"from": "vars.document", "query": `$.project.name`, "result": "one", "variable": "name",
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(t.Context(), step.Request{Vars: map[string]any{
		"document": map[string]any{"project": map[string]any{"name": "wuko"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outputs["value"] != "wuko" || result.Variables["name"] != "wuko" {
		t.Fatalf("result = %#v", result)
	}
	if !reflect.DeepEqual(result.Outputs["paths"], []any{"$['project']['name']"}) {
		t.Fatalf("paths = %#v", result.Outputs["paths"])
	}
}

func TestOneRejectsZeroOrMultipleMatchesWithoutResult(t *testing.T) {
	for _, query := range []string{`$.missing`, `$.items[*]`} {
		runner, err := New(map[string]any{"from": "vars.document", "query": query, "result": "one", "variable": "selected"})
		if err != nil {
			t.Fatal(err)
		}
		result, err := runner.Run(t.Context(), step.Request{Vars: map[string]any{
			"document": map[string]any{"items": []any{1, 2}},
		}})
		if err == nil || !strings.Contains(err.Error(), "want exactly one") {
			t.Fatalf("query %q error = %v", query, err)
		}
		if len(result.Outputs) != 0 || len(result.Variables) != 0 {
			t.Fatalf("query %q result = %#v", query, result)
		}
	}
}

func TestAllAllowsNoMatches(t *testing.T) {
	runner, err := New(map[string]any{"from": "vars.document", "query": `$.missing`})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(t.Context(), step.Request{Vars: map[string]any{"document": map[string]any{}}})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result.Outputs["value"], []any{}) || result.Outputs["count"] != 0 {
		t.Fatalf("result = %#v", result)
	}
}

func TestRejectsInvalidConfiguration(t *testing.T) {
	tests := []map[string]any{
		{},
		{"query": "$"},
		{"from": "vars.value"},
		{"from": "value", "query": "$"},
		{"from": "vars.value", "query": "items[*]"},
		{"from": "vars.value", "query": "$", "result": "first"},
		{"from": "vars.value", "query": "$", "unknown": true},
	}
	for _, raw := range tests {
		if _, err := New(raw); err == nil || strings.TrimSpace(err.Error()) == "" {
			t.Fatalf("New(%#v) error = %v", raw, err)
		}
	}
}

func TestTemplatedConfigurationIsAcceptedDuringValidation(t *testing.T) {
	if _, err := New(map[string]any{
		"from": "{{ .vars.source }}", "query": "$.{{ .vars.field }}", "result": "{{ .vars.result }}",
	}); err != nil {
		t.Fatal(err)
	}
}

func TestRunHonorsCancellation(t *testing.T) {
	runner, err := New(map[string]any{"from": "vars.document", "query": "$"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	result, err := runner.Run(ctx, step.Request{Vars: map[string]any{"document": true}})
	if err != context.Canceled || len(result.Outputs) != 0 || len(result.Variables) != 0 {
		t.Fatalf("result = %#v, error = %v", result, err)
	}
}
