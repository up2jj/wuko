package cmd

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spf13/cobra"
	envload "github.com/up2jj/wuko/environment"
	"github.com/up2jj/wuko/step"
	runworkflowstep "github.com/up2jj/wuko/steps/runworkflow"
)

type runWorkflowCaptureRunner struct {
	value string
	seen  *[]string
	mu    *sync.Mutex
}

func (runner runWorkflowCaptureRunner) Run(context.Context, step.Request) (step.Result, error) {
	if runner.mu != nil {
		runner.mu.Lock()
		defer runner.mu.Unlock()
	}
	*runner.seen = append(*runner.seen, runner.value)
	return step.Result{Outputs: map[string]any{"value": runner.value}}, nil
}

type runWorkflowFailRunner struct{}

func (runWorkflowFailRunner) Run(context.Context, step.Request) (step.Result, error) {
	return step.Result{}, errors.New("boom")
}

type runWorkflowEnvRunner struct{}

func (runWorkflowEnvRunner) Run(_ context.Context, request step.Request) (step.Result, error) {
	return step.Result{Outputs: map[string]any{
		"parent": request.Env["PARENT_ONLY"],
		"shared": request.Env["SHARED"],
	}}, nil
}

type runWorkflowConcurrency struct {
	active atomic.Int32
	max    atomic.Int32
	ready  chan struct{}
	once   sync.Once
}

