package engine_test

import (
	"context"
	"io"
	"testing"

	"github.com/up2jj/wuko/engine"
	"github.com/up2jj/wuko/internal/steptest"
	"github.com/up2jj/wuko/process"
	"github.com/up2jj/wuko/process/processtest"
	"github.com/up2jj/wuko/step"
	assertstep "github.com/up2jj/wuko/steps/assert"
	requiretoolstep "github.com/up2jj/wuko/steps/require_tool"
	"github.com/up2jj/wuko/workflow"
)

func TestRequireToolRendersConfigurationAndCommitsOutputs(t *testing.T) {
	t.Parallel()
	definition := steptest.Definition(t, "tools", workflow.Step{ID: "go", Type: "require_tool", With: map[string]any{
		"tool": "{{ .vars.tool }}", "version_args": []any{"{{ .vars.version_arg }}"},
		"constraint": "{{ .vars.constraint }}",
	}})
	definition.Vars = map[string]any{"tool": "go", "version_arg": "version", "constraint": ">= 1.26.0"}
	registry := steptest.Registry(t, nil)
	if err := requiretoolstep.Register(registry); err != nil {
		t.Fatal(err)
	}
	executor := &processtest.Executor{Reply: processtest.Reply{Result: process.Result{Stdout: "go version go1.26.1 darwin/arm64\n"}}}
	state, err := engine.New(registry).Run(t.Context(), definition, engine.Options{
		RunDir: t.TempDir(), Executor: executor, Stdout: io.Discard, Stderr: io.Discard,
	})
	if err != nil {
		t.Fatal(err)
	}
	outputs := state.Steps["go"].(map[string]any)
	if outputs["available"] != true || outputs["path"] != "go" || outputs["version"] != "1.26.1" || outputs["constraint_matched"] != true {
		t.Fatalf("outputs = %#v", outputs)
	}
	if executor.Last().Command != "go" || len(executor.Last().Args) != 1 || executor.Last().Args[0] != "version" {
		t.Fatalf("options = %#v", executor.Last())
	}
	if executor.Last().Env[step.AttemptEnv] != "1" {
		t.Fatalf("environment = %#v", executor.Last().Env)
	}
}

type unavailableRequireToolExecutor struct{}

func (unavailableRequireToolExecutor) Run(_ context.Context, _ process.Options) (process.Result, error) {
	return process.Result{Stderr: "not found", ExitCode: 127}, &process.ExitError{Command: "lsof", Code: 127}
}

func TestOptionalRequireToolCommitsOutputsForLaterConditions(t *testing.T) {
	t.Parallel()
	definition := steptest.Definition(t, "optional-tools",
		workflow.Step{ID: "lsof", Type: "require_tool", With: map[string]any{
			"tool": "lsof", "constraint": ">= 4.0.0", "required": false,
		}},
		workflow.Step{ID: "check", Type: "assert", With: map[string]any{
			"expr":    "!steps.lsof.available && steps.lsof.path == '' && steps.lsof.version == '' && !steps.lsof.constraint_matched",
			"message": "optional tool outputs were not committed",
		}},
	)
	registry := steptest.Registry(t, nil)
	for _, register := range []func(*step.Registry) error{requiretoolstep.Register, assertstep.Register} {
		if err := register(registry); err != nil {
			t.Fatal(err)
		}
	}
	state, err := engine.New(registry).Run(t.Context(), definition, engine.Options{
		RunDir: t.TempDir(), Executor: unavailableRequireToolExecutor{}, Stdout: io.Discard, Stderr: io.Discard,
	})
	if err != nil {
		t.Fatal(err)
	}
	outputs := state.Steps["lsof"].(map[string]any)
	if outputs["available"] != false || outputs["path"] != "" || outputs["version"] != "" || outputs["constraint_matched"] != false {
		t.Fatalf("outputs = %#v", outputs)
	}
}

func TestRequireToolValidationAndDryRunDoNotExecuteProbe(t *testing.T) {
	t.Parallel()
	definition := steptest.Definition(t, "tools", workflow.Step{ID: "go", Type: "require_tool", With: map[string]any{
		"tool": "go", "constraint": ">= 1.25.0",
	}})
	registry := steptest.Registry(t, nil)
	if err := requiretoolstep.Register(registry); err != nil {
		t.Fatal(err)
	}
	executor := &processtest.Executor{Reply: processtest.Reply{Result: process.Result{Stdout: "go version go1.26.1 darwin/arm64\n"}}}
	workflowEngine := engine.New(registry)
	options := engine.Options{RunDir: t.TempDir(), Executor: executor, Stdout: io.Discard, Stderr: io.Discard}
	if err := workflowEngine.Validate(t.Context(), definition, options); err != nil {
		t.Fatal(err)
	}
	options.DryRun = true
	if _, err := workflowEngine.Run(t.Context(), definition, options); err != nil {
		t.Fatal(err)
	}
	if executor.Last().Command != "" {
		t.Fatalf("validation or dry run executed probe: %#v", executor.Last())
	}
}
