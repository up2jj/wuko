package devenv_test

// This file drives the devenv executor end to end: a real workflow definition, the real
// executor and step registrations, the real process.LocalExecutor, and a fake devenv CLI
// on PATH. Nothing here stubs the commandRunner seam, so it covers the whole chain that
// the unit tests in package devenv cannot reach: argv construction -> a real child
// process -> stdout capture -> JSON decoding -> `value` and `value_decoded` in a later
// step's expressions.
//
// The fake CLI is this test binary re-entered under a different argv[0], so the encoded
// devenv command contract lives next to the code that builds those commands.

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"

	"github.com/up2jj/wuko/engine"
	"github.com/up2jj/wuko/executor"
	"github.com/up2jj/wuko/step"
	assertstep "github.com/up2jj/wuko/steps/assert"
	devenvstep "github.com/up2jj/wuko/steps/devenv"
	"github.com/up2jj/wuko/workflow"
)

const (
	fakeScriptFile = ".fake-devenv.json"
	fakeLogFile    = ".fake-devenv.jsonl"
	// unexpectedExit marks an argv the fake does not model. A fake that silently
	// returned a canned result would let a stray invocation pass unnoticed.
	unexpectedExit = 97
	fakeErrorExit  = 96
)

// fakeScript programs one scenario. It is read from the working directory because the
// executor always runs devenv in the resolved root, which is also the fixture directory.
type fakeScript struct {
	Version          string `json:"version"`
	TasksStdout      string `json:"tasks_stdout"`
	TasksStderr      string `json:"tasks_stderr"`
	TasksExit        int    `json:"tasks_exit"`
	ShellStdout      string `json:"shell_stdout"`
	SecretspecStdout string `json:"secretspec_stdout"`
	// ProcessStatus is what `processes status` reports; "running" when empty.
	ProcessStatus string `json:"process_status"`
}

// invocation is one recorded call to a fake CLI.
type invocation struct {
	Argv []string          `json:"argv"`
	Env  map[string]string `json:"env"`
}

// TestMain turns this binary into the fake CLI when it is invoked under the name devenv
// or secretspec. Dispatching on argv[0] rather than an environment marker is deliberate:
// process.LocalExecutor builds the child environment from options.Env alone, so an
// exported marker would survive only by accident of the engine's env plumbing.
func TestMain(main *testing.M) {
	switch filepath.Base(os.Args[0]) {
	case "devenv":
		os.Exit(fakeDevenv(os.Args[1:]))
	case "secretspec":
		os.Exit(fakeSecretspec(os.Args[1:]))
	}
	os.Exit(main.Run())
}

func fakeDevenv(args []string) int {
	script, err := loadFakeScript()
	if err != nil {
		fmt.Fprintf(os.Stderr, "fake devenv: %v\n", err)
		return fakeErrorExit
	}
	recordInvocation(args)
	profiles, rest := stripProfiles(args)
	if len(rest) == 0 {
		return unexpected("devenv", args)
	}
	switch rest[0] {
	case "--version":
		fmt.Fprintln(os.Stdout, script.Version)
		return 0
	case "info":
		fmt.Fprintf(os.Stdout, "DEVENV_PROFILE: %s\n", strings.Join(profiles, ","))
		return 0
	case "shell":
		if len(rest) < 3 || rest[1] != "--" {
			return unexpected("devenv", args)
		}
		// Wrapper noise lands on stdout ahead of the wrapped command's own output,
		// which is exactly how a chatty shell hook breaks JSON decoding.
		io.WriteString(os.Stdout, script.ShellStdout)
		return execWrapped(rest[2:])
	case "tasks":
		if len(rest) < 2 || rest[1] != "run" {
			return unexpected("devenv", args)
		}
		io.WriteString(os.Stdout, script.TasksStdout)
		io.WriteString(os.Stderr, script.TasksStderr)
		return script.TasksExit
	case "processes":
		if len(rest) < 2 {
			return unexpected("devenv", args)
		}
		switch rest[1] {
		case "status":
			if len(rest) < 3 {
				return unexpected("devenv", args)
			}
			status := script.ProcessStatus
			if status == "" {
				status = "running"
			}
			fmt.Fprintf(os.Stdout, "%s %s\n", rest[2], status)
			return 0
		case "start", "stop", "wait":
			return 0
		}
		return unexpected("devenv", args)
	}
	return unexpected("devenv", args)
}

