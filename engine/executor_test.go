package engine

import (
	"context"
	"errors"
	"io"
	"maps"
	"strings"
	"testing"

	"github.com/up2jj/wuko/executor"
	"github.com/up2jj/wuko/process"
	"github.com/up2jj/wuko/step"
	gitstep "github.com/up2jj/wuko/steps/git"
	requiretoolstep "github.com/up2jj/wuko/steps/require_tool"
	"github.com/up2jj/wuko/steps/shell"
	"github.com/up2jj/wuko/workflow"
)

type recordingExecutor struct {
	commands     []string
	environments []map[string]string
	closed       int
	fail         string
}

func (executor *recordingExecutor) Run(_ context.Context, options process.Options) (process.Result, error) {
	executor.commands = append(executor.commands, options.Command+" "+strings.Join(options.Args, " "))
	executor.environments = append(executor.environments, maps.Clone(options.Env))
	result := process.Result{Stdout: options.Command + "-output", ExitCode: 0}
	if options.Command == executor.fail {
		result.ExitCode = 7
		return result, &process.ExitError{Command: options.Command, Code: 7}
	}
	return result, nil
}

func TestExecutorScopeReceivesEnvironmentBlock(t *testing.T) {
	scoped := &recordingExecutor{}
	definition := testDefinition(t, "env-executor", workflow.Step{
		Env: workflow.Environment{"MODE": "scoped"}, Steps: []workflow.Step{{
			Executor: &workflow.ExecutorScope{Type: "recording", With: map[string]any{}},
			Steps:    []workflow.Step{{ID: "run", Type: "shell", With: map[string]any{"command": "printenv", "args": []any{"MODE"}}}},
		}},
	})
	if _, err := executorTestEngine(t, scoped).Run(t.Context(), definition, Options{
		BaseEnv: map[string]string{"MODE": "outer"}, RunDir: t.TempDir(), Stdout: io.Discard, Stderr: io.Discard,
	}); err != nil {
		t.Fatal(err)
	}
	if len(scoped.environments) != 1 || scoped.environments[0]["MODE"] != "scoped" {
		t.Fatalf("executor environments = %#v", scoped.environments)
	}
}

func TestExecutorScopePreservesInvocationEnvironmentLoaders(t *testing.T) {
	scoped := &recordingExecutor{}
	definition := testDefinition(t, "loader-executor", workflow.Step{
		Executor: &workflow.ExecutorScope{Type: "recording", With: map[string]any{}},
		Steps: []workflow.Step{{ID: "run", Type: "shell", If: `"direnv" in run.environment_loaders`, With: map[string]any{
			"command": "echo", "args": []any{"{{ index .run.environment_loaders 0 }}"},
		}}},
	})
	if _, err := executorTestEngine(t, scoped).Run(t.Context(), definition, Options{
		EnvironmentLoaders: []string{"direnv"}, RunDir: t.TempDir(), Stdout: io.Discard, Stderr: io.Discard,
	}); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(scoped.commands, ","); got != "echo direnv" {
		t.Fatalf("commands = %q", got)
	}
}

func (executor *recordingExecutor) Close(context.Context) error {
	executor.closed++
	return nil
}

type recordingProvider struct{ session *recordingExecutor }

func (provider recordingProvider) Open(context.Context, executor.Request) (executor.Session, error) {
	return provider.session, nil
}

func executorTestEngine(t *testing.T, session *recordingExecutor) *Engine {
	t.Helper()
	steps := step.NewRegistry()
	if err := shell.Register(steps); err != nil {
		t.Fatal(err)
	}
	if err := requiretoolstep.Register(steps); err != nil {
		t.Fatal(err)
	}
	if err := gitstep.Register(steps); err != nil {
		t.Fatal(err)
	}
	executors := executor.NewRegistry()
	if err := executors.Register("recording", func(map[string]any) (executor.Provider, error) {
		return recordingProvider{session: session}, nil
	}); err != nil {
		t.Fatal(err)
	}
	return New(steps, WithExecutors(executors))
}

