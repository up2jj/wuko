package cmd

import (
	"bytes"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/workflow"
)

func TestTreeCommandDisplaysWorkflowAndRemoteActionSteps(t *testing.T) {
	root := t.TempDir()
	workflowDir := filepath.Join(root, ".wuko", "workflows")
	if err := os.MkdirAll(workflowDir, 0o755); err != nil {
		t.Fatal(err)
	}
	definition := `version: 1
name: release
vars: {release: default}
steps:
  - id: test
    type: shell
  - id: build
    uses: https://actions.example.test/{{ .vars.release }}/build?token=secret
  - id: publish
    type: shell
    if: steps.build.ready
`
	if err := os.WriteFile(filepath.Join(workflowDir, "release.yaml"), []byte(definition), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "variables.json"), []byte(`{"release":"v1"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	action := `version: 1
name: build
steps:
  - id: compile
    type: shell
  - id: package
    type: shell
`
	client := &http.Client{Transport: commandRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.String() != "https://actions.example.test/v1/build?token=secret" {
			t.Fatalf("action URL = %q", request.URL)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(action)),
			Request:    request,
		}, nil
	})}
	var output bytes.Buffer
	command := newRootCmd(dependencies{
		stdin: bytes.NewReader(nil), stdout: &output, stderr: &output,
		cwd:       func() (string, error) { return root, nil },
		homeDir:   func() (string, error) { return filepath.Join(root, "home"), nil },
		configDir: func() (string, error) { return filepath.Join(root, "config"), nil },
		registry:  step.NewRegistry(), loader: workflow.NewLoader(client),
	})
	command.SetArgs([]string{"tree", "release", "--var-file", "variables.json"})
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	want := `release
├── test (shell)
├── build (uses https://actions.example.test/v1/build)
│   ├── compile (shell)
│   └── package (shell)
└── publish (shell) if: steps.build.ready
`
	if output.String() != want {
		t.Fatalf("output = %q, want %q", output.String(), want)
	}
}

func TestTreeCommandSupportsFileAndRemoteWorkflowSelectors(t *testing.T) {
	for _, test := range []struct {
		name string
		args func(string) []string
	}{
		{name: "file", args: func(path string) []string { return []string{"tree", "--file", path} }},
		{name: "remote", args: func(string) []string { return []string{"tree", "https://workflows.example.test/release.yaml"} }},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "workflow.yaml")
			data := "version: 1\nname: selected\nsteps:\n  - id: run\n    type: shell\n"
			if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
				t.Fatal(err)
			}
			client := &http.Client{Transport: commandRoundTripFunc(func(request *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Header: make(http.Header), Body: io.NopCloser(strings.NewReader(data)), Request: request}, nil
			})}
			var output bytes.Buffer
			command := newRootCmd(dependencies{
				stdin: bytes.NewReader(nil), stdout: &output, stderr: &output,
				cwd:       func() (string, error) { return root, nil },
				homeDir:   func() (string, error) { return filepath.Join(root, "home"), nil },
				configDir: func() (string, error) { return filepath.Join(root, "config"), nil },
				registry:  step.NewRegistry(), loader: workflow.NewLoader(client),
			})
			command.SetArgs(test.args(path))
			if err := command.ExecuteContext(t.Context()); err != nil {
				t.Fatal(err)
			}
			if output.String() != "selected\n└── run (shell)\n" {
				t.Fatalf("output = %q", output.String())
			}
		})
	}
}

func TestWriteDependencyPlanTreeShowsFullChainAndSharedDependencies(t *testing.T) {
	prepare := &workflow.DependencyNode{Definition: &workflow.Definition{
		Name:  "prepare",
		Steps: []workflow.Step{{ID: "workspace", Type: "shell"}},
	}}
	build := &workflow.DependencyNode{
		Definition:   &workflow.Definition{Name: "build", Steps: []workflow.Step{{ID: "compile", Type: "shell"}}},
		Dependencies: map[string]*workflow.DependencyNode{"prepare": prepare},
	}
	checks := &workflow.DependencyNode{
		Definition:   &workflow.Definition{Name: "checks", Steps: []workflow.Step{{ID: "test", Type: "shell"}}},
		Dependencies: map[string]*workflow.DependencyNode{"prepare": prepare},
	}
	release := &workflow.DependencyNode{
		Definition: &workflow.Definition{Name: "release", Steps: []workflow.Step{{ID: "publish", Type: "shell"}}},
		Dependencies: map[string]*workflow.DependencyNode{
			"artifacts": build,
			"checks":    checks,
		},
	}
	plan := &workflow.DependencyPlan{Root: release, Order: []*workflow.DependencyNode{prepare, build, checks, release}}

	var output bytes.Buffer
	if err := writeDependencyPlanTree(&output, plan); err != nil {
		t.Fatal(err)
	}
	want := `release
├── depends_on
│   ├── artifacts (build)
│   │   ├── depends_on
│   │   │   └── prepare
│   │   │       └── workspace (shell)
│   │   └── compile (shell)
│   └── checks
│       ├── depends_on
│       │   └── prepare (shared; shown above)
│       └── test (shell)
└── publish (shell)
`
	if output.String() != want {
		t.Fatalf("output = %q, want %q", output.String(), want)
	}
}

func TestTreeCommandRejectsMissingOrConflictingWorkflowSelector(t *testing.T) {
	for _, args := range [][]string{{"tree"}, {"tree", "name", "--file", "workflow.yaml"}} {
		command := newRootCmd(dependencies{
			stdin: bytes.NewReader(nil), stdout: io.Discard, stderr: io.Discard,
			cwd:       func() (string, error) { return t.TempDir(), nil },
			homeDir:   func() (string, error) { return "", nil },
			configDir: func() (string, error) { return "", nil },
			registry:  step.NewRegistry(),
		})
		command.SetArgs(args)
		if err := command.ExecuteContext(t.Context()); err == nil {
			t.Fatalf("args %v: expected error", args)
		}
	}
}

func TestWorkflowTreeDisplaysExecutionPolicy(t *testing.T) {
	timeout := workflow.Duration(2 * time.Minute)
	definition := &workflow.Definition{Name: "release", Steps: []workflow.Step{{
		ID: "publish", Type: "shell", Timeout: &timeout,
		Retry: &workflow.RetryPolicy{MaxAttempts: 4, BackoffMultiplier: 1, MaxElapsedTime: workflow.Duration(6 * time.Minute)},
	}}}
	var output bytes.Buffer
	if err := writeWorkflowTree(&output, definition); err != nil {
		t.Fatal(err)
	}
	if want := "release\n└── publish (shell) [timeout 2m0s, 4 attempts within 6m0s]\n"; output.String() != want {
		t.Fatalf("output = %q, want %q", output.String(), want)
	}
}

func TestWorkflowTreeDisplaysOnceBlock(t *testing.T) {
	t.Parallel()
	definition := &workflow.Definition{Name: "migration", Steps: []workflow.Step{{
		ID: "migrate", If: "vars.enabled", Once: &workflow.OnceGroup{
			Key: "schema-v1", Scope: "local", OnBusy: workflow.OnceBusyError,
			Steps: []workflow.Step{{ID: "apply", Type: "shell"}},
		},
	}}}
	var output bytes.Buffer
	if err := writeWorkflowTree(&output, definition); err != nil {
		t.Fatal(err)
	}
	want := "migration\n└── migrate (once schema-v1; local; on busy error) if: vars.enabled\n    └── apply (shell)\n"
	if output.String() != want {
		t.Fatalf("tree output = %q, want %q", output.String(), want)
	}
}

func TestWriteWorkflowTreeShowsFinallySections(t *testing.T) {
	action := &workflow.Action{
		Version: 1, Name: "action",
		Steps:   []workflow.Step{{ID: "inside", Type: "shell"}},
		Finally: []workflow.Step{{ID: "inside_cleanup", Type: "shell"}},
	}
	definition := &workflow.Definition{
		Version: 1, Name: "tree",
		Steps:   []workflow.Step{{ID: "call", Uses: workflow.ActionSource{URL: "https://example.test/action"}, Action: action}},
		Finally: []workflow.Step{{ID: "cleanup", Type: "shell"}},
	}
	var output bytes.Buffer
	if err := writeWorkflowTree(&output, definition); err != nil {
		t.Fatal(err)
	}
	want := "tree\n├── call (uses https://example.test/action)\n│   ├── inside (shell)\n│   └── finally\n│       └── inside_cleanup (shell)\n└── finally\n    └── cleanup (shell)\n"
	if output.String() != want {
		t.Fatalf("tree output = %q, want %q", output.String(), want)
	}
}

func TestWriteWorkflowTreeShowsAttachedDefer(t *testing.T) {
	definition := &workflow.Definition{Version: 1, Name: "tree", Steps: []workflow.Step{{
		ID: "create", Type: "shell", Defer: []workflow.Step{{ID: "remove", Type: "shell"}},
	}}}
	var output bytes.Buffer
	if err := writeWorkflowTree(&output, definition); err != nil {
		t.Fatal(err)
	}
	want := "tree\n└── create (shell)\n    └── defer\n        └── remove (shell)\n"
	if output.String() != want {
		t.Fatalf("tree output = %q, want %q", output.String(), want)
	}
}

func TestWorkflowTreeDisplaysCancelOnParticipants(t *testing.T) {
	definition := &workflow.Definition{Version: 1, Name: "deploy", Steps: []workflow.Step{{
		ID: "deployment_watch", CancelOn: &workflow.CancelOnGroup{
			Monitors: []workflow.Step{
				{ID: "ready", Type: "wait"},
				{ID: "service_checks", Concurrent: &workflow.ConcurrentGroup{MaxConcurrency: 2, Steps: []workflow.Step{{ID: "api", Type: "http"}, {ID: "worker", Type: "http"}}}},
			},
			Steps:   []workflow.Step{{ID: "deploy", Type: "shell"}},
			Collect: `cancel_on.winner.monitor`,
		},
	}}}
	var output bytes.Buffer
	if err := writeWorkflowTree(&output, definition); err != nil {
		t.Fatal(err)
	}
	want := "deploy\n└── deployment_watch (cancel_on collect cancel_on.winner.monitor)\n    ├── monitors\n    │   ├── ready (wait)\n    │   └── service_checks (concurrent [max 2, wait for all])\n    │       ├── api (http)\n    │       └── worker (http)\n    └── steps\n        └── deploy (shell)\n"
	if output.String() != want {
		t.Fatalf("tree output = %q, want %q", output.String(), want)
	}
}

func TestWorkflowTreeDisplaysConcurrentGroup(t *testing.T) {
	timeout := workflow.Duration(5 * time.Minute)
	definition := &workflow.Definition{Name: "checks", Steps: []workflow.Step{{Concurrent: &workflow.ConcurrentGroup{
		MaxConcurrency: 2, Timeout: &timeout, FailFast: false,
		Steps: []workflow.Step{{ID: "lint", Type: "shell"}, {ID: "test", Type: "shell"}},
	}}}}
	var output bytes.Buffer
	if err := writeWorkflowTree(&output, definition); err != nil {
		t.Fatal(err)
	}
	want := `checks
└── concurrent [max 2, timeout 5m0s, wait for all]
    ├── lint (shell)
    └── test (shell)
`
	if output.String() != want {
		t.Fatalf("output = %q, want %q", output.String(), want)
	}
}

func TestWorkflowTreeDisplaysConditionalBlock(t *testing.T) {
	definition := &workflow.Definition{Name: "conditional", Steps: []workflow.Step{
		{If: "vars.deploy", Steps: []workflow.Step{
			{ID: "build", Type: "shell"},
			{ID: "deploy", Type: "shell", If: "steps.build.exit_code == 0"},
		}},
		{ID: "notify", Type: "shell"},
	}}
	var output bytes.Buffer
	if err := writeWorkflowTree(&output, definition); err != nil {
		t.Fatal(err)
	}
	want := `conditional
├── if: vars.deploy
│   ├── build (shell)
│   └── deploy (shell) if: steps.build.exit_code == 0
└── notify (shell)
`
	if output.String() != want {
		t.Fatalf("output = %q, want %q", output.String(), want)
	}
}

func TestWorkflowTreeDisplaysWorkingDirectoryBlock(t *testing.T) {
	definition := &workflow.Definition{Name: "scoped", Steps: []workflow.Step{
		{WorkingDirectory: "./backend", Steps: []workflow.Step{
			{ID: "build", Type: "shell"},
			{WorkingDirectory: "nested", Steps: []workflow.Step{{ID: "test", Type: "shell"}}},
		}},
		{ID: "notify", Type: "shell"},
	}}
	var output bytes.Buffer
	if err := writeWorkflowTree(&output, definition); err != nil {
		t.Fatal(err)
	}
	want := `scoped
├── working_directory: ./backend
│   ├── build (shell)
│   └── working_directory: nested
│       └── test (shell)
└── notify (shell)
`
	if output.String() != want {
		t.Fatalf("output = %q, want %q", output.String(), want)
	}
}

func TestWorkflowTreeDisplaysFanoutControls(t *testing.T) {
	definition := &workflow.Definition{Name: "fanout", Steps: []workflow.Step{
		{ID: "uploads", Batch: &workflow.BatchGroup{
			Items: "vars.files", Size: workflow.BatchSize{Expression: "vars.batch_size"}, Collect: "steps.upload.stdout", MaxConcurrency: 4, FailFast: true,
			Steps: []workflow.Step{{ID: "upload", Type: "shell"}},
		}},
		{ID: "deploy", If: "vars.deploy", Foreach: &workflow.ForeachGroup{
			Items: "vars.targets", Collect: "steps.run.url", MaxConcurrency: 1, FailFast: true,
			Steps: []workflow.Step{{ID: "run", Type: "shell"}},
		}},
		{ID: "checks", Matrix: &workflow.MatrixGroup{
			Axes:           workflow.MatrixAxes{{Name: "os", Values: []any{"linux"}}, {Name: "go", Values: []any{"1.26"}}},
			Collect:        `{"os": matrix.os, "path": steps.test.path}`,
			MaxConcurrency: 2, FailFast: false,
			Steps: []workflow.Step{{ID: "test", Type: "shell"}},
		}},
	}}
	var output bytes.Buffer
	if err := writeWorkflowTree(&output, definition); err != nil {
		t.Fatal(err)
	}
	want := `fanout
├── uploads (batch vars.files by vars.batch_size; collect steps.upload.stdout) [max 4, max 10000 iterations, fail fast]
│   └── upload (shell)
├── deploy (foreach vars.targets; collect steps.run.url) [max 1, max 10000 iterations, fail fast] if: vars.deploy
│   └── run (shell)
└── checks (matrix os × go; collect {"os": matrix.os, "path": steps.test.path}) [max 2, max 10000 iterations, wait for all]
    └── test (shell)
`
	if output.String() != want {
		t.Fatalf("output = %q, want %q", output.String(), want)
	}
}

func TestWorkflowTreeDisplaysReturnControl(t *testing.T) {
	definition := &workflow.Definition{Name: "cached", Steps: []workflow.Step{
		{Return: &workflow.ReturnControl{Outputs: map[string]string{"cached": "true", "artifact": "steps.build.path"}}, If: "vars.cached"},
		{ID: "build", Type: "shell"},
	}}
	var output bytes.Buffer
	if err := writeWorkflowTree(&output, definition); err != nil {
		t.Fatal(err)
	}
	want := `cached
├── return (outputs: artifact, cached) if: vars.cached
└── build (shell)
`
	if output.String() != want {
		t.Fatalf("output = %q, want %q", output.String(), want)
	}
}
