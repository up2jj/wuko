//go:build linux

package ssh

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/up2jj/wuko/engine"
	"github.com/up2jj/wuko/executor"
	"github.com/up2jj/wuko/process"
	"github.com/up2jj/wuko/step"
	editstep "github.com/up2jj/wuko/steps/edit"
	filestep "github.com/up2jj/wuko/steps/file"
	"github.com/up2jj/wuko/steps/shell"
	"github.com/up2jj/wuko/workflow"
)

// Opt-in locally; CI requires this suite with a throwaway loopback sshd. No
// daemon configuration, user keys or known-hosts files outside t.TempDir change.
func TestSSHIntegration(t *testing.T) {
	if os.Getenv("WUKO_SSH_INTEGRATION") != "1" {
		t.Skip("set WUKO_SSH_INTEGRATION=1 with openssh-server installed")
	}
	config := sshdFixture(t)
	open := func(t *testing.T) *session {
		t.Helper()
		p := &ExecutorProvider{config: ExecutorConfig{Host: "fixture", Directory: t.TempDir(), ConfigFile: config}, run: process.LocalExecutor{}.Run}
		opened, err := p.Open(t.Context(), executor.Request{RunDir: t.TempDir(), Env: hostEnvironment(), Stdout: io.Discard, Stderr: io.Discard})
		if err != nil {
			t.Fatal(err)
		}
		s := opened.(*session)
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := s.Close(ctx); err != nil && !strings.Contains(err.Error(), "termination could not be confirmed") {
				t.Error(err)
			}
		})
		return s
	}
	t.Run("commands and files", func(t *testing.T) {
		s := open(t)
		if _, err := os.Stat(s.socket); err != nil {
			t.Fatal(err)
		}
		nested := filepath.Join(s.request.RunDir, "nested")
		if err := s.MkdirAll(t.Context(), nested, 0o710); err != nil {
			t.Fatal(err)
		}
		name := filepath.Join(nested, "quote' space\nfile")
		data := []byte{'a', 0, 'b', '\n', 255}
		if err := s.WriteFile(t.Context(), name, data, executor.WriteOptions{Mode: 0o640}); err != nil {
			t.Fatal(err)
		}
		if err := s.WriteFile(t.Context(), name, []byte("wrong"), executor.WriteOptions{Mode: 0o600}); !errors.Is(err, fs.ErrExist) {
			t.Fatalf("overwrite = %v", err)
		}
		got, err := s.ReadFile(t.Context(), name, 2)
		if err != nil || !bytes.Equal(got, data[:3]) {
			t.Fatalf("bounded read = %q, %v", got, err)
		}
		info, err := s.Stat(t.Context(), name)
		if err != nil || info.Mode.Perm() != 0o640 || info.Size != int64(len(data)) {
			t.Fatalf("metadata = %#v, %v", info, err)
		}
		if err := s.WriteFile(t.Context(), name, []byte("new"), executor.WriteOptions{Mode: 0o600, Replace: true}); err != nil {
			t.Fatal(err)
		}
		// An interrupted upload must never install a truncated replacement.
		result, uploadErr := s.script(t.Context(), installFileScript, []string{s.mapPath(name), filepath.Dir(s.mapPath(name)), "0600", "true", "100"}, process.Options{
			Stdin: strings.NewReader("short"), StdoutPolicy: process.OutputDiscard, StderrPolicy: process.OutputCapture,
		})
		if uploadErr == nil || !strings.Contains(result.Stderr, "incomplete remote file upload") {
			t.Fatalf("incomplete upload = %#v, %v", result, uploadErr)
		}
		got, err = s.ReadFile(t.Context(), name, 0)
		if err != nil || string(got) != "new" {
			t.Fatalf("incomplete replacement changed destination: %q, %v", got, err)
		}
		// Two writers bypass a preflight Stat and race the atomic no-replace install.
		var writers sync.WaitGroup
		writes := make(chan error, 2)
		for _, content := range []string{"first", "second"} {
			writers.Go(func() {
				result, err := s.script(t.Context(), installFileScript, []string{filepath.Join(s.config.Directory, "race"), s.config.Directory, "0600", "false", strconv.Itoa(len(content))}, process.Options{
					Stdin: strings.NewReader(content), StdoutPolicy: process.OutputDiscard, StderrPolicy: process.OutputCapture,
				})
				if err != nil {
					err = fileError("write", "race", result, err)
				}
				writes <- err
			})
		}
		writers.Wait()
		close(writes)
		success, exists := 0, 0
		for err := range writes {
			switch {
			case err == nil:
				success++
			case errors.Is(err, fs.ErrExist):
				exists++
			default:
				t.Fatal(err)
			}
		}
		if success != 1 || exists != 1 {
			t.Fatalf("atomic writes: success=%d, exists=%d", success, exists)
		}
		for _, missing := range []string{"missing", "missing/parent/file"} {
			if _, err := s.Stat(t.Context(), filepath.Join(s.request.RunDir, missing)); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("missing stat = %v", err)
			}
		}
		result, err = s.Run(t.Context(), process.Options{Command: "sh", Args: []string{"-c", `printf out; printf err >&2; exit 255`}})
		var exited *process.ExitError
		if !errors.As(err, &exited) || result.ExitCode != 255 || result.Stdout != "out" || result.Stderr != "err" {
			t.Fatalf("remote exit 255 = %#v, %v", result, err)
		}
		result, err = s.Run(t.Context(), process.Options{Command: "cat", Stdin: bytes.NewReader(data)})
		if err != nil || !bytes.Equal([]byte(result.Stdout), data) {
			t.Fatalf("binary stdin = %#v, %v", result, err)
		}
		s.config.ForwardEnv = []string{"FORWARDED", "MISSING"}
		s.config.Env = map[string]string{"OVERRIDE": "explicit"}
		result, err = s.Run(t.Context(), process.Options{Command: "sh", Args: []string{"-c", `printf '%s|%s|%s' "$FORWARDED" "$OVERRIDE" "$LOCAL_SECRET"`}, Env: map[string]string{"FORWARDED": "allowed", "OVERRIDE": "wrong", "LOCAL_SECRET": "private"}})
		if err != nil || result.Stdout != "allowed|explicit|" {
			t.Fatalf("environment = %#v, %v", result, err)
		}
		_, err = s.Run(t.Context(), process.Options{Command: "ln", Args: []string{"-s", "missing-target", "link"}})
		if err != nil {
			t.Fatal(err)
		}
		info, err = s.Stat(t.Context(), filepath.Join(s.request.RunDir, "link"))
		if err != nil || info.Mode&fs.ModeSymlink == 0 {
			t.Fatalf("symlink stat = %#v, %v", info, err)
		}
		if _, err := s.ReadFile(t.Context(), filepath.Join(s.request.RunDir, "link"), 0); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("reading dangling symlink = %v", err)
		}
		local, remote := s.localDir, s.remoteDir
		if err := s.Close(t.Context()); err != nil {
			t.Fatal(err)
		}
		for _, directory := range []string{local, remote} {
			if _, err := os.Stat(directory); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("resource leaked: %s: %v", directory, err)
			}
		}
	})
	t.Run("opening failure and timeout", func(t *testing.T) {
		p := &ExecutorProvider{config: ExecutorConfig{Host: "fixture", Directory: filepath.Join(t.TempDir(), "missing"), ConfigFile: config}, run: process.LocalExecutor{}.Run}
		if _, err := p.Open(t.Context(), executor.Request{RunDir: t.TempDir(), Env: hostEnvironment()}); err == nil {
			t.Fatal("opened missing remote directory")
		}
		s := open(t)
		ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
		defer cancel()
		if _, err := s.Run(ctx, process.Options{Command: "sleep", Args: []string{"300"}}); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("timeout = %v", err)
		}
		if _, err := s.Run(t.Context(), process.Options{Command: "true"}); err != nil {
			t.Fatalf("session unusable after timeout: %v", err)
		}
	})
	t.Run("workflow", func(t *testing.T) {
		for _, ending := range []string{"success", "failure", "return"} {
			t.Run(ending, func(t *testing.T) {
				local, remote := t.TempDir(), t.TempDir()
				last := "    - id: finish\n      type: shell\n      with: {command: 'true'}\n"
				if ending == "failure" {
					last = "    - id: finish\n      type: shell\n      with: {script: 'exit 7'}\n"
				} else if ending == "return" {
					last = "    - return: {outputs: {}}\n"
				}
				body := fmt.Sprintf(`version: 1
name: ssh-integration
steps:
  - id: before
    type: shell
    with: {command: pwd}
  - executor:
      type: ssh
      with: {host: fixture, directory: %q, config_file: %q}
    steps:
    - id: directory
      type: file
      with: {operation: mkdir, path: nested, recursive: true}
    - working_directory: nested
      steps:
      - id: write
        type: file
        with: {operation: write, path: config.json, content: '{"version":"1"}'}
      - id: edit
        type: edit
        with: {operation: set, from: {file: config.json}, path: $.version, value: '2'}
      - id: remote
        type: shell
        with: {command: pwd}
%s
    finally:
    - id: cleanup
      type: file
      with: {operation: write, path: cleaned, content: done}
  - id: after
    type: shell
    with: {command: pwd}
`, remote, config, last)
				definition, err := workflow.NewLoader(nil).DecodeStdinContext(t.Context(), strings.NewReader(body), local, workflow.LoadOptions{RunDir: local})
				if err != nil {
					t.Fatal(err)
				}
				registry := step.NewRegistry()
				for _, register := range []func(*step.Registry) error{shell.Register, filestep.Register, editstep.Register} {
					if err := register(registry); err != nil {
						t.Fatal(err)
					}
				}
				executors := executor.NewRegistry()
				if err := RegisterExecutor(executors); err != nil {
					t.Fatal(err)
				}
				state, err := engine.New(registry, engine.WithExecutors(executors)).Run(t.Context(), definition, engine.Options{RunDir: local, Stdout: io.Discard, Stderr: io.Discard})
				if (err != nil) != (ending == "failure") {
					t.Fatalf("workflow error = %v", err)
				}
				if data, err := os.ReadFile(filepath.Join(remote, "cleaned")); err != nil || string(data) != "done" {
					t.Fatalf("finally = %q, %v", data, err)
				}
				if _, err := os.Stat(filepath.Join(local, "nested")); !errors.Is(err, fs.ErrNotExist) {
					t.Fatalf("file operation touched local workspace: %v", err)
				}
				if ending == "success" {
					for id, want := range map[string]string{"before": local, "remote": filepath.Join(remote, "nested"), "after": local} {
						if got := strings.TrimSpace(state.Steps[id].(map[string]any)["stdout"].(string)); got != want {
							t.Fatalf("%s directory = %q, want %q", id, got, want)
						}
					}
				}
			})
		}
	})
	t.Run("cancellation and descendants", func(t *testing.T) {
		s := open(t)
		pidFile := filepath.Join(s.config.Directory, "pid")
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		done := make(chan error, 1)
		go func() {
			_, err := s.Run(ctx, process.Options{Command: "sh", Args: []string{"-c", `sleep 300 & echo $! > "$1"; wait`, "sh", pidFile}})
			done <- err
		}()
		waitFor(t, func() bool { _, err := os.Stat(pidFile); return err == nil })
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation = %v", err)
		}
		assertProcessStopped(t, pidFile)
		if _, err := s.Run(t.Context(), process.Options{Command: "true"}); err != nil {
			t.Fatalf("session unusable after cancellation: %v", err)
		}
		_, err := s.Run(t.Context(), process.Options{Command: "sh", Args: []string{"-c", `sleep 300 & echo $! > "$1"; exit 0`, "sh", pidFile}})
		if err != nil {
			t.Fatal(err)
		}
		assertProcessStopped(t, pidFile)
		// A command handling TERM keeps its grace period: the leader's interrupted wait
		// must not record completion while the command is still shutting down.
		marker := filepath.Join(s.config.Directory, "graceful")
		_ = os.Remove(pidFile)
		ctx, cancel = context.WithCancel(t.Context())
		defer cancel()
		go func() {
			_, err := s.Run(ctx, process.Options{Command: "sh", Args: []string{"-c", `trap 'sleep 0.3; touch "$2"; exit 0' TERM; echo $$ > "$1"; while :; do sleep 0.05; done`, "sh", pidFile, marker}})
			done <- err
		}()
		waitFor(t, func() bool { _, err := os.Stat(pidFile); return err == nil })
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatalf("graceful cancellation = %v", err)
		}
		if _, err := os.Stat(marker); err != nil {
			t.Fatalf("TERM handler was killed before its grace period ended: %v", err)
		}
	})
	t.Run("lost transport never reconnects", func(t *testing.T) {
		s := open(t)
		pidFile := filepath.Join(s.config.Directory, "pid")
		done := make(chan error, 1)
		go func() {
			_, err := s.Run(t.Context(), process.Options{Command: "sh", Args: []string{"-c", `echo $$ > "$1"; sleep 300`, "sh", pidFile}})
			done <- err
		}()
		waitFor(t, func() bool { _, err := os.Stat(pidFile); return err == nil })
		s.stopMaster()
		if err := <-done; err == nil || !strings.Contains(err.Error(), "remote termination could not be confirmed") {
			t.Fatalf("lost transport = %v", err)
		}
		if _, err := s.Run(t.Context(), process.Options{Command: "touch", Args: []string{"must-not-run"}}); err == nil {
			t.Fatal("reconnected after master died")
		}
		if _, err := os.Stat(filepath.Join(s.config.Directory, "must-not-run")); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("command replayed: %v", err)
		}
	})
}