func fakeSecretspec(args []string) int {
	script, err := loadFakeScript()
	if err != nil {
		fmt.Fprintf(os.Stderr, "fake secretspec: %v\n", err)
		return fakeErrorExit
	}
	recordInvocation(args)
	if len(args) < 3 || args[0] != "run" || args[1] != "--" {
		return unexpected("secretspec", args)
	}
	io.WriteString(os.Stdout, script.SecretspecStdout)
	return execWrapped(args[2:])
}

// execWrapped really replaces this process with the wrapped command, the way
// `devenv shell --` and `secretspec run --` do. Simulating the wrapper instead would not
// prove that the inner process's stdout survives two levels of nesting into the capture
// buffer, which is the whole question the secrets scenarios ask.
func execWrapped(argv []string) int {
	path, err := exec.LookPath(argv[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "fake wrapper: %v\n", err)
		return fakeErrorExit
	}
	if err := syscall.Exec(path, argv, os.Environ()); err != nil {
		fmt.Fprintf(os.Stderr, "fake wrapper: exec %s: %v\n", path, err)
		return fakeErrorExit
	}
	return 0
}

func unexpected(tool string, args []string) int {
	fmt.Fprintf(os.Stderr, "unexpected %s invocation: %q\n", tool, args)
	return unexpectedExit
}

// stripProfiles removes the leading --profile pairs the executor prepends, returning the
// profiles in order and the remaining argv.
func stripProfiles(args []string) ([]string, []string) {
	profiles := []string(nil)
	index := 0
	for index+1 < len(args) && args[index] == "--profile" {
		profiles = append(profiles, args[index+1])
		index += 2
	}
	return profiles, args[index:]
}

func loadFakeScript() (fakeScript, error) {
	script := fakeScript{Version: "devenv 2.2.0"}
	content, err := os.ReadFile(fakeScriptFile)
	if err != nil {
		return script, fmt.Errorf("reading %s: %w", fakeScriptFile, err)
	}
	if err := json.Unmarshal(content, &script); err != nil {
		return script, fmt.Errorf("decoding %s: %w", fakeScriptFile, err)
	}
	if script.Version == "" {
		script.Version = "devenv 2.2.0"
	}
	return script, nil
}

func recordInvocation(args []string) {
	record := invocation{Argv: args, Env: map[string]string{}}
	for _, key := range []string{"SECRETSPEC_PROFILE", "SECRETSPEC_PROVIDER"} {
		if value, ok := os.LookupEnv(key); ok {
			record.Env[key] = value
		}
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		return
	}
	file, err := os.OpenFile(fakeLogFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer file.Close()
	fmt.Fprintf(file, "%s\n", encoded)
}

// installFakeCLI puts the fake devenv and secretspec on PATH for one test.
func installFakeCLI(t *testing.T) {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	for _, name := range []string{"devenv", "secretspec"} {
		if err := os.Symlink(self, filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	// exec.Command resolves the binary against this process's PATH, not options.Env.
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	// Running the suite from inside a real devenv shell would otherwise take the
	// active-environment path and fail on the root comparison.
	for _, key := range []string{
		"DEVENV_ROOT", "DEVENV_PROFILE", "DEVENV_RUNTIME", "DEVENV_DOTFILE", "DEVENV_STATE",
		"SECRETSPEC_PROFILE", "SECRETSPEC_PROVIDER",
	} {
		t.Setenv(key, "")
	}
}

func writeFakeScript(t *testing.T, dir string, script fakeScript) {
	t.Helper()
	encoded, err := json.Marshal(script)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, fakeScriptFile), encoded, 0o644); err != nil {
		t.Fatal(err)
	}
}

// recordedArgv returns every fake invocation in order, as space-joined argv.
func recordedArgv(t *testing.T, dir string) []string {
	t.Helper()
	commands := []string(nil)
	for _, record := range recordedInvocations(t, dir) {
		commands = append(commands, strings.Join(record.Argv, " "))
	}
	return commands
}

func recordedInvocations(t *testing.T, dir string) []invocation {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(dir, fakeLogFile))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	records := []invocation(nil)
	for _, line := range strings.Split(strings.TrimSpace(string(content)), "\n") {
		if line == "" {
			continue
		}
		var record invocation
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("decoding recorded invocation %q: %v", line, err)
		}
		records = append(records, record)
	}
	return records
}

