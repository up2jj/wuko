package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/spf13/cobra"
	"github.com/up2jj/wuko/engine"
	"github.com/up2jj/wuko/executor"
	"github.com/up2jj/wuko/internal/testfs"
	"github.com/up2jj/wuko/reference"
	"github.com/up2jj/wuko/step"
	devenvstep "github.com/up2jj/wuko/steps/devenv"
	"github.com/up2jj/wuko/workflow"
)

func builtinReference(t *testing.T) *reference.Catalog {
	t.Helper()
	r := step.NewRegistry()
	if err := registerBuiltinSteps(r); err != nil {
		t.Fatal(err)
	}
	c, err := reference.New(r.Definitions())
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestReferenceExamplesValidateAndRun(t *testing.T) {
	t.Parallel()
	c := builtinReference(t)
	for _, entry := range c.Steps() {
		item, err := c.Step(entry.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(item.Examples) == 0 || item.Examples[0].ID != "basic" {
			t.Fatalf("%s needs a basic example", item.ID)
		}
		seen := map[string]bool{}
		examples, err := c.Examples(item.ID)
		if err != nil {
			t.Fatal(err)
		}
		for _, example := range examples {
			if seen[example.ID] {
				t.Fatalf("duplicate %s/%s", item.ID, example.ID)
			}
			seen[example.ID] = true
			t.Run(item.ID+"/"+example.ID, func(t *testing.T) {
				t.Parallel()
				if example.Title == "" || example.Expected == "" || example.Description == "" {
					t.Fatal("incomplete example metadata")
				}
				dir := t.TempDir()
				for path, content := range example.Files {
					if !filepath.IsLocal(path) {
						t.Fatalf("non-local supporting file %q", path)
					}
					testfs.Write(t, filepath.Join(dir, path), content)
				}
				file := filepath.Join(dir, "workflow.yaml")
				testfs.Write(t, file, example.Workflow)
				definition, err := workflow.NewLoader(nil).Load(t.Context(), file, workflow.LoadOptions{RunDir: dir})
				if err != nil {
					t.Fatal(err)
				}
				registry := step.NewRegistry()
				if err := registerBuiltinSteps(registry); err != nil {
					t.Fatal(err)
				}
				executors := executor.NewRegistry()
				if err := devenvstep.RegisterExecutor(executors); err != nil {
					t.Fatal(err)
				}
				runner := workflowEngine(dependencies{registry: registry, executors: executors, cwd: func() (string, error) { return dir, nil }, homeDir: func() (string, error) { return dir, nil }, configDir: func() (string, error) { return dir, nil }, loader: workflow.NewLoader(nil), stdout: io.Discard, stderr: io.Discard})
				options := engine.Options{RunDir: dir, LocalValueDir: filepath.Join(dir, ".values"), GlobalValueDir: filepath.Join(dir, ".global-values"), Stdout: io.Discard, Stderr: io.Discard, ElevationAllowed: true}
				if err := runner.Validate(t.Context(), definition, options); err != nil {
					t.Fatal(err)
				}
				if example.Run {
					if _, err := runner.Run(t.Context(), definition, options); err != nil {
						t.Fatal(err)
					}
					for path, want := range example.ExpectedFiles {
						got, err := os.ReadFile(filepath.Join(dir, path))
						if err != nil {
							t.Fatal(err)
						}
						if string(got) != want {
							t.Errorf("%s = %q, want %q", path, got, want)
						}
					}
					for _, path := range example.AbsentFiles {
						if _, err := os.Lstat(filepath.Join(dir, path)); !os.IsNotExist(err) {
							t.Errorf("%s should be absent, stat: %v", path, err)
						}
					}
				}
			})
		}
	}
}

func TestHelpAndReferenceAreOffline(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{
		{"--help"}, {"run", "--help"}, {"help", "run", "--format", "json"},
		{"help", "plugin", "install", "--format", "json"}, {"reference"},
		{"reference", "step", "--format", "json"}, {"reference", "step", "shell", "--format", "json"},
		{"reference", "topic", "agents", "--format", "json"}, {"reference", "search", "write file", "--format", "json"},
		{"reference", "example", "set", "basic", "--format", "yaml"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			t.Parallel()
			var stdout, stderr bytes.Buffer
			fail := func() (string, error) { return "", fmt.Errorf("unexpected directory lookup") }
			root := newRootCmd(dependencies{stdout: &stdout, stderr: &stderr, cwd: fail, homeDir: fail, configDir: fail, isInteractive: func(io.Reader) bool { t.Fatal("unexpected terminal lookup"); return false }})
			root.SetArgs(args)
			if err := root.ExecuteContext(t.Context()); err != nil {
				t.Fatal(err)
			}
			if stdout.Len() == 0 || stderr.Len() != 0 {
				t.Fatalf("stdout %q, stderr %q", stdout.String(), stderr.String())
			}
		})
	}
	// A valid working directory containing malformed local defaults must also work.
	dir := t.TempDir()
	testfs.Write(t, filepath.Join(dir, "wuko.yml"), "[broken")
	root := newRootCmd(configTestDependencies(dir, io.Discard))
	root.SetArgs([]string{"reference", "step"})
	if err := root.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestReferenceErrorsLeaveStdoutEmpty(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{
		{"help", "missing", "--format", "json"}, {"help", "run", "extra", "--format", "json"},
		{"help", "__script-sdk", "--format", "json"}, {"help", "--format", "yaml"},
		{"reference", "step", "missing", "--format", "json"}, {"reference", "topic", "missing", "--format", "json"},
		{"reference", "example", "set", "missing", "--format", "json"}, {"reference", "step", "set", "--format", "yaml"},
		{"reference", "example", "import_vars", "basic", "--format", "yaml"}, {"reference", "search", "", "--format", "json"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			var out bytes.Buffer
			root := newRootCmd(dependencies{stdout: &out, stderr: io.Discard})
			root.SetArgs(args)
			if err := root.ExecuteContext(t.Context()); err == nil {
				t.Fatal("expected an error")
			}
			if out.Len() != 0 {
				t.Fatalf("partial output %q", out.String())
			}
		})
	}
}

func TestHelpTextAndJSONDescribeSameCommand(t *testing.T) {
	t.Parallel()
	for _, path := range [][]string{nil, {"run"}, {"plugin", "install"}, {"reference", "example"}} {
		var actual [2]string
		for i, args := range [][]string{append(append([]string{}, path...), "--help"), append([]string{"help"}, path...)} {
			var out bytes.Buffer
			root := newRootCmd(dependencies{stdout: &out, stderr: io.Discard})
			root.SetArgs(args)
			if err := root.ExecuteContext(t.Context()); err != nil {
				t.Fatal(err)
			}
			actual[i] = out.String()
		}
		if actual[0] != actual[1] {
			t.Fatalf("help variants differ for %v\n--help:\n%s\nhelp:\n%s", path, actual[0], actual[1])
		}
		var out bytes.Buffer
		root := newRootCmd(dependencies{stdout: &out, stderr: io.Discard})
		root.SetArgs(append(append([]string{"help"}, path...), "--format", "json"))
		if err := root.ExecuteContext(t.Context()); err != nil {
			t.Fatal(err)
		}
		var document struct {
			SchemaVersion int         `json:"schema_version"`
			Kind          string      `json:"kind"`
			Data          commandHelp `json:"data"`
		}
		if err := json.Unmarshal(out.Bytes(), &document); err != nil {
			t.Fatal(err)
		}
		if document.SchemaVersion != 1 || document.Kind != "command" || !strings.Contains(actual[0], document.Data.Usage) {
			t.Fatalf("unexpected document: %s", out.String())
		}
		if strings.Contains(out.String(), "__script-sdk") {
			t.Fatal("private command leaked")
		}
	}
}

func TestEveryPublicCommandHasUsefulHelp(t *testing.T) {
	t.Parallel()
	root := NewRootCmd()
	root.InitDefaultHelpCmd()
	var visit func(*cobra.Command)
	visit = func(cmd *cobra.Command) {
		if cmd.Hidden {
			return
		}
		doc := describeCommand(cmd)
		if doc.Summary == "" || len(doc.Examples) == 0 || doc.Behavior == "" {
			t.Errorf("incomplete help for %s", doc.Path)
		}
		if len(doc.Examples) == 1 && strings.HasSuffix(doc.Examples[0], " --help") {
			t.Errorf("%s needs an operation example", doc.Path)
		}
		for _, flag := range append(doc.Flags, doc.GlobalFlags...) {
			if flag.Description == "" {
				t.Errorf("undocumented flag %s --%s", doc.Path, flag.Name)
			}
		}
		for _, child := range cmd.Commands() {
			visit(child)
		}
	}
	visit(root)
}

func TestHelpGolden(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		args []string
	}{{"root", []string{"--help"}}, {"run", []string{"run", "--help"}}, {"reference-example", []string{"reference", "example", "--help"}}} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var out bytes.Buffer
			root := newRootCmd(dependencies{stdout: &out, stderr: io.Discard})
			root.SetArgs(test.args)
			if err := root.ExecuteContext(t.Context()); err != nil {
				t.Fatal(err)
			}
			want, err := os.ReadFile(filepath.Join("testdata", "help", test.name+".txt"))
			if err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(string(want), out.String()); diff != "" {
				t.Fatalf("help (-want +got):\n%s", diff)
			}
		})
	}
}

