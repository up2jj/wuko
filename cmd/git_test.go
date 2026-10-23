package cmd

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/up2jj/wuko/diagnostic"
	"github.com/up2jj/wuko/engine"
	"github.com/up2jj/wuko/githook"
	"github.com/up2jj/wuko/internal/gittest"
	"github.com/up2jj/wuko/internal/testfs"
	"github.com/up2jj/wuko/step"
	filestep "github.com/up2jj/wuko/steps/file"
	gitstep "github.com/up2jj/wuko/steps/git"
	"github.com/up2jj/wuko/steps/shell"
)

func TestGitHookRunExposesStructuredContext(t *testing.T) {
	t.Parallel()
	root := gittest.Init(t)
	writeGitHookWorkflow(t, root, "inspect", `printf '%s|%s|%s' "$1" "$2" "$3"`, []string{
		"{{ .git.hook.name }}", "{{ index .git.hook.args 0 }}", "{{ (index .git.hook.payload.updates 0).local_ref }}",
	})
	writeGitHookManifest(t, root, "pre-push:\n    - workflow: inspect")
	var stdout, stderr bytes.Buffer
	command := newRootCmd(gitHookTestDependencies(t, root, &stdout, &stderr, func(string) string { return "" }))
	command.SetIn(strings.NewReader("refs/heads/main aaaa refs/heads/main bbbb\n"))
	command.SetArgs([]string{"git", "hook", "run", "pre-push", "--", "origin", "ssh://example/repo"})
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if stdout.String() != "pre-push|origin|refs/heads/main" {
		t.Fatalf("stdout = %q", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want a quiet successful hook run", stderr.String())
	}
}

func TestGitHookRunStopsAfterFirstWorkflowFailure(t *testing.T) {
	t.Parallel()
	root := gittest.Init(t)
	writeGitHookWorkflow(t, root, "fail", "exit 7", nil)
	marker := filepath.Join(root, "second-ran")
	writeGitHookWorkflow(t, root, "second", `touch "$1"`, []string{marker})
	writeGitHookManifest(t, root, "pre-commit:\n    - workflow: fail\n    - workflow: second")
	command := newRootCmd(gitHookTestDependencies(t, root, io.Discard, io.Discard, func(string) string { return "" }))
	command.SetArgs([]string{"git", "hook", "run", "pre-commit"})
	if err := command.ExecuteContext(t.Context()); err == nil {
		t.Fatal("hook unexpectedly succeeded")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("second workflow ran: %v", err)
	}
}

func TestGitHookRunUsesConciseFailureReporter(t *testing.T) {
	t.Parallel()
	root := gittest.Init(t)
	writeGitHookWorkflow(t, root, "check", `printf 'step detail\n' >&2; exit 7`, nil)
	writeGitHookManifest(t, root, "pre-commit:\n    - workflow: check")
	var stdout, stderr bytes.Buffer
	command := newRootCmd(gitHookTestDependencies(t, root, &stdout, &stderr, func(string) string { return "" }))
	command.SetArgs([]string{"git", "hook", "run", "pre-commit"})
	err := command.ExecuteContext(t.Context())
	if err == nil {
		t.Fatal("hook unexpectedly succeeded")
	}
	for _, want := range []string{`pre-commit: workflow "check" failed at step "run"`, `.wuko/workflows/check.yaml:4:5`, "status 7"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %q, want %q", err, want)
		}
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want no progress output", stdout.String())
	}
	if stderr.String() != "step detail\n" {
		t.Fatalf("stderr = %q, want only explicit step output", stderr.String())
	}
}

func TestGitHookReporterShowsOnlyFinalRetryFailure(t *testing.T) {
	t.Parallel()
	var stderr bytes.Buffer
	debug := false
	command := &cobra.Command{}
	command.SetErr(&stderr)
	reporter := newGitHookReporter(command, dependencies{debug: &debug}, "/repo", "pre-push", "verify")
	location := diagnostic.Location{Source: "/repo/.wuko/workflows/verify.yaml", Line: 8, Column: 5}
	reporter.Diagnostic(diagnostic.Event{
		Status: diagnostic.StatusFailed, WorkflowName: "verify", StepID: "temporary", StepRunID: "temporary-run", Location: location,
		Error: errors.New("temporary failure"),
	})
	reporter.Progress(engine.ProgressEvent{
		Kind: engine.WorkflowFinished, Status: engine.StatusFailed, WorkflowName: "verify",
		Stats: engine.RunStats{Steps: []engine.StepStats{{
			ID: "push", Status: engine.StatusFailed, Location: location, Error: errors.New("permanent failure"),
			Attempts: []engine.AttemptStats{{Number: 1}, {Number: 2}, {Number: 3}},
		}}},
	})
	err := reporter.failureError(errors.New("wrapped workflow failure"))
	if got := err.Error(); got != `pre-push: workflow "verify" failed at step "push" (.wuko/workflows/verify.yaml:8:5) after 3 attempts: permanent failure` {
		t.Fatalf("error = %q", got)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want no retry chatter", stderr.String())
	}
}