func devenvEngine(t *testing.T) *engine.Engine {
	t.Helper()
	steps := step.NewRegistry()
	for _, register := range []func(*step.Registry) error{devenvstep.RegisterTask, assertstep.Register} {
		if err := register(steps); err != nil {
			t.Fatal(err)
		}
	}
	executors := executor.NewRegistry()
	if err := devenvstep.RegisterExecutor(executors); err != nil {
		t.Fatal(err)
	}
	return engine.New(steps, engine.WithExecutors(executors))
}

// devenvDefinition puts the devenv_task step in an executor scope and the assertions
// after it. Executor blocks accept only process-backed steps, so `assert` has to sit
// outside the block and read the committed outputs from workflow state.
func devenvDefinition(executorWith map[string]any, task map[string]any, assertions ...workflow.Step) *workflow.Definition {
	steps := []workflow.Step{{
		Executor: &workflow.ExecutorScope{Type: "devenv", With: executorWith},
		Steps:    []workflow.Step{{ID: "task", Type: "devenv_task", With: task}},
	}}
	return &workflow.Definition{Version: 1, Name: "devenv-e2e", Steps: append(steps, assertions...)}
}

func assertStep(id, expr string) workflow.Step {
	return workflow.Step{ID: id, Type: "assert", With: map[string]any{
		"expr": expr, "message": "expression failed: " + expr,
	}}
}

// runDevenvWorkflow installs the fake CLI, writes the script and any fixture files into
// the run directory, and runs the definition.
func runDevenvWorkflow(t *testing.T, script fakeScript, definition *workflow.Definition, fixtures ...func(*testing.T, string)) (string, *engine.State, error) {
	t.Helper()
	installFakeCLI(t)
	dir := t.TempDir()
	writeFakeScript(t, dir, script)
	for _, fixture := range fixtures {
		fixture(t, dir)
	}
	// BaseEnv stays nil so the shim PATH reaches the child; setting it would strip PATH
	// and the fake could not resolve the command it wraps.
	state, err := devenvEngine(t).Run(t.Context(), definition, engine.Options{
		RunDir: dir, Stdout: io.Discard, Stderr: io.Discard,
	})
	return dir, state, err
}

func taskOutputs(t *testing.T, state *engine.State) map[string]any {
	t.Helper()
	outputs, ok := state.Steps["task"].(map[string]any)
	if !ok {
		t.Fatalf("task outputs = %#v", state.Steps["task"])
	}
	return outputs
}

func TestDevenvTaskDecodesTypedValueEndToEnd(t *testing.T) {
	script := fakeScript{TasksStdout: `{"app:build":{"artifact":"dist/app"}}` + "\n"}
	definition := devenvDefinition(
		map[string]any{"directory": "."},
		map[string]any{"name": "app:build", "mode": "single", "inputs": map[string]any{"target": "prod"}},
		assertStep("verify", `steps.task.value_decoded == true`),
		assertStep("verify_value", `steps.task.value["app:build"]["artifact"] == "dist/app"`),
	)
	dir, state, err := runDevenvWorkflow(t, script, definition)
	if err != nil {
		t.Fatal(err)
	}
	// Outside an active devenv the invocation is always wrapped in `devenv shell --`,
	// so the inner devenv is a second process whose stdout has to survive the nesting.
	want := []string{
		"--version",
		`shell -- devenv tasks run app:build --mode single --input-json {"target":"prod"}`,
		`tasks run app:build --mode single --input-json {"target":"prod"}`,
	}
	if got := recordedArgv(t, dir); !slices.Equal(got, want) {
		t.Fatalf("recorded argv = %#v, want %#v", got, want)
	}
	if outputs := taskOutputs(t, state); outputs["value_decoded"] != true || outputs["exit_code"] != 0 {
		t.Fatalf("outputs = %#v", outputs)
	}
}

func TestDevenvTaskTreatsEmptyStdoutAsNoTaskOutputs(t *testing.T) {
	definition := devenvDefinition(
		map[string]any{"directory": "."},
		map[string]any{"name": "app:noop"},
		assertStep("verify", `steps.task.value_decoded == true`),
		assertStep("verify_empty", `len(steps.task.value) == 0`),
	)
	if _, _, err := runDevenvWorkflow(t, fakeScript{}, definition); err != nil {
		t.Fatal(err)
	}
}

