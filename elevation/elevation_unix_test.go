//go:build darwin || linux

package elevation

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/up2jj/wuko/process"
	"github.com/up2jj/wuko/ptyinteract"
)

type recordingExecutor struct {
	options process.Options
	result  process.Result
	err     error
}

func (executor *recordingExecutor) Run(_ context.Context, options process.Options) (process.Result, error) {
	executor.options = options
	return executor.result, executor.err
}

func TestTargetEnvironmentUsesCleanRootBaseline(t *testing.T) {
	host := map[string]string{"TERM": "xterm-256color", "LANG": "pl_PL.UTF-8", "SECRET": "host-secret"}
	environment := targetEnvironment(map[string]string{"TOKEN": "explicit", "PATH": "/custom/bin"}, func(key string) string { return host[key] })
	if environment["USER"] != "root" || environment["LOGNAME"] != "root" || environment["TOKEN"] != "explicit" || environment["PATH"] != "/custom/bin" {
		t.Fatalf("environment = %#v", environment)
	}
	if environment["TERM"] != "xterm-256color" || environment["LANG"] != "pl_PL.UTF-8" {
		t.Fatalf("safe host metadata missing from %#v", environment)
	}
	if _, exists := environment["SECRET"]; exists {
		t.Fatalf("host secret leaked into %#v", environment)
	}
}

func TestRootExecutionResolvesCommandAndUsesSanitizedEnvironment(t *testing.T) {
	dir := t.TempDir()
	command := filepath.Join(dir, "tool")
	if err := os.WriteFile(command, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	recorder := &recordingExecutor{}
	executor := NewExecutor()
	executor.geteuid = func() int { return 0 }
	executor.local = recorder
	executor.getenv = func(key string) string {
		if key == "TERM" {
			return "xterm"
		}
		return ""
	}
	_, err := executor.Run(t.Context(), process.Options{Command: "tool", Dir: dir, Env: map[string]string{"PATH": dir, "VALUE": "step"}})
	if err != nil {
		t.Fatal(err)
	}
	if recorder.options.Command != command || recorder.options.Dir != dir {
		t.Fatalf("command = %q, dir = %q", recorder.options.Command, recorder.options.Dir)
	}
	if recorder.options.Env["VALUE"] != "step" || recorder.options.Env["USER"] != "root" || recorder.options.Env["TERM"] != "xterm" {
		t.Fatalf("environment = %#v", recorder.options.Env)
	}
}

func TestExecutionHonorsCanceledContextBeforeAuthentication(t *testing.T) {
	executor := NewExecutor()
	executor.geteuid = func() int { return 0 }
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := executor.Run(ctx, process.Options{Command: "ignored"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestAuthorizePromptsOnlyAfterNonInteractiveValidationFails(t *testing.T) {
	dir := t.TempDir()
	sudo := filepath.Join(dir, "sudo")
	script := "#!/bin/sh\nif [ \"$1\" = -n ]; then exit 1; fi\nexit 0\n"
	if err := os.WriteFile(sudo, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	executor := NewExecutor()
	executor.lookPath = func(string) (string, error) { return sudo, nil }
	executor.getenv = func(string) string { return "" }
	var prompt bytes.Buffer
	if err := executor.authorize(t.Context(), process.Options{Interactive: true, PromptStdin: bytes.NewReader(nil), PromptStderr: &prompt}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(prompt.String(), "administrator privileges") {
		t.Fatalf("prompt = %q", prompt.String())
	}
}

func TestAuthorizeRejectsMissingCredentialsWithoutInteraction(t *testing.T) {
	dir := t.TempDir()
	sudo := filepath.Join(dir, "sudo")
	if err := os.WriteFile(sudo, []byte("#!/bin/sh\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	executor := NewExecutor()
	executor.lookPath = func(string) (string, error) { return sudo, nil }
	executor.getenv = func(string) string { return "" }
	err := executor.authorize(t.Context(), process.Options{PromptStderr: io.Discard})
	if err == nil || !strings.Contains(err.Error(), "non-interactive") {
		t.Fatalf("authorize() error = %v", err)
	}
}

func TestTransportRequestIsStrictAndOwnedByCaller(t *testing.T) {
	command := filepath.Join(t.TempDir(), "tool")
	if err := os.WriteFile(command, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	dir, listener, nonce, err := prepareTransport("/tmp")
	if err != nil {
		if errors.Is(err, syscall.EPERM) {
			t.Skip("sandbox does not permit Unix sockets")
		}
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	defer listener.Close()
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	stat := info.Sys().(*syscall.Stat_t)
	if err := validateTransportDir(dir, stat.Uid, stat.Gid); err != nil {
		t.Fatal(err)
	}
	options := process.Options{Command: command, Dir: filepath.Dir(command), Interactions: secretPlan(t)}
	events := make(chan message)
	result := make(chan error, 1)
	go serveHelper(listener, buildRequest(nonce, options), nonce, events, result)
	go func() {
		for range events {
		}
	}()
	connection, err := net.Dial("unix", filepath.Join(dir, socketName))
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	payload, err := readRequest(connection)
	if err != nil {
		t.Fatal(err)
	}
	if payload.Version != protocolVersion || payload.Nonce != nonce || payload.Command != command {
		t.Fatalf("request = %#v", payload)
	}
	// The request carries PTY interaction specs marked sensitive, so nothing but the control
	// socket may ever land in the transport directory.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() != socketName {
			t.Fatalf("transport directory holds %q", entry.Name())
		}
	}
}

func secretPlan(t *testing.T) *ptyinteract.Plan {
	t.Helper()
	plan, err := ptyinteract.Compile([]ptyinteract.Spec{{Expect: "Password:", Send: "hunter2", Sensitive: true}})
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func TestResolveExecutableIgnoresRelativePathEntries(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "build"), []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, _, err := resolveExecutable("build", dir, "/nonexistent-elevation-path:"); err == nil {
		t.Fatal("an empty PATH element resolved against the run directory")
	}
	resolved, _, err := resolveExecutable("build", dir, "/nonexistent-elevation-path:"+dir)
	if err != nil {
		t.Fatal(err)
	}
	if resolved != filepath.Join(dir, "build") {
		t.Fatalf("resolved = %q", resolved)
	}
}

func TestHandleHelperIgnoresNormalInvocation(t *testing.T) {
	if _, handled := HandleHelper([]string{"wuko", "run", "workflow"}); handled {
		t.Fatal("ordinary invocation was treated as helper mode")
	}
}
