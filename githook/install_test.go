package githook

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallRefusesConflictAndChainRestoresIt(t *testing.T) {
	repository := testRepository(t)
	hookPath, err := repository.HookPath(context.Background(), "pre-commit")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(hookPath), 0o755); err != nil {
		t.Fatal(err)
	}
	original := []byte("#!/bin/sh\necho original\n")
	if err := os.WriteFile(hookPath, original, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := Manifest{Version: 1, Hooks: map[string][]Binding{"pre-commit": {{Workflow: "check"}}}}
	executable := filepath.Join(repository.Root, "wuko")
	if err := os.WriteFile(executable, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Install(context.Background(), repository, executable, manifest, false); err == nil {
		t.Fatal("Install() unexpectedly replaced existing hook")
	}
	if _, err := Install(context.Background(), repository, executable, manifest, true); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(hookPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), dispatcherMarker) {
		t.Fatalf("dispatcher = %q", data)
	}
	statuses, err := Inspect(context.Background(), repository, manifest)
	if err != nil {
		t.Fatal(err)
	}
	if len(statuses) != 1 || statuses[0].State != "installed" || !statuses[0].Chained {
		t.Fatalf("statuses = %#v", statuses)
	}
	if _, err := Uninstall(context.Background(), repository, nil); err != nil {
		t.Fatal(err)
	}
	restored, err := os.ReadFile(hookPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(restored) != string(original) {
		t.Fatalf("restored = %q", restored)
	}
}

func TestUninstallRefusesModifiedDispatcher(t *testing.T) {
	repository := testRepository(t)
	manifest := Manifest{Version: 1, Hooks: map[string][]Binding{"pre-commit": {{Workflow: "check"}}}}
	executable := filepath.Join(repository.Root, "wuko")
	if err := os.WriteFile(executable, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Install(context.Background(), repository, executable, manifest, false); err != nil {
		t.Fatal(err)
	}
	hookPath, err := repository.HookPath(context.Background(), "pre-commit")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hookPath, []byte("changed"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Uninstall(context.Background(), repository, nil); err == nil {
		t.Fatal("Uninstall() unexpectedly removed modified hook")
	}
}

func TestHookPathRejectsSharedCoreHooksPath(t *testing.T) {
	repository := testRepository(t)
	shared := t.TempDir()
	command := exec.Command("git", "-C", repository.Root, "config", "core.hooksPath", shared)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git config: %v: %s", err, output)
	}
	if _, err := repository.HookPath(context.Background(), "pre-commit"); err == nil || !strings.Contains(err.Error(), "shared outside") {
		t.Fatalf("error = %v", err)
	}
}

func TestHookPathAllowsRepositoryLocalCoreHooksPath(t *testing.T) {
	repository := testRepository(t)
	command := exec.Command("git", "-C", repository.Root, "config", "core.hooksPath", ".githooks")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git config: %v: %s", err, output)
	}
	path, err := repository.HookPath(context.Background(), "pre-commit")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(repository.Root, ".githooks", "pre-commit")
	if path != want {
		t.Fatalf("path = %q, want %q", path, want)
	}
}

func TestLinkedWorktreesShareCommonHookPath(t *testing.T) {
	primary := testRepository(t)
	for _, arguments := range [][]string{
		{"config", "user.name", "Wuko Test"},
		{"config", "user.email", "wuko@example.test"},
		{"commit", "--allow-empty", "--no-verify", "-m", "initial"},
	} {
		command := exec.Command("git", append([]string{"-C", primary.Root}, arguments...)...)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", arguments, err, output)
		}
	}
	linkedRoot := filepath.Join(t.TempDir(), "linked")
	command := exec.Command("git", "-C", primary.Root, "worktree", "add", "-q", "-b", "linked-test", linkedRoot)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git worktree add: %v: %s", err, output)
	}
	linked, err := Discover(context.Background(), linkedRoot)
	if err != nil {
		t.Fatal(err)
	}
	primaryHook, err := primary.HookPath(context.Background(), "pre-commit")
	if err != nil {
		t.Fatal(err)
	}
	linkedHook, err := linked.HookPath(context.Background(), "pre-commit")
	if err != nil {
		t.Fatal(err)
	}
	if linked.CommonDir != primary.CommonDir || linkedHook != primaryHook {
		t.Fatalf("primary = %#v (%s), linked = %#v (%s)", primary, primaryHook, linked, linkedHook)
	}
}