// A chatty wrapper is the failure mode the runtime SecretSpec mode risks: the invocation
// succeeds, so the step must succeed too, reporting the undecodable output rather than
// failing a task graph whose side effects already committed.
func TestDevenvTaskSurvivesWrapperNoiseOnStdout(t *testing.T) {
	script := fakeScript{
		TasksStdout: `{"app:build":{"artifact":"dist/app"}}` + "\n",
		ShellStdout: "direnv: loading devenv\n",
	}
	definition := devenvDefinition(
		map[string]any{"directory": "."},
		map[string]any{"name": "app:build"},
		assertStep("verify", `steps.task.value_decoded == false`),
		assertStep("verify_exit", `steps.task.exit_code == 0`),
	)
	dir, state, err := runDevenvWorkflow(t, script, definition, secretspecFixture)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"--version",
		"shell -- secretspec run -- devenv tasks run app:build --mode before",
		"run -- devenv tasks run app:build --mode before",
		"tasks run app:build --mode before",
	}
	if got := recordedArgv(t, dir); !slices.Equal(got, want) {
		t.Fatalf("recorded argv = %#v, want %#v", got, want)
	}
	if _, ok := taskOutputs(t, state)["value"]; ok {
		t.Fatalf("undecodable stdout unexpectedly published value: %#v", state.Steps["task"])
	}
}

// The positive control for the wrapper chain: two levels of nesting do not by themselves
// break decoding, so a failure of the real nightly smoke test points at devenv's output
// rather than at Wuko's plumbing.
func TestDevenvTaskDecodesTypedValueThroughSecretspecWrapper(t *testing.T) {
	script := fakeScript{TasksStdout: `{"app:build":{"artifact":"dist/app"}}` + "\n"}
	definition := devenvDefinition(
		map[string]any{
			"directory": ".",
			"secrets":   map[string]any{"mode": "runtime", "profile": "dev", "provider": "env"},
		},
		map[string]any{"name": "app:build"},
		assertStep("verify", `steps.task.value_decoded == true`),
		assertStep("verify_value", `steps.task.value["app:build"]["artifact"] == "dist/app"`),
	)
	dir, _, err := runDevenvWorkflow(t, script, definition, secretspecFixture)
	if err != nil {
		t.Fatal(err)
	}
	secretspec := invocationsOf(recordedInvocations(t, dir), "run")
	if len(secretspec) != 1 {
		t.Fatalf("expected exactly one secretspec invocation, got %#v", recordedArgv(t, dir))
	}
	if secretspec[0].Env["SECRETSPEC_PROFILE"] != "dev" || secretspec[0].Env["SECRETSPEC_PROVIDER"] != "env" {
		t.Fatalf("secretspec environment = %#v", secretspec[0].Env)
	}
}

func TestDevenvTaskReportsUndecodableOutputWithShowOutput(t *testing.T) {
	script := fakeScript{TasksStdout: "running app:build\n" + `{"app:build":{}}` + "\n"}
	definition := devenvDefinition(
		map[string]any{"directory": "."},
		map[string]any{"name": "app:build", "show_output": true},
		assertStep("verify", `steps.task.value_decoded == false`),
		assertStep("verify_exit", `steps.task.exit_code == 0`),
	)
	dir, _, err := runDevenvWorkflow(t, script, definition)
	if err != nil {
		t.Fatal(err)
	}
	if got := recordedArgv(t, dir); !slices.Contains(got, "tasks run app:build --mode before --show-output") {
		t.Fatalf("recorded argv = %#v", got)
	}
}

func TestDevenvTaskRunsMultipleRootsUnderProfiles(t *testing.T) {
	script := fakeScript{TasksStdout: `{"app:prepare":{"prepared":true},"app:build":{"artifact":"dist/app"}}` + "\n"}
	definition := devenvDefinition(
		map[string]any{"directory": ".", "profiles": []any{"alpha", "beta"}},
		map[string]any{"names": []any{"app:prepare", "app:build"}, "mode": "single"},
		assertStep("verify_prepare", `steps.task.value["app:prepare"]["prepared"] == true`),
		assertStep("verify_build", `steps.task.value["app:build"]["artifact"] == "dist/app"`),
	)
	dir, _, err := runDevenvWorkflow(t, script, definition)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"--profile alpha --profile beta --version",
		"--profile alpha --profile beta shell -- devenv tasks run app:prepare app:build --mode single",
		"tasks run app:prepare app:build --mode single",
	}
	if got := recordedArgv(t, dir); !slices.Equal(got, want) {
		t.Fatalf("recorded argv = %#v, want %#v", got, want)
	}
}

