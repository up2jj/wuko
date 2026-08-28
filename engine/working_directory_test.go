package engine

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/workflow"
)

type directoryCaptureRunner struct {
	variable string
}

func (runner directoryCaptureRunner) Run(_ context.Context, request step.Request) (step.Result, error) {
	result := step.Result{Outputs: map[string]any{"dir": request.RunDir}}
	if runner.variable != "" {
		result.Variables = map[string]any{runner.variable: request.RunDir}
	}
	return result, nil
}

type scopedValidationRunner struct {
	expected string
}

func (runner scopedValidationRunner) Validate(_ context.Context, request step.Request) error {
	if request.RunDir != runner.expected {
		return fmt.Errorf("validation run directory = %q, want %q", request.RunDir, runner.expected)
	}
	return nil
}

func (runner scopedValidationRunner) Run(_ context.Context, request step.Request) (step.Result, error) {
	return step.Result{Outputs: map[string]any{"dir": request.RunDir}}, nil
}

func workingDirectoryRegistry(t *testing.T) *step.Registry {
	t.Helper()
	registry := newTestRegistry(t, nil)
	if err := registry.Register("capture_dir", func(raw map[string]any) (step.Runner, error) {
		variable, _ := raw["variable"].(string)
		return directoryCaptureRunner{variable: variable}, nil
	}); err != nil {
		t.Fatal(err)
	}
	return registry
}

