package devenv

import (
	"context"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/up2jj/wuko/executor"
	"github.com/up2jj/wuko/process"
)

type recordedCommand struct {
	options process.Options
	result  process.Result
	err     error
}

func (r *recordedCommand) run(_ context.Context, options process.Options) (process.Result, error) {
	r.options = options
	if slices.Contains(options.Args, "--version") {
		return process.Result{Stdout: "devenv 2.2.0"}, nil
	}
	if slices.Contains(options.Args, "info") {
		return process.Result{Stdout: "DEVENV_PROFILE: " + options.Env["DEVENV_PROFILE"]}, nil
	}
	return r.result, r.err
}

func TestOpenUsesActiveEnvironmentAndRuntimeSecretSpec(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "secretspec.toml"), []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	command := &recordedCommand{}
	provider := &ExecutorProvider{
		config:  ExecutorConfig{Directory: root, Secrets: &SecretsConfig{Mode: "runtime"}},
		command: command.run,
	}
	session, err := provider.Open(t.Context(), executor.Request{RunDir: root, Env: map[string]string{"DEVENV_ROOT": root}})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close(t.Context())

	if _, err := session.Run(t.Context(), process.Options{Command: "go", Args: []string{"test", "./..."}, Env: map[string]string{"MODE": "test"}}); err != nil {
		t.Fatal(err)
	}
	if command.options.Command != "secretspec" || !slices.Equal(command.options.Args, []string{"run", "--", "go", "test", "./..."}) {
		t.Fatalf("wrapped command = %s %v", command.options.Command, command.options.Args)
	}
	if command.options.Env["MODE"] != "test" {
		t.Fatalf("environment = %#v", command.options.Env)
	}
}

func TestOpenActivatesConfiguredProfilesOutsideDevenv(t *testing.T) {
	root := t.TempDir()
	command := &recordedCommand{}
	provider := &ExecutorProvider{
		config:  ExecutorConfig{Directory: root, Profiles: []string{"backend", "testing"}},
		command: command.run,
	}
	session, err := provider.Open(t.Context(), executor.Request{RunDir: root, Env: map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close(t.Context())
	commandArgs := []string{"test", "with spaces", `a"quote`, "*.go", "$HOME", "", "x; echo ignored"}
	if _, err := session.Run(t.Context(), process.Options{Command: "go", Args: commandArgs}); err != nil {
		t.Fatal(err)
	}
	want := append([]string{"--profile", "backend", "--profile", "testing", "shell", "--", "go"}, commandArgs...)
	if command.options.Command != "devenv" || !slices.Equal(command.options.Args, want) {
		t.Fatalf("devenv command = %s %v", command.options.Command, command.options.Args)
	}
}

func TestOpenRejectsDifferentActiveRoot(t *testing.T) {
	provider := &ExecutorProvider{config: ExecutorConfig{Directory: t.TempDir()}, command: (&recordedCommand{}).run}
	_, err := provider.Open(t.Context(), executor.Request{RunDir: t.TempDir(), Env: map[string]string{"DEVENV_ROOT": t.TempDir()}})
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("error = %v", err)
	}
}

func TestOpenVerifiesExplicitActiveProfiles(t *testing.T) {
	root := t.TempDir()
	command := &recordedCommand{}
	provider := &ExecutorProvider{config: ExecutorConfig{Directory: root, Profiles: []string{"backend", "testing"}}, command: command.run}
	environment := map[string]string{"DEVENV_ROOT": root, "DEVENV_PROFILE": "/nix/store/profile"}
	command.result = process.Result{Stdout: environment["DEVENV_PROFILE"]}
	session, err := provider.Open(t.Context(), executor.Request{RunDir: root, Env: environment})
	if err != nil {
		t.Fatal(err)
	}
	if err := session.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(command.options.Args, " "), "testing") {
		t.Fatalf("profile probe args = %v", command.options.Args)
	}
}