func TestDiscoverRejectsBareRepository(t *testing.T) {
	root := t.TempDir()
	command := exec.Command("git", "init", "--bare", "-q", root)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git init --bare: %v: %s", err, output)
	}
	if _, err := Discover(context.Background(), root); err == nil || !strings.Contains(err.Error(), "bare Git repositories") {
		t.Fatalf("error = %v", err)
	}
}

func TestInstalledDispatcherForwardsArgumentsAndStdin(t *testing.T) {
	repository := testRepository(t)
	argumentsPath := filepath.Join(repository.Root, "arguments")
	stdinPath := filepath.Join(repository.Root, "stdin")
	executable := filepath.Join(repository.Root, "fake-wuko")
	script := "#!/bin/sh\nprintf '%s' \"$*\" > " + shellQuote(argumentsPath) + "\ncat > " + shellQuote(stdinPath) + "\n"
	if err := os.WriteFile(executable, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := Manifest{Version: 1, Hooks: map[string][]Binding{"pre-push": {{Workflow: "check"}}}}
	if _, err := Install(context.Background(), repository, executable, manifest, false); err != nil {
		t.Fatal(err)
	}
	hookPath, err := repository.HookPath(context.Background(), "pre-push")
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(hookPath, "origin", "ssh://example/repo")
	command.Stdin = bytes.NewBufferString("refs/heads/main aaaa refs/heads/main bbbb\n")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("dispatcher: %v: %s", err, output)
	}
	arguments, err := os.ReadFile(argumentsPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(arguments) != "git hook run pre-push -- origin ssh://example/repo" {
		t.Fatalf("arguments = %q", arguments)
	}
	input, err := os.ReadFile(stdinPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(input) != "refs/heads/main aaaa refs/heads/main bbbb\n" {
		t.Fatalf("stdin = %q", input)
	}
}

func TestUninstallRepeatedNameKeepsPreservedHook(t *testing.T) {
	repository := testRepository(t)
	hookPath, err := repository.HookPath(context.Background(), "pre-commit")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(hookPath), 0o755); err != nil {
		t.Fatal(err)
	}
	original := []byte("#!/bin/sh\necho original\n")
	if err := os.WriteFile(hookPath, original, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := Manifest{Version: 1, Hooks: map[string][]Binding{"pre-commit": {{Workflow: "check"}}}}
	executable := filepath.Join(repository.Root, "wuko")
	if err := os.WriteFile(executable, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Install(context.Background(), repository, executable, manifest, true); err != nil {
		t.Fatal(err)
	}
	statuses, err := Uninstall(context.Background(), repository, []string{"pre-commit", "pre-commit"})
	if err != nil {
		t.Fatal(err)
	}
	if len(statuses) != 1 {
		t.Fatalf("statuses = %#v", statuses)
	}
	restored, err := os.ReadFile(hookPath)
	if err != nil {
		t.Fatalf("preserved hook was removed: %v", err)
	}
	if string(restored) != string(original) {
		t.Fatalf("restored = %q", restored)
	}
}

func TestUninstallRemovesStateForMissingDispatcher(t *testing.T) {
	repository := testRepository(t)
	manifest := Manifest{Version: 1, Hooks: map[string][]Binding{"pre-commit": {{Workflow: "check"}}, "pre-push": {{Workflow: "check"}}}}
	executable := filepath.Join(repository.Root, "wuko")
	if err := os.WriteFile(executable, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Install(context.Background(), repository, executable, manifest, false); err != nil {
		t.Fatal(err)
	}
	hookPath, err := repository.HookPath(context.Background(), "pre-commit")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(hookPath); err != nil {
		t.Fatal(err)
	}
	if _, err := Uninstall(context.Background(), repository, nil); err != nil {
		t.Fatalf("Uninstall() refused to clean up a missing dispatcher: %v", err)
	}
	state, err := loadState(repository)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Hooks) != 0 {
		t.Fatalf("state = %#v", state)
	}
}

func testRepository(t *testing.T) Repository {
	t.Helper()
	root := t.TempDir()
	command := exec.Command("git", "init", "-q", root)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	repository, err := Discover(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	return repository
}
