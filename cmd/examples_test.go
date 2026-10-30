package cmd

import (
	"bytes"
	"context"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/up2jj/wuko/engine"
	envload "github.com/up2jj/wuko/environment"
	"github.com/up2jj/wuko/executor"
	"github.com/up2jj/wuko/step"
	sshstep "github.com/up2jj/wuko/steps/ssh"
	"github.com/up2jj/wuko/workflow"
)

// Every example must validate against the built-in steps, so the examples cannot drift from the
// schema they document. Standalone files are validated directly, every target included; example
// projects are validated through the validate command, which also resolves their dependencies
// and child workflows.
func TestExamplesValidate(t *testing.T) {
	t.Parallel()
	root, err := filepath.Abs(filepath.Join("..", "examples"))
	if err != nil {
		t.Fatal(err)
	}
	registry := step.NewRegistry()
	if err := registerBuiltinSteps(registry); err != nil {
		t.Fatal(err)
	}
	executors := executor.NewRegistry()
	if err := sshstep.RegisterExecutor(executors); err != nil {
		t.Fatal(err)
	}

	standalone, err := filepath.Glob(filepath.Join(root, "*.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range standalone {
		t.Run(filepath.Base(path), func(t *testing.T) {
			runDir := t.TempDir()
			definition, err := workflow.NewLoader(nil).Decode(path, workflow.LoadOptions{RunDir: runDir})
			if err != nil {
				t.Fatal(err)
			}
			targets := slices.Sorted(maps.Keys(definition.Targets))
			if len(targets) == 0 {
				targets = []string{""}
			}
			for _, target := range targets {
				definition, err := workflow.NewLoader(nil).Load(t.Context(), path, workflow.LoadOptions{Target: target, RunDir: runDir})
				if err != nil {
					t.Fatalf("target %q: %v", target, err)
				}
				if err := engine.New(registry, engine.WithExecutors(executors)).Validate(t.Context(), definition, engine.Options{RunDir: runDir, ReplaceProcess: previewProcessReplacement}); err != nil {
					t.Fatalf("target %q: %v", target, err)
				}
			}
		})
	}

	projects, err := filepath.Glob(filepath.Join(root, "*", ".wuko", "workflows"))
	if err != nil {
		t.Fatal(err)
	}
	projects = append(projects, filepath.Join(root, ".wuko", "workflows"))
	for _, workflows := range projects {
		project := filepath.Dir(filepath.Dir(workflows))
		name, _ := filepath.Rel(root, project)
		t.Run("project "+name, func(t *testing.T) {
			// The project is copied so validation cannot write plugin pins or state into the repository.
			copied := t.TempDir()
			if err := os.CopyFS(copied, os.DirFS(project)); err != nil {
				t.Fatal(err)
			}
			var output bytes.Buffer
			command := newRootCmd(dependencies{
				stdin: bytes.NewReader(nil), stdout: &output, stderr: &output,
				cwd: func() (string, error) { return copied, nil }, homeDir: func() (string, error) { return t.TempDir(), nil },
				configDir: func() (string, error) { return t.TempDir(), nil }, registry: registry,
				getenv: func(string) string { return "" }, environment: envload.InvocationLoaderFunc(func(context.Context, string, map[string]string, envload.Policy) (envload.InvocationEnvironment, error) {
					// start-task reads a token from the environment; any value satisfies validation.
					return envload.InvocationEnvironment{Values: map[string]string{"WUKO_DEMO_TOKEN": "demo"}}, nil
				}),
			})
			command.SetArgs([]string{"validate"})
			if err := command.ExecuteContext(t.Context()); err != nil {
				t.Fatalf("%v\n%s", err, output.String())
			}
		})
	}
}
