package cmd

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/up2jj/wuko/step"
	setstep "github.com/up2jj/wuko/steps/set"
	"github.com/up2jj/wuko/workflow"
)

func TestRunCommandExecutesDependencyChainAndExportsRootOutputs(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".wuko", "workflows")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeWorkflowData(t, filepath.Join(dir, "build.yaml"), `version: 1
name: build
invokable: false
outputs:
  artifact: {type: string, value: '"fallback"'}
steps:
  - return: {outputs: {artifact: '"dist/app.tar.gz"'}}
`)
	writeWorkflowData(t, filepath.Join(dir, "release.yaml"), `version: 1
name: release
depends_on: {build: build}
outputs:
  artifact: {type: string, value: dependencies.build.artifact}
steps:
  - return: {outputs: {artifact: dependencies.build.artifact}}
`)
	outputPath := filepath.Join(root, "github-output")
	summaryPath := filepath.Join(root, "github-summary")
	for _, path := range []string{outputPath, summaryPath} {
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	values := map[string]string{"GITHUB_OUTPUT": outputPath, "GITHUB_STEP_SUMMARY": summaryPath, "GITHUB_WORKSPACE": root}
	var terminal bytes.Buffer
	command := newRootCmd(dependencies{
		stdin: bytes.NewReader(nil), stdout: &terminal, stderr: &terminal,
		cwd: func() (string, error) { return root, nil }, homeDir: func() (string, error) { return filepath.Join(root, "home"), nil },
		configDir: func() (string, error) { return filepath.Join(root, "config"), nil }, registry: step.NewRegistry(),
		getenv: func(name string) string { return values[name] },
	})
	command.SetArgs([]string{"run", "release", "--reporter", "github"})
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "dist/app.tar.gz") {
		t.Fatalf("GITHUB_OUTPUT = %q", data)
	}
}

func TestRunCommandOrdersAndDeduplicatesDiamondDependencies(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".wuko", "workflows")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	definitions := map[string]string{
		"base":  "",
		"left":  "depends_on: {base: base}\n",
		"right": "depends_on: {base: base}\n",
		"root":  "depends_on: {left: left, right: right}\n",
	}
	for name, dependency := range definitions {
		writeWorkflowData(t, filepath.Join(dir, name+".yaml"), "version: 1\nname: "+name+"\n"+dependency+"steps:\n  - id: record\n    type: dependency_record\n    with: {label: "+name+"}\n")
	}
	var order []string
	registry := step.NewRegistry()
	if err := registry.Register("dependency_record", func(raw map[string]any) (step.Runner, error) {
		return dependencyRecordRunner{label: raw["label"].(string), order: &order}, nil
	}); err != nil {
		t.Fatal(err)
	}
	command := newRootCmd(dependencies{
		stdin: bytes.NewReader(nil), stdout: io.Discard, stderr: io.Discard,
		cwd: func() (string, error) { return root, nil }, homeDir: func() (string, error) { return t.TempDir(), nil },
		configDir: func() (string, error) { return t.TempDir(), nil }, registry: registry,
		getenv: func(string) string { return "" }, environment: func(context.Context, string) (map[string]string, error) { return map[string]string{}, nil },
	})
	command.SetArgs([]string{"run", "root", "--var", "marker=shared", "--env", "MODE=test"})
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(order, ","); got != "base:shared:test,left:shared:test,right:shared:test,root:shared:test" {
		t.Fatalf("order = %s", got)
	}
}

func TestReleaseDependencyPlanRemovesRetainedGraph(t *testing.T) {
	definition := &workflow.Definition{Name: "scheduled"}
	plans := map[*workflow.Definition]*workflow.DependencyPlan{
		definition: {Root: &workflow.DependencyNode{Definition: definition}},
	}
	released := false
	release := releaseDependencyPlan(plans, definition, func() { released = true })
	release()
	if len(plans) != 0 {
		t.Fatalf("plans = %#v, want empty", plans)
	}
	if !released {
		t.Fatal("underlying release was not called")
	}
}

type dependencyRecordRunner struct {
	label string
	order *[]string
}

func (runner dependencyRecordRunner) Run(_ context.Context, request step.Request) (step.Result, error) {
	*runner.order = append(*runner.order, runner.label+":"+request.Vars["marker"].(string)+":"+request.Env["MODE"])
	return step.Result{}, nil
}

func TestDocumentedDependencyExamplesValidateTreeAndDryRun(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", "examples", "dependencies"))
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		args []string
		want string
	}{
		{args: []string{"validate", "release"}, want: "release: valid"},
		{args: []string{"tree", "release"}, want: "prepare\n"},
		{args: []string{"run", "release", "--dry-run"}, want: "Workflow release"},
		{args: []string{"run", "nightly-release", "--once", "--dry-run"}, want: "Workflow nightly-release"},
	} {
		registry := step.NewRegistry()
		if err := setstep.Register(registry); err != nil {
			t.Fatal(err)
		}
		var output bytes.Buffer
		command := newRootCmd(dependencies{
			stdin: bytes.NewReader(nil), stdout: &output, stderr: &output,
			cwd: func() (string, error) { return root, nil }, homeDir: func() (string, error) { return t.TempDir(), nil },
			configDir: func() (string, error) { return t.TempDir(), nil }, registry: registry,
			getenv: func(string) string { return "" }, environment: func(context.Context, string) (map[string]string, error) { return map[string]string{}, nil },
		})
		command.SetOut(io.Writer(&output))
		command.SetErr(io.Writer(&output))
		command.SetArgs(test.args)
		if err := command.ExecuteContext(t.Context()); err != nil {
			t.Fatalf("%v: %v", test.args, err)
		}
		if !strings.Contains(output.String(), test.want) {
			t.Fatalf("%v output = %q, want %q", test.args, output.String(), test.want)
		}
	}
}