func TestHelpCanonicalAliasesAndInheritedFlags(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	root := newRootCmd(dependencies{stdout: &out, stderr: io.Discard})
	run, _, err := root.Find([]string{"run"})
	if err != nil {
		t.Fatal(err)
	}
	run.Aliases = []string{"execute"}
	root.SetArgs([]string{"help", "execute", "--format", "json"})
	if err := root.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		Data commandHelp `json:"data"`
	}
	if err := json.Unmarshal(out.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	doc := envelope.Data
	if doc.Path != "wuko run" || !slices.Contains(doc.Aliases, "execute") {
		t.Fatalf("alias lost canonical command: %#v", doc)
	}
	if !slices.ContainsFunc(doc.Flags, func(f flagHelp) bool { return f.Name == "file" }) || !slices.ContainsFunc(doc.GlobalFlags, func(f flagHelp) bool { return f.Name == "no-config" }) {
		t.Fatal("local and inherited flags missing")
	}
	if slices.ContainsFunc(doc.Flags, func(f flagHelp) bool { return f.Name == "no-config" }) {
		t.Fatal("inherited flag duplicated as local")
	}
	rootDoc := describeCommand(root)
	if slices.ContainsFunc(rootDoc.Flags, func(f flagHelp) bool { return f.Name == "report-json" }) {
		t.Fatal("conditional root run flag exposed")
	}
}