func TestCloseStopsOwnedProcessesOnly(t *testing.T) {
	root := t.TempDir()
	command := &recordedCommand{}
	provider := &ExecutorProvider{
		config:  ExecutorConfig{Directory: root, Processes: []string{"api", "db"}},
		command: command.run,
		processState: processStateReaderFunc(func(_ context.Context, _ string, names []string, _ map[string]string) (map[string]processState, error) {
			return map[string]processState{names[0]: processRunning, names[1]: processStopped}, nil
		}),
	}
	session, err := provider.Open(t.Context(), executor.Request{RunDir: root, Env: map[string]string{"DEVENV_ROOT": root}})
	if err != nil {
		t.Fatal(err)
	}
	if err := session.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(command.options.Args, []string{"processes", "stop", "db"}) {
		t.Fatalf("cleanup command = %v", command.options.Args)
	}
}

func TestOpenWaitsForConfiguredProcesses(t *testing.T) {
	root := t.TempDir()
	var commands []string
	provider := &ExecutorProvider{
		config: ExecutorConfig{Directory: root, Processes: []string{"api"}},
		command: func(_ context.Context, options process.Options) (process.Result, error) {
			commands = append(commands, options.Command+" "+strings.Join(options.Args, " "))
			if slices.Contains(options.Args, "--version") {
				return process.Result{Stdout: "devenv 2.2.0"}, nil
			}
			return process.Result{}, nil
		},
		processState: processStateReaderFunc(func(context.Context, string, []string, map[string]string) (map[string]processState, error) {
			return map[string]processState{"api": processRunning}, nil
		}),
	}
	session, err := provider.Open(t.Context(), executor.Request{RunDir: root, Env: map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close(t.Context())

	want := []string{"devenv --version", "devenv processes wait"}
	if !slices.Equal(commands, want) {
		t.Fatalf("commands = %v, want %v", commands, want)
	}
}

func TestOpenCleansUpProcessesStartedBeforeFailure(t *testing.T) {
	root := t.TempDir()
	var commands []string
	provider := &ExecutorProvider{
		config: ExecutorConfig{Directory: root, Processes: []string{"api", "db"}},
		command: func(_ context.Context, options process.Options) (process.Result, error) {
			commands = append(commands, options.Command+" "+strings.Join(options.Args, " "))
			if slices.Contains(options.Args, "--version") {
				return process.Result{Stdout: "devenv 2.2.0"}, nil
			}
			if slices.Equal(options.Args, []string{"processes", "start", "db"}) {
				return process.Result{}, errors.New("db failed to start")
			}
			return process.Result{}, nil
		},
		processState: processStateReaderFunc(func(context.Context, string, []string, map[string]string) (map[string]processState, error) {
			return map[string]processState{"api": processStopped, "db": processStopped}, nil
		}),
	}
	_, err := provider.Open(t.Context(), executor.Request{RunDir: root, Env: map[string]string{}})
	if err == nil || !strings.Contains(err.Error(), "db failed to start") {
		t.Fatalf("Open() error = %v", err)
	}

	want := []string{
		"devenv --version",
		"devenv processes start api",
		"devenv processes start db",
		"devenv processes stop api",
	}
	if !slices.Equal(commands, want) {
		t.Fatalf("commands = %v, want %v", commands, want)
	}
}

func TestCloseAcceptsProcessAlreadyStopped(t *testing.T) {
	root := t.TempDir()
	statusCalls := 0
	provider := &ExecutorProvider{
		config: ExecutorConfig{Directory: root, Processes: []string{"db"}},
		command: func(_ context.Context, options process.Options) (process.Result, error) {
			if slices.Contains(options.Args, "--version") {
				return process.Result{Stdout: "devenv 2.2.0"}, nil
			}
			if slices.Contains(options.Args, "stop") {
				return process.Result{}, errors.New("process already stopped")
			}
			return process.Result{}, nil
		},
		processState: processStateReaderFunc(func(context.Context, string, []string, map[string]string) (map[string]processState, error) {
			statusCalls++
			return map[string]processState{"db": processStopped}, nil
		}),
	}
	session, err := provider.Open(t.Context(), executor.Request{RunDir: root, Env: map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
	if err := session.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if statusCalls != 2 {
		t.Fatalf("status calls = %d, want initial ownership and cleanup verification", statusCalls)
	}
}

func TestApplySecretOverrides(t *testing.T) {
	environment := map[string]string{"BASE": "yes"}
	applySecretOverrides(environment, &SecretsConfig{Profile: "development", Provider: "env"})
	want := map[string]string{"BASE": "yes", "SECRETSPEC_PROFILE": "development", "SECRETSPEC_PROVIDER": "env"}
	if !maps.Equal(environment, want) {
		t.Fatalf("environment = %#v", environment)
	}
}

func TestSecretSpecDisabledDoesNotWrapOrOverride(t *testing.T) {
	root := t.TempDir()
	command := &recordedCommand{}
	provider := &ExecutorProvider{
		config:  ExecutorConfig{Directory: root, Secrets: &SecretsConfig{Mode: "disabled", Profile: "prod", Provider: "keyring"}},
		command: command.run,
	}
	session, err := provider.Open(t.Context(), executor.Request{RunDir: root, Env: map[string]string{"DEVENV_ROOT": root}})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close(t.Context())
	if _, err := session.Run(t.Context(), process.Options{Command: "echo"}); err != nil {
		t.Fatal(err)
	}
	if command.options.Command != "echo" || command.options.Env["SECRETSPEC_PROFILE"] != "" {
		t.Fatalf("command=%q env=%#v", command.options.Command, command.options.Env)
	}
}

func TestEnvironmentValueParsesDevenvInfo(t *testing.T) {
	output := "# env\n- DEVENV_ROOT: /tmp/project\n- DEVENV_PROFILE: /nix/store/profile\n"
	if got := environmentValue(output, "DEVENV_PROFILE"); got != "/nix/store/profile" {
		t.Fatalf("value = %q", got)
	}
}

func TestAmbiguousProcessStateFailsSafely(t *testing.T) {
	root := t.TempDir()
	provider := &ExecutorProvider{
		config:  ExecutorConfig{Directory: root, Processes: []string{"api"}},
		command: (&recordedCommand{}).run,
		processState: processStateReaderFunc(func(context.Context, string, []string, map[string]string) (map[string]processState, error) {
			return map[string]processState{}, nil
		}),
	}
	_, err := provider.Open(t.Context(), executor.Request{RunDir: root, Env: map[string]string{}})
	if err == nil || !strings.Contains(err.Error(), "cannot establish ownership") {
		t.Fatalf("error = %v", err)
	}
}

func TestDetectActiveEnvironmentRequiresRoot(t *testing.T) {
	_, _, err := detectActiveEnvironment(map[string]string{"DEVENV_PROFILE": "/nix/store/profile"})
	if err == nil || !strings.Contains(err.Error(), "DEVENV_ROOT") {
		t.Fatalf("error = %v", err)
	}
}

func TestDetectActiveEnvironmentValidatesMarkers(t *testing.T) {
	root := t.TempDir()
	active, ok, err := detectActiveEnvironment(map[string]string{
		"DEVENV_ROOT":    root,
		"DEVENV_DOTFILE": filepath.Join(root, ".devenv"),
		"DEVENV_STATE":   filepath.Join(root, ".devenv", "state"),
		"DEVENV_RUNTIME": filepath.Join(root, "runtime"),
		"DEVENV_PROFILE": "/nix/store/profile",
	})
	canonicalRoot, canonicalErr := canonicalPath(root)
	if err != nil || canonicalErr != nil || !ok || active.root != canonicalRoot {
		t.Fatalf("active=%#v ok=%v err=%v", active, ok, err)
	}
}