func (tracker *runWorkflowConcurrency) run(ctx context.Context) error {
	active := tracker.active.Add(1)
	defer tracker.active.Add(-1)
	for {
		maximum := tracker.max.Load()
		if active <= maximum || tracker.max.CompareAndSwap(maximum, active) {
			break
		}
	}
	if active >= 2 {
		tracker.once.Do(func() { close(tracker.ready) })
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-tracker.ready:
	}
	timer := time.NewTimer(20 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

type runWorkflowConcurrencyRunner struct{ tracker *runWorkflowConcurrency }

func (runner runWorkflowConcurrencyRunner) Run(ctx context.Context, _ step.Request) (step.Result, error) {
	if err := runner.tracker.run(ctx); err != nil {
		return step.Result{}, err
	}
	return step.Result{}, nil
}

type runWorkflowResourceRunner struct{ cleaned *atomic.Bool }

func (runner runWorkflowResourceRunner) Run(context.Context, step.Request) (step.Result, error) {
	return step.Result{Outputs: map[string]any{"resource": "ready"}}, nil
}

func (runner runWorkflowResourceRunner) Cleanup(context.Context, step.Result) error {
	runner.cleaned.Store(true)
	return nil
}

type runWorkflowAssertCleanupRunner struct{ cleaned *atomic.Bool }

func (runner runWorkflowAssertCleanupRunner) Run(context.Context, step.Request) (step.Result, error) {
	if !runner.cleaned.Load() {
		return step.Result{}, errors.New("child cleanup has not completed")
	}
	return step.Result{}, nil
}

// runWorkflowCountingRunner counts how often the engine validates and runs it.
type runWorkflowCountingRunner struct {
	validated *atomic.Int32
	ran       *atomic.Int32
}

func (runner runWorkflowCountingRunner) Validate(context.Context, step.Request) error {
	runner.validated.Add(1)
	return nil
}

func (runner runWorkflowCountingRunner) Run(context.Context, step.Request) (step.Result, error) {
	runner.ran.Add(1)
	return step.Result{}, nil
}

func TestRunWorkflowValidatesOneCallShapeOnce(t *testing.T) {
	root, workflowDir := runWorkflowFixture(t)
	writeWorkflowData(t, filepath.Join(workflowDir, "child.yaml"), `version: 1
name: child
steps:
  - id: work
    type: run_workflow_count
    with: {}
`)
	writeWorkflowData(t, filepath.Join(workflowDir, "parent.yaml"), `version: 1
name: parent
vars: {items: [1, 2, 3]}
steps:
  - id: children
    foreach:
      items: vars.items
      steps:
        - id: call
          type: run_workflow
          with: {workflow: child}
`)

	var validated, ran atomic.Int32
	command := runWorkflowCommand(t, root, nil, func(registry *step.Registry) {
		if err := registry.Register("run_workflow_count", func(map[string]any) (step.Runner, error) {
			return runWorkflowCountingRunner{validated: &validated, ran: &ran}, nil
		}); err != nil {
			t.Fatal(err)
		}
	})
	command.SetArgs([]string{"run", "parent"})
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := ran.Load(); got != 3 {
		t.Fatalf("child runs = %d, want 3", got)
	}
	// Every iteration makes the same call, so the subtree is validated once for the whole run
	// rather than again before each invocation.
	if got := validated.Load(); got != 1 {
		t.Fatalf("child validations = %d, want 1", got)
	}
}

func TestRunWorkflowCommandReturnsTargetOutputsWithDependencies(t *testing.T) {
	root, workflowDir := runWorkflowFixture(t)
	writeWorkflowData(t, filepath.Join(workflowDir, "base.yaml"), `version: 1
name: base
invokable: false
outputs:
  prefix: {type: string, value: '"base"'}
steps:
  - return: {outputs: {prefix: '"base"'}}
`)
	writeWorkflowData(t, filepath.Join(workflowDir, "package.yaml"), `version: 1
name: package
vars: {revision: ""}
outputs:
  revision: {type: string, value: vars.revision}
steps:
  - return: {outputs: {revision: vars.revision}}
`)
	writeWorkflowData(t, filepath.Join(workflowDir, "build.yaml"), `version: 1
name: build
vars: {revision: "", nested: []}
targets:
  linux:
    depends_on: {base: base}
    outputs:
      artifact: {type: string, value: 'dependencies.base.prefix + ":" + steps.package.revision'}
    steps:
      - id: package
        type: run_workflow
        with:
          workflow: package
          vars: {revision: {expr: vars.revision}}
      - return: {outputs: {artifact: 'dependencies.base.prefix + ":" + steps.package.revision'}}
`)
	writeWorkflowData(t, filepath.Join(workflowDir, "parent.yaml"), `version: 1
name: parent
vars: {revision: abc123, parent_only: hidden}
env: {PARENT_ONLY: hidden}
steps:
  - id: build
    type: run_workflow
    with:
      workflow: build
      target: linux
      vars:
        revision: {expr: vars.revision}
        nested: [{literal: {expr: keep}}]
  - id: capture
    type: run_workflow_capture
    with: {value: '{{ .steps.build.artifact }}'}
`)

	var seen []string
	command := runWorkflowCommand(t, root, &seen)
	command.SetArgs([]string{"--debug", "run", "parent"})
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 1 || seen[0] != "base:abc123" {
		t.Fatalf("captured = %#v", seen)
	}
}

func TestRunWorkflowCommandFailureUsesStepPrefixAndTryCatchRecord(t *testing.T) {
	root, workflowDir := runWorkflowFixture(t)
	writeWorkflowData(t, filepath.Join(workflowDir, "child.yaml"), `version: 1
name: child
steps:
  - {id: fail, type: run_workflow_fail}
`)
	writeWorkflowData(t, filepath.Join(workflowDir, "parent.yaml"), `version: 1
name: parent
steps:
  - id: guarded
    try:
      steps:
        - {id: child, type: run_workflow, with: {workflow: child}}
    catch:
      steps:
        - id: capture
          type: run_workflow_capture
          with: {value: '{{ .error.step }}|{{ .error.type }}|{{ .error.message }}'}
`)

	var seen []string
	command := runWorkflowCommand(t, root, &seen)
	command.SetArgs([]string{"run", "parent"})
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 1 || !strings.HasPrefix(seen[0], "child|run_workflow|workflow \"parent\" step \"child\" (run_workflow): running workflow \"child\":") {
		t.Fatalf("catch record = %#v", seen)
	}
}

func TestRunWorkflowCommandRejectsInvocationCycle(t *testing.T) {
	root, workflowDir := runWorkflowFixture(t)
	writeWorkflowData(t, filepath.Join(workflowDir, "a.yaml"), `version: 1
name: a
steps:
  - {id: call_b, type: run_workflow, with: {workflow: b}}
`)
	writeWorkflowData(t, filepath.Join(workflowDir, "b.yaml"), `version: 1
name: b
steps:
  - {id: call_a, type: run_workflow, with: {workflow: a}}
`)
	command := runWorkflowCommand(t, root, nil)
	command.SetArgs([]string{"run", "a"})
	err := command.ExecuteContext(t.Context())
	if err == nil || !strings.Contains(err.Error(), "workflow invocation cycle: a -> b -> a") {
		t.Fatalf("error = %v", err)
	}
}

func TestRunWorkflowCommandValidatesStaticReference(t *testing.T) {
	tests := []struct {
		name   string
		child  string
		call   string
		wanted string
	}{
		{name: "missing workflow", call: `{workflow: missing}`, wanted: `workflow "missing" not found`},
		{name: "not invokable", child: "version: 1\nname: child\ninvokable: false\nsteps:\n  - return: {outputs: {}}\n", call: `{workflow: child}`, wanted: `workflow "child" is not directly invokable`},
		{name: "missing target", child: "version: 1\nname: child\ntargets:\n  linux:\n    steps:\n      - return: {outputs: {}}\n", call: `{workflow: child, target: darwin}`, wanted: `workflow "child" has no target "darwin"`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root, workflowDir := runWorkflowFixture(t)
			if test.child != "" {
				writeWorkflowData(t, filepath.Join(workflowDir, "child.yaml"), test.child)
			}
			writeWorkflowData(t, filepath.Join(workflowDir, "parent.yaml"), "version: 1\nname: parent\nsteps:\n  - {id: child, type: run_workflow, with: "+test.call+"}\n")
			command := runWorkflowCommand(t, root, nil)
			command.SetArgs([]string{"run", "parent"})
			err := command.ExecuteContext(t.Context())
			if err == nil || !strings.Contains(err.Error(), test.wanted) {
				t.Fatalf("error = %v, want containing %q", err, test.wanted)
			}
		})
	}
}

func TestRunWorkflowCommandDryRunDoesNotExecuteChild(t *testing.T) {
	root, workflowDir := runWorkflowFixture(t)
	writeWorkflowData(t, filepath.Join(workflowDir, "child.yaml"), `version: 1
name: child
steps:
  - {id: capture, type: run_workflow_capture, with: {value: child-ran}}
`)
	writeWorkflowData(t, filepath.Join(workflowDir, "parent.yaml"), `version: 1
name: parent
steps:
  - {id: child, type: run_workflow, with: {workflow: child}}
`)
	var seen []string
	var output bytes.Buffer
	command := runWorkflowCommand(t, root, &seen)
	command.SetOut(&output)
	command.SetArgs([]string{"run", "parent", "--dry-run"})
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 0 {
		t.Fatalf("child executed during dry-run: %#v", seen)
	}
	if !strings.Contains(output.String(), "child (run_workflow)") {
		t.Fatalf("dry-run output = %q", output.String())
	}
}

func TestRunWorkflowCommandRejectsStdinParent(t *testing.T) {
	root, workflowDir := runWorkflowFixture(t)
	writeWorkflowData(t, filepath.Join(workflowDir, "child.yaml"), "version: 1\nname: child\nsteps:\n  - return: {outputs: {}}\n")
	data := "version: 1\nname: parent\nsteps:\n  - {id: child, type: run_workflow, with: {workflow: child}}\n"
	command := runWorkflowCommand(t, root, nil)
	command.SetIn(strings.NewReader(data))
	command.SetArgs([]string{"run", "--file", "-"})
	err := command.ExecuteContext(t.Context())
	if err == nil || !strings.Contains(err.Error(), "only available to trusted local workflows") {
		t.Fatalf("error = %v", err)
	}
}

func TestRunWorkflowCommandDoesNotInheritParentWorkflowEnvironment(t *testing.T) {
	root, workflowDir := runWorkflowFixture(t)
	writeWorkflowData(t, filepath.Join(workflowDir, "child.yaml"), `version: 1
name: child
outputs:
  parent: {type: string, value: steps.probe.parent}
  shared: {type: string, value: steps.probe.shared}
steps:
  - {id: probe, type: run_workflow_env}
`)
	writeWorkflowData(t, filepath.Join(workflowDir, "parent.yaml"), `version: 1
name: parent
env: {PARENT_ONLY: hidden}
steps:
  - {id: child, type: run_workflow, with: {workflow: child}}
  - id: capture
    type: run_workflow_capture
    with: {value: '{{ .steps.child.parent }}|{{ .steps.child.shared }}'}
`)
	var seen []string
	command := runWorkflowCommand(t, root, &seen)
	command.SetArgs([]string{"run", "parent", "--env", "SHARED=cli"})
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 1 || seen[0] != "|cli" {
		t.Fatalf("captured = %#v", seen)
	}
}

func TestRunWorkflowInsideLocalActionDoesNotInheritScopedEnvironment(t *testing.T) {
	root, workflowDir := runWorkflowFixture(t)
	writeWorkflowData(t, filepath.Join(workflowDir, "child.yaml"), `version: 1
name: child
outputs:
  parent: {type: string, value: steps.probe.parent}
steps:
  - {id: probe, type: run_workflow_env}
`)
	actionDir := filepath.Join(root, "actions", "call-child")
	if err := os.MkdirAll(actionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeWorkflowData(t, filepath.Join(actionDir, "action.yaml"), `version: 1
name: call-child
outputs:
  parent: {value: steps.child.parent}
steps:
  - {id: child, type: run_workflow, with: {workflow: child}}
`)
	writeWorkflowData(t, filepath.Join(workflowDir, "parent.yaml"), `version: 1
name: parent
steps:
  - env: {PARENT_ONLY: scoped}
    steps:
      - id: call
        uses: ../../actions/call-child
  - id: capture
    type: run_workflow_capture
    with: {value: '{{ .steps.call.parent }}'}
`)
	var seen []string
	command := runWorkflowCommand(t, root, &seen)
	command.SetArgs([]string{"run", "parent"})
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 1 || seen[0] != "" {
		t.Fatalf("captured = %#v", seen)
	}
}

func TestRunWorkflowForeachBoundsActiveChildren(t *testing.T) {
	root, workflowDir := runWorkflowFixture(t)
	writeWorkflowData(t, filepath.Join(workflowDir, "child.yaml"), `version: 1
name: child
vars: {item: 0}
outputs:
  item: {type: number, value: vars.item}
steps:
  - {id: overlap, type: run_workflow_overlap}
`)
	writeWorkflowData(t, filepath.Join(workflowDir, "parent.yaml"), `version: 1
name: parent
vars: {items: [1, 2, 3, 4]}
steps:
  - id: children
    foreach:
      items: vars.items
      max_concurrency: 2
      collect: steps.child.item
      steps:
        - id: child
          type: run_workflow
          with:
            workflow: child
            vars: {item: {expr: foreach.item}}
`)
	tracker := &runWorkflowConcurrency{ready: make(chan struct{})}
	command := runWorkflowCommand(t, root, nil, func(registry *step.Registry) {
		if err := registry.Register("run_workflow_overlap", func(map[string]any) (step.Runner, error) {
			return runWorkflowConcurrencyRunner{tracker: tracker}, nil
		}); err != nil {
			t.Fatal(err)
		}
	})
	command.SetArgs([]string{"--debug", "run", "parent"})
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if maximum := tracker.max.Load(); maximum != 2 {
		t.Fatalf("maximum active children = %d, want 2", maximum)
	}
}

func TestRunWorkflowCompletesChildCleanupBeforeReturning(t *testing.T) {
	root, workflowDir := runWorkflowFixture(t)
	writeWorkflowData(t, filepath.Join(workflowDir, "child.yaml"), `version: 1
name: child
outputs:
  resource: {type: string, value: steps.resource.resource}
steps:
  - {id: resource, type: run_workflow_resource}
`)
	writeWorkflowData(t, filepath.Join(workflowDir, "parent.yaml"), `version: 1
name: parent
steps:
  - {id: child, type: run_workflow, with: {workflow: child}}
  - {id: assert_cleanup, type: run_workflow_assert_cleanup}
`)
	var cleaned atomic.Bool
	command := runWorkflowCommand(t, root, nil, func(registry *step.Registry) {
		if err := registry.Register("run_workflow_resource", func(map[string]any) (step.Runner, error) {
			return runWorkflowResourceRunner{cleaned: &cleaned}, nil
		}); err != nil {
			t.Fatal(err)
		}
		if err := registry.Register("run_workflow_assert_cleanup", func(map[string]any) (step.Runner, error) {
			return runWorkflowAssertCleanupRunner{cleaned: &cleaned}, nil
		}); err != nil {
			t.Fatal(err)
		}
	})
	command.SetArgs([]string{"run", "parent"})
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func runWorkflowFixture(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	directory := filepath.Join(root, ".wuko", "workflows")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	return root, directory
}

func runWorkflowCommand(t *testing.T, root string, seen *[]string, configure ...func(*step.Registry)) *cobra.Command {
	t.Helper()
	if seen == nil {
		seen = &[]string{}
	}
	var mu sync.Mutex
	registry := step.NewRegistry()
	if err := runworkflowstep.Register(registry); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register("run_workflow_capture", func(raw map[string]any) (step.Runner, error) {
		value, _ := raw["value"].(string)
		return runWorkflowCaptureRunner{value: value, seen: seen, mu: &mu}, nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register("run_workflow_fail", func(map[string]any) (step.Runner, error) {
		return runWorkflowFailRunner{}, nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register("run_workflow_env", func(map[string]any) (step.Runner, error) {
		return runWorkflowEnvRunner{}, nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, apply := range configure {
		apply(registry)
	}
	return newRootCmd(dependencies{
		stdin: bytes.NewReader(nil), stdout: io.Discard, stderr: io.Discard,
		cwd:       func() (string, error) { return root, nil },
		homeDir:   func() (string, error) { return filepath.Join(root, "home"), nil },
		configDir: func() (string, error) { return filepath.Join(root, "config"), nil },
		registry:  registry,
		getenv:    func(string) string { return "" },
		environment: envload.InvocationLoaderFunc(func(context.Context, string, map[string]string, envload.Policy) (envload.InvocationEnvironment, error) {
			return envload.InvocationEnvironment{Values: map[string]string{}}, nil
		}),
	})
}