func TestGitHookRunSelectsConfiguredTarget(t *testing.T) {
	t.Parallel()
	root := gittest.Init(t)
	directory := filepath.Join(root, ".wuko", "workflows")
	testfs.MkdirAll(t, directory, 0o755)
	data := `version: 1
name: check
targets:
  fast:
    steps:
      - {id: run, type: shell, with: {script: "printf fast"}}
  thorough:
    steps:
      - {id: run, type: shell, with: {script: "printf thorough"}}
`
	testfs.WriteFile(t, filepath.Join(directory, "check.yaml"), []byte(data), 0o644)
	writeGitHookManifest(t, root, "pre-commit:\n    - workflow: check\n      target: thorough")
	var output bytes.Buffer
	command := newRootCmd(gitHookTestDependencies(t, root, &output, io.Discard, func(string) string { return "" }))
	command.SetArgs([]string{"git", "hook", "run", "pre-commit"})
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if output.String() != "thorough" {
		t.Fatalf("stdout = %q", output.String())
	}
}

func TestGitHookRunRejectsRecursion(t *testing.T) {
	t.Parallel()
	root := gittest.Init(t)
	writeGitHookManifest(t, root, "pre-commit:\n    - workflow: check")
	command := newRootCmd(gitHookTestDependencies(t, root, io.Discard, io.Discard, func(name string) string {
		if name == gitHookStackEnvironment {
			return "pre-push,pre-commit"
		}
		return ""
	}))
	command.SetArgs([]string{"git", "hook", "run", "pre-commit"})
	err := command.ExecuteContext(t.Context())
	if err == nil || !strings.Contains(err.Error(), "recursive Git hook") {
		t.Fatalf("error = %v", err)
	}
}

func TestGitHookInstallAndStatus(t *testing.T) {
	t.Parallel()
	root := gittest.Init(t)
	writeGitHookWorkflow(t, root, "check", "true", nil)
	writeGitHookManifest(t, root, "pre-commit:\n    - workflow: check")
	executable := filepath.Join(root, "wuko")
	testfs.WriteFile(t, executable, []byte("binary"), 0o755)
	var output bytes.Buffer
	deps := gitHookTestDependencies(t, root, &output, io.Discard, func(string) string { return "" })
	deps.executable = func() (string, error) { return executable, nil }
	command := newRootCmd(deps)
	command.SetArgs([]string{"git", "hook", "install"})
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	command = newRootCmd(deps)
	command.SetArgs([]string{"git", "hook", "status"})
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "pre-commit\tinstalled") {
		t.Fatalf("output = %q", output.String())
	}
}