func hostEnvironment() map[string]string {
	values := make(map[string]string)
	for _, item := range os.Environ() {
		key, value, _ := strings.Cut(item, "=")
		values[key] = value
	}
	return values
}

func waitFor(t *testing.T, ready func() bool) {
	t.Helper()
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for !ready() {
		select {
		case <-timer.C:
			t.Fatal("timed out waiting for SSH fixture")
		case <-ticker.C:
		}
	}
}

func assertProcessStopped(t *testing.T, file string) {
	t.Helper()
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 1 {
		t.Fatalf("invalid child PID %q", data)
	}
	waitFor(t, func() bool {
		stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		return errors.Is(err, fs.ErrNotExist) || strings.HasPrefix(string(stat[strings.LastIndex(string(stat), ")")+1:]), " Z")
	})
}

func sshdFixture(t *testing.T) string {
	t.Helper()
	directory := t.TempDir()
	for _, key := range []string{"host", "client"} {
		if output, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", filepath.Join(directory, key)).CombinedOutput(); err != nil {
			t.Fatalf("keygen: %s: %v", output, err)
		}
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	account, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	server := fmt.Sprintf("Port %d\nListenAddress 127.0.0.1\nHostKey %s/host\nPidFile %s/pid\nAuthorizedKeysFile %s/client.pub\nStrictModes no\nUsePAM yes\nPasswordAuthentication no\nKbdInteractiveAuthentication no\nPermitRootLogin prohibit-password\nAllowUsers %s\n", port, directory, directory, directory, account.Username)
	serverFile := filepath.Join(directory, "sshd_config")
	if err := os.WriteFile(serverFile, []byte(server), 0o600); err != nil {
		t.Fatal(err)
	}
	daemon := exec.Command("/usr/sbin/sshd", "-D", "-e", "-f", serverFile)
	var log bytes.Buffer
	daemon.Stdout, daemon.Stderr = &log, &log
	if err := daemon.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- daemon.Wait() }()
	t.Cleanup(func() {
		_ = daemon.Process.Kill()
		<-done
		if t.Failed() {
			t.Log(log.String())
		}
	})
	waitFor(t, func() bool {
		connection, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 100*time.Millisecond)
		if err == nil {
			_ = connection.Close()
		}
		return err == nil
	})
	publicKey, err := os.ReadFile(filepath.Join(directory, "host.pub"))
	if err != nil {
		t.Fatal(err)
	}
	known := filepath.Join(directory, "known_hosts")
	if err := os.WriteFile(known, []byte(fmt.Sprintf("[127.0.0.1]:%d %s", port, publicKey)), 0o600); err != nil {
		t.Fatal(err)
	}
	// These user-config settings must not change executor ownership, consume stdin,
	// force a terminal, or replace the helper command.
	client := fmt.Sprintf("Host fixture\n HostName 127.0.0.1\n Port %d\n User %s\n IdentityFile %s/client\n IdentitiesOnly yes\n StrictHostKeyChecking yes\n UserKnownHostsFile %s\n ForkAfterAuthentication yes\n StdinNull yes\n SessionType none\n RequestTTY force\n RemoteCommand false\n", port, account.Username, directory, known)
	clientFile := filepath.Join(directory, "ssh_config")
	if err := os.WriteFile(clientFile, []byte(client), 0o600); err != nil {
		t.Fatal(err)
	}
	return clientFile
}