func TestAgentAuthoringDiscoveryAndReport(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	invoke := func(args ...string) string {
		t.Helper()
		var out, stderr bytes.Buffer
		registry := step.NewRegistry()
		if err := registerBuiltinSteps(registry); err != nil {
			t.Fatal(err)
		}
		root := newRootCmd(dependencies{registry: registry, stdout: &out, stderr: &stderr, cwd: func() (string, error) { return dir, nil }, homeDir: func() (string, error) { return dir, nil }, configDir: func() (string, error) { return dir, nil }})
		root.SetArgs(args)
		if err := root.ExecuteContext(t.Context()); err != nil {
			t.Fatalf("%v: %v\n%s", args, err, stderr.String())
		}
		return out.String()
	}
	for _, args := range [][]string{{"help", "run", "--format", "json"}, {"reference", "step", "set", "--format", "json"}, {"reference", "example", "set", "--format", "json"}} {
		var envelope struct {
			SchemaVersion int `json:"schema_version"`
		}
		if err := json.Unmarshal([]byte(invoke(args...)), &envelope); err != nil {
			t.Fatal(err)
		}
		if envelope.SchemaVersion != 1 {
			t.Fatal("unsupported discovery schema")
		}
	}
	workflowYAML := invoke("reference", "example", "set", "basic", "--format", "yaml")
	file := filepath.Join(dir, "workflow.yaml")
	testfs.Write(t, file, workflowYAML)
	invoke("--no-config", "--env-loader", "none", "validate", "--file", file, "--format", "json")
	invoke("--no-config", "--env-loader", "none", "tree", "--file", file)
	report := invoke("--no-config", "--env-loader", "none", "run", "--file", file, "--once", "--report-json", "-")
	var result struct {
		SchemaVersion int    `json:"schema_version"`
		Status        string `json:"status"`
		DryRun        bool   `json:"dry_run"`
	}
	if err := json.Unmarshal([]byte(report), &result); err != nil {
		t.Fatal(err)
	}
	if result.SchemaVersion != 2 || result.Status != "succeeded" || result.DryRun {
		t.Fatalf("unexpected execution report: %s", report)
	}
}

// Ensure the help layer does not invoke a registry resolver supplied by an embedder.
func TestReferenceDoesNotResolveRuntimeSteps(t *testing.T) {
	t.Parallel()
	r := step.NewRegistry(step.WithResolver(func(context.Context, string, map[string]any) (step.Runner, error) {
		t.Fatal("resolver called")
		return nil, nil
	}))
	root := newRootCmd(dependencies{registry: r, stdout: io.Discard, stderr: io.Discard})
	root.SetArgs([]string{"reference", "step", "shell"})
	if err := root.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
}