func TestWorkingDirectoryScopesAndRestoresRunDirectory(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	nested := filepath.Join(project, "nested")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	definition := &workflow.Definition{
		Version: 1, Name: "scoped", Dir: root, Vars: map[string]any{"project": "project"},
		Steps: []workflow.Step{
			{WorkingDirectory: "{{ .vars.project }}", Steps: []workflow.Step{
				{ID: "project", Type: "capture_dir", With: map[string]any{}},
				{WorkingDirectory: "nested", Steps: []workflow.Step{{ID: "nested", Type: "capture_dir", With: map[string]any{}}}},
			}},
			{ID: "outside", Type: "capture_dir", With: map[string]any{}},
		},
	}
	state, err := New(workingDirectoryRegistry(t)).Run(t.Context(), definition, Options{RunDir: root, Stdout: io.Discard, Stderr: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if got := state.Steps["project"].(map[string]any)["dir"]; got != project {
		t.Fatalf("project dir = %q, want %q", got, project)
	}
	if got := state.Steps["nested"].(map[string]any)["dir"]; got != nested {
		t.Fatalf("nested dir = %q, want %q", got, nested)
	}
	if got := state.Steps["outside"].(map[string]any)["dir"]; got != root {
		t.Fatalf("outside dir = %q, want %q", got, root)
	}
	if state.Stats.Total != 3 || len(state.Stats.Steps) != 3 {
		t.Fatalf("stats = %#v", state.Stats)
	}
}

func TestWorkingDirectoryValidatorReceivesScopedRunDirectory(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	if err := os.Mkdir(project, 0o755); err != nil {
		t.Fatal(err)
	}
	registry := newTestRegistry(t, nil)
	if err := registry.Register("validate_dir", func(map[string]any) (step.Runner, error) {
		return scopedValidationRunner{expected: project}, nil
	}); err != nil {
		t.Fatal(err)
	}
	definition := &workflow.Definition{
		Version: 1, Name: "validated", Dir: root, Vars: map[string]any{"project": "project"},
		Steps: []workflow.Step{{WorkingDirectory: "{{ .vars.project }}", Steps: []workflow.Step{{ID: "inside", Type: "validate_dir", With: map[string]any{}}}}},
	}
	state, err := New(registry).Run(t.Context(), definition, Options{RunDir: root, Stdout: io.Discard, Stderr: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if got := state.Steps["inside"].(map[string]any)["dir"]; got != project {
		t.Fatalf("run directory = %q, want %q", got, project)
	}
}

func TestWorkingDirectoryDefersValidationUntilRuntimeDirectoryIsKnown(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	if err := os.Mkdir(project, 0o755); err != nil {
		t.Fatal(err)
	}
	registry := newTestRegistry(t, nil)
	if err := registry.Register("select_dir", func(map[string]any) (step.Runner, error) {
		return runnerFunc(func(context.Context, step.Request) (step.Result, error) {
			return step.Result{Outputs: map[string]any{"dir": project}}, nil
		}), nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register("validate_dir", func(map[string]any) (step.Runner, error) {
		return scopedValidationRunner{expected: project}, nil
	}); err != nil {
		t.Fatal(err)
	}
	definition := &workflow.Definition{
		Version: 1, Name: "runtime-validated", Dir: root,
		Steps: []workflow.Step{
			{ID: "select", Type: "select_dir", With: map[string]any{}},
			{WorkingDirectory: "{{ .steps.select.dir }}", Steps: []workflow.Step{{ID: "inside", Type: "validate_dir", With: map[string]any{}}}},
		},
	}
	if _, err := New(registry).Run(t.Context(), definition, Options{RunDir: root, Stdout: io.Discard, Stderr: io.Discard}); err != nil {
		t.Fatal(err)
	}
}

func TestWorkingDirectoryRejectsMissingAndNonDirectoryPaths(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "file.txt")
	if err := os.WriteFile(file, []byte("content"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		path string
		want string
	}{
		{name: "missing", path: "missing", want: "inspecting directory"},
		{name: "file", path: file, want: "is not a directory"},
	} {
		t.Run(test.name, func(t *testing.T) {
			definition := &workflow.Definition{Version: 1, Name: "invalid", Dir: root, Steps: []workflow.Step{{
				WorkingDirectory: test.path, Steps: []workflow.Step{{ID: "inside", Type: "capture_dir", With: map[string]any{}}},
			}}}
			_, err := New(workingDirectoryRegistry(t)).Run(t.Context(), definition, Options{RunDir: root, Stdout: io.Discard, Stderr: io.Discard})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestWorkingDirectoryIsAtomicConcurrentBranch(t *testing.T) {
	root := t.TempDir()
	backend := filepath.Join(root, "backend")
	frontend := filepath.Join(root, "frontend")
	for _, dir := range []string{backend, frontend} {
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	definition := &workflow.Definition{Version: 1, Name: "parallel", Dir: root, Steps: []workflow.Step{{
		Concurrent: &workflow.ConcurrentGroup{MaxConcurrency: 2, FailFast: true, Steps: []workflow.Step{
			{WorkingDirectory: "backend", Steps: []workflow.Step{
				{ID: "generate", Type: "capture_dir", With: map[string]any{"variable": "generated"}},
				{ID: "test", Type: "capture_dir", With: map[string]any{}},
			}},
			{WorkingDirectory: frontend, Steps: []workflow.Step{{ID: "lint", Type: "capture_dir", With: map[string]any{}}}},
		}},
	}}}
	state, err := New(workingDirectoryRegistry(t)).Run(t.Context(), definition, Options{RunDir: root, Stdout: io.Discard, Stderr: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if state.Vars["generated"] != backend {
		t.Fatalf("generated = %#v", state.Vars["generated"])
	}
	wantIDs := []string{"generate", "test", "lint"}
	for i, stats := range state.Stats.Steps {
		if stats.ID != wantIDs[i] || stats.Index != i+1 {
			t.Fatalf("stats[%d] = %#v", i, stats)
		}
	}
}

func TestWorkingDirectoryConcurrentBranchesDetectVariableConflicts(t *testing.T) {
	root := t.TempDir()
	definition := &workflow.Definition{Version: 1, Name: "conflict", Dir: root, Steps: []workflow.Step{{
		Concurrent: &workflow.ConcurrentGroup{MaxConcurrency: 2, FailFast: true, Steps: []workflow.Step{
			{WorkingDirectory: root, Steps: []workflow.Step{{ID: "one", Type: "capture_dir", With: map[string]any{"variable": "shared"}}}},
			{WorkingDirectory: root, Steps: []workflow.Step{{ID: "two", Type: "capture_dir", With: map[string]any{"variable": "shared"}}}},
		}},
	}}}
	_, err := New(workingDirectoryRegistry(t)).Run(t.Context(), definition, Options{RunDir: root, Stdout: io.Discard, Stderr: io.Discard})
	if err == nil || !strings.Contains(err.Error(), `both write variable "shared"`) {
		t.Fatalf("conflict error = %v", err)
	}
}

func TestWorkingDirectoryComposesWithForeachAndCompositeActions(t *testing.T) {
	root := t.TempDir()
	service := filepath.Join(root, "services", "api")
	if err := os.MkdirAll(service, 0o755); err != nil {
		t.Fatal(err)
	}
	action := &workflow.Action{
		Version: 1, Name: "capture action", Dir: root,
		Outputs: map[string]workflow.ActionOutput{"dir": {Value: "steps.inside.dir"}},
		Steps:   []workflow.Step{{ID: "inside", Type: "capture_dir", With: map[string]any{}}},
	}
	definition := &workflow.Definition{
		Version: 1, Name: "composed", Dir: root, Vars: map[string]any{"services": []any{"api"}},
		Steps: []workflow.Step{{WorkingDirectory: "services", Steps: []workflow.Step{{
			ID: "service_loop", Foreach: &workflow.ForeachGroup{
				Items: "vars.services", Collect: "steps.call", MaxConcurrency: 1, FailFast: true,
				Steps: []workflow.Step{{WorkingDirectory: "{{ .foreach.item }}", Steps: []workflow.Step{{
					ID: "call", Uses: workflow.ActionSource{URL: "https://example.test/action"}, Action: action, With: map[string]any{},
				}}}},
			},
		}}}},
	}
	state, err := New(workingDirectoryRegistry(t)).Run(t.Context(), definition, Options{RunDir: root, Stdout: io.Discard, Stderr: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	call := state.Steps["service_loop"].(map[string]any)["results"].([]any)[0].(map[string]any)
	if call["dir"] != service {
		t.Fatalf("action dir = %#v, want %q", call["dir"], service)
	}
}

func TestWorkingDirectoryDryRunDisplaysNestedPlan(t *testing.T) {
	definition := testDefinition(t, "dry", workflow.Step{
		WorkingDirectory: "{{ .vars.dir }}", Steps: []workflow.Step{{ID: "run", Type: "capture_dir", With: map[string]any{}}},
	})
	definition.Vars = map[string]any{"dir": "/unused"}

	var output bytes.Buffer
	if _, err := New(workingDirectoryRegistry(t)).Run(t.Context(), definition, Options{DryRun: true, Stdout: &output, Stderr: io.Discard}); err != nil {
		t.Fatal(err)
	}
	if want := "1. working_directory: {{ .vars.dir }}\n   1.1 run (capture_dir)\n"; output.String() != want {
		t.Fatalf("output = %q, want %q", output.String(), want)
	}
}