func TestGitHookInitCreatesValidStarterConfiguration(t *testing.T) {
	t.Parallel()
	root := gittest.Init(t)
	var output bytes.Buffer
	deps := gitHookTestDependencies(t, root, &output, io.Discard, func(string) string { return "" })
	command := newRootCmd(deps)
	command.SetArgs([]string{"git", "hook", "init"})
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"created .wuko/git-hooks.yaml",
		"created .wuko/workflows/git-check.yaml",
		"created .wuko/workflows/git-commit-message.yaml",
		"wuko git hook install",
	} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("output = %q, want %q", output.String(), want)
		}
	}

	output.Reset()
	command = newRootCmd(deps)
	command.SetArgs([]string{"validate"})
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatalf("generated configuration is invalid: %v", err)
	}
	for _, want := range []string{"git-check (pushed): valid", "git-check (staged): valid", "git-commit-message: valid", ".wuko/git-hooks.yaml: valid"} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("validate output = %q, want %q", output.String(), want)
		}
	}
	badPath := filepath.Join(root, "bad.txt")
	testfs.WriteFile(t, badPath, []byte("trailing whitespace \n"), 0o600)
	gittest.Run(t, root, "add", "bad.txt")
	command = newRootCmd(deps)
	command.SetArgs([]string{"git", "hook", "run", "pre-commit"})
	if err := command.ExecuteContext(t.Context()); err == nil || !strings.Contains(err.Error(), "bad.txt:1") {
		t.Fatalf("generated pre-commit workflow error = %v", err)
	}
	testfs.WriteFile(t, badPath, []byte("clean\n"), 0o600)
	gittest.Run(t, root, "add", "bad.txt")
	command = newRootCmd(deps)
	command.SetArgs([]string{"git", "hook", "run", "pre-commit"})
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatalf("generated pre-commit workflow rejected clean changes: %v", err)
	}

	messagePath := filepath.Join(root, "COMMIT_EDITMSG")
	testfs.WriteFile(t, messagePath, []byte("feat(cli): initialize Git hooks\n"), 0o600)
	command = newRootCmd(deps)
	command.SetArgs([]string{"git", "hook", "run", "commit-msg", "--", messagePath})
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatalf("generated commit-msg workflow rejected a conventional commit: %v", err)
	}
	testfs.WriteFile(t, messagePath, []byte("not conventional\n"), 0o600)
	command = newRootCmd(deps)
	command.SetArgs([]string{"git", "hook", "run", "commit-msg", "--", messagePath})
	err := command.ExecuteContext(t.Context())
	if err == nil || !strings.Contains(err.Error(), "invalid conventional commit") {
		t.Fatalf("generated commit-msg workflow error = %v", err)
	}
}

func TestGitHookInitRefusesExistingFilesWithoutMutation(t *testing.T) {
	t.Parallel()
	root := gittest.Init(t)
	manifestPath := filepath.Join(root, ".wuko", "git-hooks.yaml")
	testfs.MkdirAll(t, filepath.Dir(manifestPath), 0o755)
	testfs.WriteFile(t, manifestPath, []byte("existing\n"), 0o600)
	command := newRootCmd(gitHookTestDependencies(t, root, io.Discard, io.Discard, func(string) string { return "" }))
	command.SetArgs([]string{"git", "hook", "init"})
	err := command.ExecuteContext(t.Context())
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("error = %v", err)
	}
	data, readErr := os.ReadFile(manifestPath)
	if readErr != nil || string(data) != "existing\n" {
		t.Fatalf("manifest = %q, %v", data, readErr)
	}
	if _, statErr := os.Stat(filepath.Join(root, ".wuko", "workflows")); !os.IsNotExist(statErr) {
		t.Fatalf("workflow directory was created: %v", statErr)
	}
}

// Git ignores a hook file without the execute bit, so chaining one must not turn it into a
// failure that blocks every push.
func TestGitHookRunSkipsNonExecutablePreservedHook(t *testing.T) {
	t.Parallel()
	root := gittest.Init(t)
	writeGitHookWorkflow(t, root, "check", "true", nil)
	writeGitHookManifest(t, root, "pre-push:\n    - workflow: check")
	repository, err := githook.Discover(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	hookPath, err := repository.HookPath(t.Context(), "pre-push")
	if err != nil {
		t.Fatal(err)
	}
	testfs.WriteFile(t, hookPath, []byte("#!/bin/sh\nexit 1\n"), 0o644)
	executable := filepath.Join(root, "wuko")
	testfs.WriteFile(t, executable, []byte("binary"), 0o755)
	manifest, err := githook.LoadManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := githook.Install(t.Context(), repository, executable, manifest, true); err != nil {
		t.Fatal(err)
	}
	command := newRootCmd(gitHookTestDependencies(t, root, io.Discard, io.Discard, func(string) string { return "" }))
	command.SetIn(strings.NewReader(""))
	command.SetArgs([]string{"git", "hook", "run", "pre-push", "--", "origin", "url"})
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatalf("hook run error = %v, want the ignored preserved hook skipped", err)
	}
}

