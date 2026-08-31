package set

import (
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/up2jj/wuko/step"
)

func TestLiteralValue(t *testing.T) {
	value := map[string]any{"enabled": true, "regions": []any{"eu", "us"}}
	runner, err := New(map[string]any{"variable": "deployment", "value": value})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(t.Context(), step.Request{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Variables["deployment"].(map[string]any)["enabled"] != true {
		t.Fatalf("result = %#v", result)
	}
}

func TestExpressionUsesAllRoots(t *testing.T) {
	runner, err := New(map[string]any{
		"variable": "artifact",
		"expr":     `steps.release.version + "-" + vars.target + "-" + inputs.suffix + "-" + env.CHANNEL + "-" + workflow.name + "-" + run.dir`,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(t.Context(), step.Request{
		WorkflowName: "publish", RunDir: "/work",
		Inputs: map[string]any{"suffix": "archive"}, Vars: map[string]any{"target": "linux"},
		Env: map[string]string{"CHANNEL": "stable"}, Steps: map[string]any{"release": map[string]any{"version": "v1"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := result.Outputs["value"]; got != "v1-linux-archive-stable-publish-/work" {
		t.Fatalf("value = %#v", got)
	}
}

func TestExpressionUsesControlBindings(t *testing.T) {
	runner, err := New(map[string]any{"variable": "value", "expr": `len(batch.items) + foreach.index + matrix.offset`})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(t.Context(), step.Request{Bindings: map[string]any{
		"batch": map[string]any{"items": []any{"api", "worker"}}, "foreach": map[string]any{"index": 2}, "matrix": map[string]any{"offset": 3},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outputs["value"] != 7 {
		t.Fatalf("result = %#v", result)
	}
}

func TestExpressionUsesSharedHelpers(t *testing.T) {
	runner, err := New(map[string]any{
		"variable": "manifest",
		"expr":     `toYAML(dict("name", default(vars.name, "wuko")))`,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(t.Context(), step.Request{Vars: map[string]any{"name": ""}})
	if err != nil {
		t.Fatal(err)
	}
	if got := result.Outputs["value"]; got != "name: wuko\n" {
		t.Fatalf("value = %#v", got)
	}
}

func TestExpressionUsesSecretHelper(t *testing.T) {
	runner, err := New(map[string]any{"variable": "token", "expr": `secret("op://Production/API/token")`})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(t.Context(), step.Request{Secret: func(reference string) (string, error) {
		if reference != "op://Production/API/token" {
			t.Fatalf("reference = %q", reference)
		}
		return "resolved-token", nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outputs["value"] != "resolved-token" {
		t.Fatalf("result = %#v", result)
	}
}

func TestExpressionStoresTypedCollectionResults(t *testing.T) {
	tests := []struct {
		name string
		expr string
		vars map[string]any
		want any
	}{
		{
			name: "index by",
			expr: `indexBy(vars.services, "id")`,
			vars: map[string]any{"services": []any{
				map[string]any{"id": "api", "port": 8080},
				map[string]any{"id": "web", "port": 3000},
			}},
			want: map[string]any{
				"api": map[string]any{"id": "api", "port": 8080},
				"web": map[string]any{"id": "web", "port": 3000},
			},
		},
		{
			name: "chunk",
			expr: `chunk(vars.targets, 2)`,
			vars: map[string]any{"targets": []any{"api", "worker", "web"}},
			want: [][]any{{"api", "worker"}, {"web"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner, err := New(map[string]any{"variable": "result", "expr": tt.expr})
			if err != nil {
				t.Fatal(err)
			}
			result, err := runner.Run(t.Context(), step.Request{Vars: tt.vars})
			if err != nil {
				t.Fatal(err)
			}
			if got := result.Variables["result"]; !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("value = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestExpressionFailureDoesNotReturnResult(t *testing.T) {
	runner, err := New(map[string]any{"variable": "value", "expr": `vars.missing.name`})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(t.Context(), step.Request{Vars: map[string]any{}})
	if err == nil || len(result.Outputs) != 0 || len(result.Variables) != 0 {
		t.Fatalf("result = %#v, error = %v", result, err)
	}
}

func TestRejectsInvalidConfigurationAndValue(t *testing.T) {
	tests := []map[string]any{
		{"value": true},
		{"variable": "x"},
		{"variable": "x", "value": true, "expr": "true"},
		{"variable": "x", "expr": ""},
		{"variable": "x", "expr": "vars."},
		{"variable": "x", "value": math.Inf(1)},
		{"variable": "x", "value": true, "unknown": true},
	}
	for _, raw := range tests {
		if _, err := New(raw); err == nil {
			t.Fatalf("New(%#v) succeeded", raw)
		} else if strings.TrimSpace(err.Error()) == "" {
			t.Fatal("empty error")
		}
	}
}
