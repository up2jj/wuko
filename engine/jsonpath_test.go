package engine_test

import (
	"io"
	"reflect"
	"testing"

	"github.com/up2jj/wuko/engine"
	"github.com/up2jj/wuko/step"
	jsonpathstep "github.com/up2jj/wuko/steps/jsonpath"
	"github.com/up2jj/wuko/workflow"
)

func TestJSONPathTemplatesAndCommitsTypedResults(t *testing.T) {
	definition := &workflow.Definition{
		Version: 1, Name: "selection", Dir: t.TempDir(),
		Vars: map[string]any{
			"source": "vars.document", "collection": "projects", "mode": "all",
			"document": map[string]any{"projects": []any{
				map[string]any{"id": "one", "active": true},
				map[string]any{"id": "two", "active": false},
			}},
		},
		Steps: []workflow.Step{{ID: "active", Type: "jsonpath", With: map[string]any{
			"from": "{{ .vars.source }}", "query": "$.{{ .vars.collection }}[?@.active == true].id",
			"result": "{{ .vars.mode }}", "variable": "active_ids",
		}}},
	}
	registry := step.NewRegistry()
	if err := jsonpathstep.Register(registry); err != nil {
		t.Fatal(err)
	}
	state, err := engine.New(registry).Run(t.Context(), definition, engine.Options{
		RunDir: t.TempDir(), Stdout: io.Discard, Stderr: io.Discard,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []any{"one"}
	if !reflect.DeepEqual(state.Vars["active_ids"], want) {
		t.Fatalf("variables = %#v", state.Vars)
	}
	if !reflect.DeepEqual(state.Steps["active"].(map[string]any)["value"], want) {
		t.Fatalf("outputs = %#v", state.Steps["active"])
	}
}