func TestDevenvTaskReportsTruncatedStdout(t *testing.T) {
	script := fakeScript{TasksStdout: `{"app:build":{"artifact":"dist/app","note":"padding to exceed the limit"}}`}
	definition := devenvDefinition(
		map[string]any{"directory": "."},
		map[string]any{"name": "app:build", "capture_limit": "16B"},
		assertStep("verify_truncated", `steps.task.stdout_truncated == true`),
		assertStep("verify", `steps.task.value_decoded == false`),
	)
	if _, _, err := runDevenvWorkflow(t, script, definition); err != nil {
		t.Fatal(err)
	}
}

func TestDevenvTaskReportsNonObjectStdout(t *testing.T) {
	definition := devenvDefinition(
		map[string]any{"directory": "."},
		map[string]any{"name": "app:build"},
		assertStep("verify", `steps.task.value_decoded == false`),
	)
	if _, _, err := runDevenvWorkflow(t, fakeScript{TasksStdout: "[1,2]\n"}, definition); err != nil {
		t.Fatal(err)
	}
}

func TestDevenvTaskPreservesOutputsWhenTheGraphFails(t *testing.T) {
	script := fakeScript{TasksStdout: "partial", TasksStderr: "task failed\n", TasksExit: 3}
	definition := devenvDefinition(
		map[string]any{"directory": "."},
		map[string]any{"name": "app:build"},
	)
	dir, _, err := runDevenvWorkflow(t, script, definition)
	if err == nil {
		t.Fatal("expected the failed task graph to fail the workflow")
	}
	if !strings.Contains(err.Error(), "3") {
		t.Fatalf("Run() error = %v, want the exit status surfaced", err)
	}
	if got := recordedArgv(t, dir); !slices.Contains(got, "tasks run app:build --mode before") {
		t.Fatalf("recorded argv = %#v", got)
	}
}

// The process lifecycle is the one part of the executor that mutates state outside the
// step, and the only devenv subcommands whose argv nothing else asserts end to end.
func TestDevenvExecutorStartsAndStopsOnlyTheProcessesItOwns(t *testing.T) {
	script := fakeScript{
		TasksStdout:   `{"app:build":{}}` + "\n",
		ProcessStatus: "stopped",
	}
	definition := devenvDefinition(
		map[string]any{"directory": ".", "processes": []any{"db"}},
		map[string]any{"name": "app:build"},
	)
	dir, _, err := runDevenvWorkflow(t, script, definition)
	if err != nil {
		t.Fatal(err)
	}
	// A process Wuko found stopped is one it owns, so it must start it, wait for
	// readiness, and stop it again when the session closes.
	want := []string{
		"--version",
		"processes status db",
		"processes start db",
		"processes wait",
		"shell -- devenv tasks run app:build --mode before",
		"tasks run app:build --mode before",
		"processes stop db",
	}
	if got := recordedArgv(t, dir); !slices.Equal(got, want) {
		t.Fatalf("recorded argv = %#v, want %#v", got, want)
	}
}

func TestDevenvExecutorLeavesProcessesItDoesNotOwnRunning(t *testing.T) {
	script := fakeScript{TasksStdout: `{"app:build":{}}` + "\n", ProcessStatus: "running"}
	definition := devenvDefinition(
		map[string]any{"directory": ".", "processes": []any{"db"}},
		map[string]any{"name": "app:build"},
	)
	dir, _, err := runDevenvWorkflow(t, script, definition)
	if err != nil {
		t.Fatal(err)
	}
	for _, command := range recordedArgv(t, dir) {
		if strings.HasPrefix(command, "processes start") || strings.HasPrefix(command, "processes stop") {
			t.Fatalf("an already-running process was taken over: %#v", recordedArgv(t, dir))
		}
	}
}

// secretspecFixture makes resolveSecretMode pick runtime mode from the default auto, the
// way any repository carrying a secretspec.toml does.
func secretspecFixture(t *testing.T, dir string) {
	t.Helper()
	content := "[project]\nname = \"wuko-devenv-e2e\"\nrevision = \"1.0\"\n"
	if err := os.WriteFile(filepath.Join(dir, "secretspec.toml"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func invocationsOf(records []invocation, first string) []invocation {
	matched := []invocation(nil)
	for _, record := range records {
		if len(record.Argv) > 0 && record.Argv[0] == first {
			matched = append(matched, record)
		}
	}
	return matched
}
