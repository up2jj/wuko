package engine

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/up2jj/wuko/internal/steptest"
	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/workflow"
)

func TestCaptureValuesRedactsSensitiveValuesOnFailedRun(t *testing.T) {
	t.Parallel()
	registry := steptest.Registry(t, map[string]step.Builder{
		"prompt": func(map[string]any) (step.Runner, error) {
			return steptest.RunnerFunc(func(context.Context, step.Request) (step.Result, error) {
				return step.Result{
					Outputs:   map[string]any{"value": "hunter22", "items": []string{"a", "b"}},
					Variables: map[string]any{"login": "hunter22", "api_token": "abc", "count": 3},
					Sensitive: []string{"hunter22"},
				}, nil
			}), nil
		},
		"echo": func(map[string]any) (step.Runner, error) {
			return steptest.RunnerFunc(func(context.Context, step.Request) (step.Result, error) {
				return step.Result{Outputs: map[string]any{"stdout": "using hunter22 now", "url": "https://user:pw@example.com/x?key=1"}}, nil
			}), nil
		},
		"fail": func(map[string]any) (step.Runner, error) {
			return steptest.RunnerFunc(func(context.Context, step.Request) (step.Result, error) {
				return step.Result{}, errors.New("unexpected type")
			}), nil
		},
	})
	definition := steptest.Definition(t, "capture",
		workflow.Step{ID: "ask", Type: "prompt"},
		workflow.Step{ID: "use", Type: "echo"},
		workflow.Step{ID: "check", Type: "fail"},
	)
	var values *RunValues
	_, err := New(registry).Run(t.Context(), definition, Options{CaptureValues: true, Stdout: io.Discard, Stderr: io.Discard,
		Progress: func(event ProgressEvent) {
			if event.Kind == WorkflowFinished && event.Depth == 0 {
				values = event.Values
			}
		}})
	if err == nil {
		t.Fatal("run succeeded")
	}
	if values == nil {
		t.Fatal("failed run captured no values")
	}
	want := &RunValues{
		Steps: map[string]any{
			"ask": map[string]any{"value": "<redacted>", "items": []any{"a", "b"}},
			"use": map[string]any{"stdout": "using <redacted> now", "url": "https://example.com/x"},
		},
		Vars: map[string]any{"login": "<redacted>", "api_token": "<redacted>", "count": json.Number("3")},
	}
	if diff := cmp.Diff(want, values); diff != "" {
		t.Fatalf("values (-want +got):\n%s", diff)
	}
}

func TestCaptureValuesIsOffByDefault(t *testing.T) {
	t.Parallel()
	registry := steptest.Registry(t, map[string]step.Builder{"noop": func(map[string]any) (step.Runner, error) {
		return steptest.RunnerFunc(func(context.Context, step.Request) (step.Result, error) { return step.Result{}, nil }), nil
	}})
	captured := false
	_, err := New(registry).Run(t.Context(), steptest.Definition(t, "off", workflow.Step{ID: "work", Type: "noop"}), Options{Stdout: io.Discard, Stderr: io.Discard,
		Progress: func(event ProgressEvent) { captured = captured || event.Values != nil }})
	if err != nil || captured {
		t.Fatalf("captured=%v err=%v", captured, err)
	}
}

func TestNormalizeValueReportsUnencodableEntries(t *testing.T) {
	t.Parallel()
	if got, ok := normalizeValue(func() {}).(string); !ok || !strings.HasPrefix(got, "<unavailable:") {
		t.Fatalf("normalized = %#v", got)
	}
}