func TestGitHookRunExecutesPreservedHookBeforeLoadingManifest(t *testing.T) {
	t.Parallel()
	root := gittest.Init(t)
	writeGitHookWorkflow(t, root, "check", "true", nil)
	writeGitHookManifest(t, root, "pre-push:\n    - workflow: check")
	repository, err := githook.Discover(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	hookPath, err := repository.HookPath(t.Context(), "pre-push")
	if err != nil {
		t.Fatal(err)
	}
	inputPath := filepath.Join(root, "preserved-input")
	testfs.WriteFile(t, hookPath, []byte("#!/bin/sh\ncat > "+strconvQuote(inputPath)+"\n"), 0o755)
	executable := filepath.Join(root, "wuko")
	testfs.WriteFile(t, executable, []byte("binary"), 0o755)
	manifest, err := githook.LoadManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := githook.Install(t.Context(), repository, executable, manifest, true); err != nil {
		t.Fatal(err)
	}
	testfs.WriteFile(t, filepath.Join(root, ".wuko", "git-hooks.yaml"), []byte("invalid: true\n"), 0o644)
	command := newRootCmd(gitHookTestDependencies(t, root, io.Discard, io.Discard, func(string) string { return "" }))
	input := "refs/heads/main aaaa refs/heads/main bbbb\n"
	command.SetIn(strings.NewReader(input))
	command.SetArgs([]string{"git", "hook", "run", "pre-push", "--", "origin", "url"})
	if err := command.ExecuteContext(t.Context()); err == nil {
		t.Fatal("hook unexpectedly accepted invalid manifest")
	}
	preservedInput, err := os.ReadFile(inputPath)
	if err != nil {
		t.Fatalf("preserved hook did not run first: %v", err)
	}
	if string(preservedInput) != input {
		t.Fatalf("preserved hook stdin = %q", preservedInput)
	}
}

func TestValidateRecognizesGitContextAndManifest(t *testing.T) {
	t.Parallel()
	root := gittest.Init(t)
	writeGitHookWorkflow(t, root, "check", `printf '%s' "$1"`, []string{"{{ .git.hook.name }}"})
	writeGitHookManifest(t, root, "pre-commit:\n    - workflow: check")
	var output bytes.Buffer
	command := newRootCmd(gitHookTestDependencies(t, root, &output, io.Discard, func(string) string { return "" }))
	command.SetArgs([]string{"validate"})
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), ".wuko/git-hooks.yaml: valid") {
		t.Fatalf("output = %q", output.String())
	}
}

func TestGitHookRunToleratesMissingManifest(t *testing.T) {
	t.Parallel()
	root := gittest.Init(t)
	command := newRootCmd(gitHookTestDependencies(t, root, io.Discard, io.Discard, func(string) string { return "" }))
	command.SetArgs([]string{"git", "hook", "run", "pre-commit"})
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatalf("hook run without a manifest failed: %v", err)
	}
}

func writeGitHookManifest(t *testing.T, root, hooks string) {
	t.Helper()
	testfs.MkdirAll(t, filepath.Join(root, ".wuko"), 0o755)
	data := "version: 1\nhooks:\n  " + hooks + "\n"
	testfs.WriteFile(t, filepath.Join(root, ".wuko", "git-hooks.yaml"), []byte(data), 0o644)
}

func writeGitHookWorkflow(t *testing.T, root, name, script string, args []string) {
	t.Helper()
	directory := filepath.Join(root, ".wuko", "workflows")
	testfs.MkdirAll(t, directory, 0o755)
	var arguments strings.Builder
	if len(args) > 0 {
		arguments.WriteString("\n      args:")
		for _, argument := range args {
			arguments.WriteString("\n        - " + strconvQuote(argument))
		}
	}
	data := "version: 1\nname: " + name + "\nsteps:\n  - id: run\n    type: shell\n    with:\n      script: " + strconvQuote(script) + arguments.String() + "\n"
	testfs.WriteFile(t, filepath.Join(directory, name+".yaml"), []byte(data), 0o644)
}

func strconvQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

func gitHookTestDependencies(t *testing.T, root string, stdout, stderr io.Writer, getenv func(string) string) dependencies {
	t.Helper()
	registry := step.NewRegistry()
	for _, register := range []func(*step.Registry) error{shell.Register, filestep.Register, gitstep.Register} {
		if err := register(registry); err != nil {
			t.Fatal(err)
		}
	}
	return dependencies{
		stdin: bytes.NewReader(nil), stdout: stdout, stderr: stderr, registry: registry, getenv: getenv,
		cwd: func() (string, error) { return root, nil }, homeDir: func() (string, error) { return filepath.Join(root, "home"), nil },
		configDir: func() (string, error) { return filepath.Join(root, "config"), nil },
	}
}