func TestExecutorScopeSupportsRequireTool(t *testing.T) {
	scoped := &recordingExecutor{}
	definition := testDefinition(t, "tools", workflow.Step{
		Executor: &workflow.ExecutorScope{Type: "recording", With: map[string]any{}},
		Steps: []workflow.Step{{ID: "tool", Type: "require_tool", With: map[string]any{
			"tool": "go", "version_args": []any{"version"},
		}}},
	})
	state, err := executorTestEngine(t, scoped).Run(t.Context(), definition, Options{
		RunDir: t.TempDir(), Stdout: io.Discard, Stderr: io.Discard,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(scoped.commands, ","); got != "go version" {
		t.Fatalf("scoped commands = %q", got)
	}
	if state.Steps["tool"].(map[string]any)["path"] != "go" {
		t.Fatalf("steps = %#v", state.Steps)
	}
}

func TestExecutorScopeCommitsAllowedShellExitForLaterCondition(t *testing.T) {
	scoped := &recordingExecutor{fail: "probe"}
	definition := testDefinition(t, "probe", workflow.Step{
		Executor: &workflow.ExecutorScope{Type: "recording", With: map[string]any{}},
		Steps: []workflow.Step{
			{ID: "probe", Type: "shell", With: map[string]any{
				"command": "probe", "allowed_exit_codes": []any{0, 7},
			}},
			{ID: "fallback", Type: "shell", If: "steps.probe.exit_code == 7", With: map[string]any{"command": "fallback"}},
		},
	})
	state, err := executorTestEngine(t, scoped).Run(t.Context(), definition, Options{
		RunDir: t.TempDir(), Stdout: io.Discard, Stderr: io.Discard,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(scoped.commands, ","); got != "probe ,fallback " {
		t.Fatalf("scoped commands = %q", got)
	}
	if state.Steps["probe"].(map[string]any)["exit_code"] != 7 {
		t.Fatalf("steps = %#v", state.Steps)
	}
}

func TestExecutorScopeSupportsGitAssertions(t *testing.T) {
	scoped := &recordingExecutor{}
	definition := testDefinition(t, "git", workflow.Step{
		Executor: &workflow.ExecutorScope{Type: "recording", With: map[string]any{}},
		Steps: []workflow.Step{{ID: "branch", Type: "git_branch", With: map[string]any{
			"operation": "assert", "branch": "main", "exists": true,
		}}},
	})
	if _, err := executorTestEngine(t, scoped).Run(t.Context(), definition, Options{
		RunDir: t.TempDir(), Stdout: io.Discard, Stderr: io.Discard,
	}); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(scoped.commands, ","); got != "git show-ref --verify --quiet refs/heads/main" {
		t.Fatalf("scoped commands = %q", got)
	}
}

func TestExecutorScopeSupportsGitCommit(t *testing.T) {
	scoped := &recordingExecutor{}
	definition := testDefinition(t, "git-commit", workflow.Step{
		Executor: &workflow.ExecutorScope{Type: "recording", With: map[string]any{}},
		Steps: []workflow.Step{{ID: "commit", Type: "git_commit", With: map[string]any{
			"message": "automated commit", "on_empty": "commit", "verify": false,
		}}},
	})
	state, err := executorTestEngine(t, scoped).Run(t.Context(), definition, Options{
		RunDir: t.TempDir(), Stdout: io.Discard, Stderr: io.Discard,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(scoped.commands, ","); got != "git diff --cached --quiet --exit-code --,git commit --allow-empty --no-verify -m automated commit,git rev-parse --verify --quiet HEAD^{commit}" {
		t.Fatalf("scoped commands = %q", got)
	}
	outputs := state.Steps["commit"].(map[string]any)
	if outputs["created"] != true || outputs["commit"] != "git-output" {
		t.Fatalf("commit outputs = %#v", outputs)
	}
}

func TestExecutorScopeRestoresParentAndSharesOutputs(t *testing.T) {
	scoped := &recordingExecutor{}
	local := &recordingExecutor{}
	definition := testDefinition(t, "mixed",
		workflow.Step{Executor: &workflow.ExecutorScope{Type: "recording", With: map[string]any{}},
			Steps:   []workflow.Step{{ID: "build", Type: "shell", With: map[string]any{"command": "container-build"}}},
			Finally: []workflow.Step{{ID: "clean", Type: "shell", With: map[string]any{"command": "container-clean"}}}},
		workflow.Step{ID: "package", Type: "shell", With: map[string]any{"command": "local-package", "args": []any{"{{ .steps.build.stdout }}"}}},
	)
	state, err := executorTestEngine(t, scoped).Run(t.Context(), definition, Options{RunDir: t.TempDir(), Executor: local, Stdout: io.Discard, Stderr: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(scoped.commands, ","); got != "container-build ,container-clean " {
		t.Fatalf("scoped commands = %q", got)
	}
	if got := strings.Join(local.commands, ","); got != "local-package container-build-output" {
		t.Fatalf("local commands = %q", got)
	}
	if scoped.closed != 1 || state.Steps["build"].(map[string]any)["stdout"] != "container-build-output" {
		t.Fatalf("closed = %d, state = %#v", scoped.closed, state.Steps)
	}
}

func TestExecutorScopeRunsFinallyAfterFailure(t *testing.T) {
	scoped := &recordingExecutor{fail: "fail"}
	definition := testDefinition(t, "cleanup",
		workflow.Step{Executor: &workflow.ExecutorScope{Type: "recording", With: map[string]any{}},
			Steps:   []workflow.Step{{ID: "work", Type: "shell", With: map[string]any{"command": "fail"}}},
			Finally: []workflow.Step{{ID: "clean", Type: "shell", With: map[string]any{"command": "clean"}}}},
	)
	state, err := executorTestEngine(t, scoped).Run(t.Context(), definition, Options{RunDir: t.TempDir(), Stdout: io.Discard, Stderr: io.Discard})
	if err == nil || !errors.As(err, new(*process.ExitError)) {
		t.Fatalf("error = %v", err)
	}
	if state != nil {
		t.Fatalf("failed state = %#v", state)
	}
	if got := strings.Join(scoped.commands, ","); got != "fail ,clean " || scoped.closed != 1 {
		t.Fatalf("commands = %q, closed = %d", got, scoped.closed)
	}
}

func TestExecutorScopeRunsDeferBeforeFinallyAndClose(t *testing.T) {
	scoped := &recordingExecutor{}
	definition := testDefinition(t, "defer-cleanup",
		workflow.Step{
			Executor: &workflow.ExecutorScope{Type: "recording", With: map[string]any{}},
			Steps: []workflow.Step{{
				ID: "create", Type: "shell", With: map[string]any{"command": "create"},
				Defer: []workflow.Step{{ID: "defer_clean", Type: "shell", With: map[string]any{"command": "defer-clean"}}},
			}},
			Finally: []workflow.Step{{ID: "final_clean", Type: "shell", With: map[string]any{"command": "final-clean"}}},
		},
	)
	if _, err := executorTestEngine(t, scoped).Run(t.Context(), definition, Options{RunDir: t.TempDir(), Stdout: io.Discard, Stderr: io.Discard}); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(scoped.commands, ","); got != "create ,defer-clean ,final-clean " || scoped.closed != 1 {
		t.Fatalf("commands = %q, closed = %d", got, scoped.closed)
	}
}

func TestExecutorScopeRejectsNonAwareRunner(t *testing.T) {
	steps := newTestRegistry(t, map[string]step.Builder{"host_only": func(map[string]any) (step.Runner, error) {
		return countingRunner{}, nil
	}})
	executors := executor.NewRegistry()
	if err := executors.Register("recording", func(map[string]any) (executor.Provider, error) {
		return recordingProvider{session: &recordingExecutor{}}, nil
	}); err != nil {
		t.Fatal(err)
	}
	definition := testDefinition(t, "invalid", workflow.Step{
		Executor: &workflow.ExecutorScope{Type: "recording", With: map[string]any{}},
		Steps:    []workflow.Step{{ID: "host", Type: "host_only", With: map[string]any{}}},
	})
	err := New(steps, WithExecutors(executors)).Validate(t.Context(), definition, Options{RunDir: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "not supported inside executor blocks") {
		t.Fatalf("error = %v", err)
	}
}

// An attempt is a control, not a step, so it is legal inside an executor block and its body steps
// are checked for executor support one by one. The old wait pseudo-step was rejected outright
// because its polling executor was engine-native, so polling inside an executor is new.
func TestExecutorScopeAllowsAttempt(t *testing.T) {
	definition := testDefinition(t, "attempt-in-executor", workflow.Step{
		Executor: &workflow.ExecutorScope{Type: "recording", With: map[string]any{}},
		Steps: []workflow.Step{{
			ID: "pause",
			Attempt: &workflow.AttemptControl{
				MaxAttempts:       workflow.LiteralCount(1),
				BackoffMultiplier: workflow.LiteralFactor(1),
				Steps:             []workflow.Step{{ID: "unsupported", Type: "git_clean", With: map[string]any{}}},
			},
		}},
	})
	if err := executorTestEngine(t, &recordingExecutor{}).Validate(t.Context(), definition, Options{RunDir: t.TempDir()}); err != nil {
		t.Fatalf("attempt was rejected inside an executor block: %v", err)
	}
}
