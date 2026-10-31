package engine_test

import (
	"context"
	"io"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/up2jj/wuko/engine"
	"github.com/up2jj/wuko/helper"
	"github.com/up2jj/wuko/internal/testfs"
	"github.com/up2jj/wuko/workflow"
)

type bindingPluginHelpers struct{ calls atomic.Int32 }

func (loader *bindingPluginHelpers) LoadHelpers(context.Context, map[string]workflow.ResolvedPluginSource) (helper.Set, error) {
	return helper.Set{"acme_value": func(context.Context, []any) (any, error) {
		loader.calls.Add(1)
		return map[string]any{"enabled": true, "items": []any{1, "{{ .vars.unknown }}"}}, nil
	}}, nil
}

func TestBindingsCallPluginHelpersOnlyAtRuntimeOnce(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "workflow.yaml")
	testfs.Write(t, path, `version: 1
name: helper-bindings
plugins:
  acme:
    source: https://example.test/plugin.json
steps:
  - id: output
    type: set
    with:
      variable: output
      value: {expr: acme_value()}
`)
	helpers := &bindingPluginHelpers{}
	definition, err := workflow.NewLoader(nil, workflow.WithPluginHelpers(helpers)).DecodeContext(t.Context(), path, workflow.LoadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	subject := bindingEngine(t)
	if err := subject.Validate(t.Context(), definition, engine.Options{RunDir: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	if helpers.calls.Load() != 0 {
		t.Fatal("validation called a plugin helper")
	}
	state, err := subject.Run(t.Context(), definition, engine.Options{RunDir: t.TempDir(), Stdout: io.Discard, Stderr: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if helpers.calls.Load() != 1 {
		t.Fatalf("helper calls=%d", helpers.calls.Load())
	}
	output := state.Vars["output"].(map[string]any)
	if output["enabled"] != true || output["items"].([]any)[1] != "{{ .vars.unknown }}" {
		t.Fatalf("output=%#v", output)
	}
}
