package engine_test

import (
	"context"
	"io"
	"testing"

	"github.com/up2jj/wuko/engine"
	"github.com/up2jj/wuko/process"
	"github.com/up2jj/wuko/step"
	assertstep "github.com/up2jj/wuko/steps/assert"
	requiretoolstep "github.com/up2jj/wuko/steps/require_tool"
	"github.com/up2jj/wuko/workflow"
)

type requireToolExecutor struct{ options process.Options }

func (executor *requireToolExecutor) Run(_ context.Context, options process.Options) (process.Result, error) {
	executor.options = options
	return process.Result{Stdout: "go version go1.26.1 darwin/arm64\n"}, nil
}

func TestRequireToolRendersConfigurationAndCommitsOutputs(t *testing.T) {
	definition := testDefinition(t, "tools", workflow.Step{ID: "go", Type: "require_tool", With: map[string]any{
		"tool": "{{ .vars.tool }}", "version_args": []any{"{{ .vars.version_arg }}"},
		"constraint": "{{ .vars.constraint }}",
	}})
	definition.Vars = map[string]any{"tool": "go", "version_arg": "version", "constraint": ">= 1.26.0"}
	registry := newTestRegistry(t, nil)
	if err := requiretoolstep.Register(registry); err != nil {
		t.Fatal(err)
	}
	executor := &requireToolExecutor{}
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
	if executor.options.Command != "go" || len(executor.options.Args) != 1 || executor.options.Args[0] != "version" {
		t.Fatalf("options = %#v", executor.options)
	}
	if executor.options.Env[step.AttemptEnv] != "1" {
		t.Fatalf("environment = %#v", executor.options.Env)
	}
}

type unavailableRequireToolExecutor struct{}

func (unavailableRequireToolExecutor) Run(_ context.Context, _ process.Options) (process.Result, error) {
	return process.Result{Stderr: "not found", ExitCode: 127}, &process.ExitError{Command: "lsof", Code: 127}
}

func TestOptionalRequireToolCommitsOutputsForLaterConditions(t *testing.T) {
	definition := testDefinition(t, "optional-tools",
		workflow.Step{ID: "lsof", Type: "require_tool", With: map[string]any{
			"tool": "lsof", "constraint": ">= 4.0.0", "required": false,
		}},
		workflow.Step{ID: "check", Type: "assert", With: map[string]any{
			"expr":    "!steps.lsof.available && steps.lsof.path == '' && steps.lsof.version == '' && !steps.lsof.constraint_matched",
			"message": "optional tool outputs were not committed",
		}},
	)
	registry := newTestRegistry(t, nil)
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
	definition := testDefinition(t, "tools", workflow.Step{ID: "go", Type: "require_tool", With: map[string]any{
		"tool": "go", "constraint": ">= 1.25.0",
	}})
	registry := newTestRegistry(t, nil)
	if err := requiretoolstep.Register(registry); err != nil {
		t.Fatal(err)
	}
	executor := &requireToolExecutor{}
	workflowEngine := engine.New(registry)
	options := engine.Options{RunDir: t.TempDir(), Executor: executor, Stdout: io.Discard, Stderr: io.Discard}
	if err := workflowEngine.Validate(t.Context(), definition, options); err != nil {
		t.Fatal(err)
	}
	options.DryRun = true
	if _, err := workflowEngine.Run(t.Context(), definition, options); err != nil {
		t.Fatal(err)
	}
	if executor.options.Command != "" {
		t.Fatalf("validation or dry run executed probe: %#v", executor.options)
	}
}
