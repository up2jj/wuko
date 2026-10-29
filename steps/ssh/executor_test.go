package ssh

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/up2jj/wuko/executor"
	"github.com/up2jj/wuko/internal/fakecli"
	"github.com/up2jj/wuko/process"
)

func TestExecutorConfiguration(t *testing.T) {
	for _, test := range []struct {
		name string
		with map[string]any
	}{
		{"missing host", map[string]any{"host": ""}},
		{"option host", map[string]any{"host": "-Fbad"}},
		{"relative directory", map[string]any{"directory": "project"}},
		{"port", map[string]any{"port": 65536}},
		{"timeout", map[string]any{"connect_timeout": "0s"}},
		{"environment name", map[string]any{"forward_env": []string{"BAD=KEY"}}},
		{"NUL environment", map[string]any{"env": map[string]string{"KEY": "a\x00b"}}},
		{"unknown field", map[string]any{"password": "no"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw := map[string]any{"host": "production", "directory": "/srv/app"}
			maps.Copy(raw, test.with)
			if _, err := NewExecutor(raw); err == nil {
				t.Fatal("expected invalid configuration")
			}
		})
	}
	if _, err := NewExecutor(map[string]any{"host": "{{ .vars.host }}", "directory": "{{ .vars.directory }}", "connect_timeout": "{{ .vars.timeout }}"}); err != nil {
		t.Fatal(err)
	}
}

func TestStepRestrictions(t *testing.T) {
	p := &ExecutorProvider{}
	for _, test := range []struct {
		kind string
		raw  map[string]any
	}{
		{"process", nil}, {"shell", map[string]any{"tty": true}},
		{"shell", map[string]any{"user": "root"}}, {"shell", map[string]any{"interact": true}},
		{"shell", map[string]any{"interactions": []any{}}},
	} {
		if err := p.ValidateStep(test.kind, test.raw); err == nil {
			t.Fatalf("accepted %s %#v", test.kind, test.raw)
		}
	}
	for _, kind := range []string{"shell", "file", "edit"} {
		if err := p.ValidateStep(kind, nil); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPathMappingAndEnvironment(t *testing.T) {
	s := &session{config: ExecutorConfig{Directory: "/srv/app", ForwardEnv: []string{"RELEASE", "MISSING"}, Env: map[string]string{"RELEASE": "override", "EXPLICIT": "value"}}, request: executor.Request{RunDir: "/local/project"}}
	for input, want := range map[string]string{"": "/srv/app", "/local/project": "/srv/app", "/local/project/a b": "/srv/app/a b", "nested/file": "/srv/app/nested/file", "/etc/app.conf": "/etc/app.conf", "/local/project-other": "/local/project-other"} {
		if got := s.mapPath(input); got != want {
			t.Errorf("mapPath(%q) = %q, want %q", input, got, want)
		}
	}
	got, err := s.environment(map[string]string{"PATH": "/local/bin", "HOME": "/local/home", "RELEASE": "step"})
	if want := "export EXPLICIT='value'\nexport RELEASE='override'\n"; err != nil || got != want {
		t.Fatalf("environment = %q, %v", got, err)
	}
	if _, err := s.environment(map[string]string{"MISSING": "a\x00b"}); err == nil {
		t.Fatal("accepted NUL in a forwarded value")
	}
}

func TestRemoteShellQuotingAndTransportIsolation(t *testing.T) {
	values := []string{"", "a b", "'", `a"b`, "$HOME", "$(touch unexpected)", "a;b", "*.go", "line\nbreak", "-option"}
	s := &session{config: ExecutorConfig{Host: "production"}, request: executor.Request{RunDir: t.TempDir(), Env: map[string]string{"PATH": os.Getenv("PATH")}}}
	s.run = func(ctx context.Context, options process.Options) (process.Result, error) {
		command := options.Args[len(options.Args)-1]
		if !slices.Contains(options.Args, "ProxyCommand=false") || !slices.Contains(options.Args, "ProxyJump=none") {
			t.Fatal("slave could reconnect without its owned master")
		}
		if !slices.Contains(options.Args, "SessionType=default") {
			t.Fatal("slave must execute its command even with SessionType none configured")
		}
		return process.Run(ctx, process.Options{Command: "/bin/sh", Args: []string{"-c", command}, Env: options.Env, StdoutPolicy: process.OutputCapture})
	}
	result, err := s.script(t.Context(), `printf '%s\000' "$@"`, values, process.Options{})
	if err != nil || result.Stdout != strings.Join(values, "\x00")+"\x00" {
		t.Fatalf("result = %#v, err = %v", result, err)
	}
}

func TestConnectionArguments(t *testing.T) {
	s := &session{socket: "/tmp/private/s", config: ExecutorConfig{User: "deploy", Port: 2222, IdentityFile: "keys/id", ConfigFile: "ssh.conf"}, request: executor.Request{RunDir: "/local/project"}}
	args := s.connectionArgs(1500 * time.Millisecond)
	for _, value := range []string{"BatchMode=yes", "ConnectTimeout=2", "/local/project/keys/id", "/local/project/ssh.conf", "2222", "deploy", "ControlPersist=no", "ForkAfterAuthentication=no", "StdinNull=no"} {
		if !slices.Contains(args, value) {
			t.Errorf("missing %q in %q", value, args)
		}
	}
}

func TestFileModes(t *testing.T) {
	for unix, want := range map[uint32]fs.FileMode{0o100640: 0o640, 0o040755: fs.ModeDir | 0o755, 0o120777: fs.ModeSymlink | 0o777, 0o104755: fs.ModeSetuid | 0o755, 0o010600: fs.ModeNamedPipe | 0o600} {
		if got := fileMode(unix); got != want {
			t.Errorf("fileMode(%o) = %v, want %v", unix, got, want)
		}
	}
}

// Re-enter the test binary as a fake ssh executable. Remote helper invocation is
// decoded by a real shell, so argument quoting and actual os/exec wiring are tested.
func TestMain(main *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "--fake-remote" {
		fakecli.Exit(fakeRemote(os.Args[2], os.Args[4:]))
	}
	if filepath.Base(os.Args[0]) == "ssh" {
		fakecli.Exit(fakeSSH(os.Args[1:]))
	}
	os.Exit(main.Run())
}

func fakeSSH(args []string) int {
	var socket, operation string
	for i := 0; i+1 < len(args); i++ {
		switch args[i] {
		case "-S":
			socket = args[i+1]
		case "-O":
			operation = args[i+1]
		}
	}
	if slices.Contains(args, "-M") {
		if err := os.WriteFile(socket, nil, 0o600); err != nil {
			return 1
		}
		for {
			if _, err := os.Stat(socket); err != nil {
				return 0
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	if _, err := os.Stat(socket); err != nil {
		return 255
	}
	if operation == "check" {
		return 0
	}
	if operation == "exit" {
		_ = os.Remove(socket)
		return 0
	}
	command := args[len(args)-1]
	if !strings.HasPrefix(command, "exec sh -c ") {
		return 99
	}
	self, _ := os.Executable()
	child := exec.Command("/bin/sh", "-c", "exec "+quote(self)+" --fake-remote "+strings.TrimPrefix(command, "exec sh -c "))
	child.Stdin, child.Stdout, child.Stderr = os.Stdin, os.Stdout, os.Stderr
	child.Env = []string{"PATH=" + os.Getenv("PATH"), "SERVER_ONLY=remote", "HOME=/remote/home"}
	return childExit(child.Run())
}

func childExit(err error) int {
	if err == nil {
		return 0
	}
	var exited *exec.ExitError
	if errors.As(err, &exited) {
		return exited.ExitCode()
	}
	return 99
}

func fakeRemote(script string, args []string) int {
	switch script {
	case initializeScript:
		if info, err := os.Stat(args[0]); err != nil || !info.IsDir() {
			return 1
		}
		dir, err := os.MkdirTemp("/tmp", "wuko-ssh.")
		if err != nil {
			return 1
		}
		fmt.Println(dir)
		return 0
	case runScript:
		child := exec.Command("/bin/sh", append([]string{"-c", `. "$1" && shift && exec "$@"`, "wuko", filepath.Join(args[0], "env")}, args[4:]...)...)
		child.Dir = args[1]
		child.Stdin, child.Stdout, child.Stderr = os.Stdin, os.Stdout, os.Stderr
		code := childExit(child.Run())
		if err := os.WriteFile(filepath.Join(args[0], "status"), []byte(strconv.Itoa(code)), 0o600); err != nil {
			return 1
		}
		return 0
	default:
		child := exec.Command("/bin/sh", append([]string{"-c", script, "wuko"}, args...)...)
		child.Stdin, child.Stdout, child.Stderr = os.Stdin, os.Stdout, os.Stderr
		return childExit(child.Run())
	}
}

func fakeProvider(t *testing.T) (*ExecutorProvider, executor.Request) {
	t.Helper()
	bin := t.TempDir()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(self, filepath.Join(bin, "ssh")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	local, remote := t.TempDir(), t.TempDir()
	p := &ExecutorProvider{config: ExecutorConfig{Host: "fake", Directory: remote, ForwardEnv: []string{"RELEASE"}}, run: process.LocalExecutor{}.Run}
	return p, executor.Request{RunDir: local, Env: map[string]string{"PATH": os.Getenv("PATH"), "HOME": "/local/home", "LOCAL_SECRET": "private"}, Stdout: io.Discard, Stderr: io.Discard}
}

func TestFakeSSHCommandsAndCleanup(t *testing.T) {
	p, request := fakeProvider(t)
	opened, err := p.Open(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	s := opened.(*session)
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	var streamed bytes.Buffer
	result, err := s.Run(t.Context(), process.Options{Command: "sh", Args: []string{"-c", `printf '%s|%s|%s|%s' "$PWD" "$SERVER_ONLY" "$HOME" "$RELEASE"; printf err >&2; test -z "$LOCAL_SECRET"`}, Env: map[string]string{"RELEASE": "v2", "HOME": "/bad/home", "LOCAL_SECRET": "private"}, Stdout: &streamed, CaptureLimit: 8})
	if err != nil || streamed.String() != p.config.Directory+"|remote|/remote/home|v2" || !result.StdoutTruncated || result.Stderr != "err" {
		t.Fatalf("result = %#v, stream = %q, err = %v", result, streamed.String(), err)
	}
	result, err = s.Run(t.Context(), process.Options{Command: "sh", Args: []string{"-c", "exit 255"}})
	var exit *process.ExitError
	if !errors.As(err, &exit) || exit.Code != 255 || result.ExitCode != 255 {
		t.Fatalf("exit 255 = %#v, %v", result, err)
	}
	input := []byte{'a', 0, 'b', '\n'}
	result, err = s.Run(t.Context(), process.Options{Command: "cat", Stdin: bytes.NewReader(input)})
	if err != nil || !bytes.Equal([]byte(result.Stdout), input) {
		t.Fatalf("stdin = %#v, %v", result, err)
	}
	localDir, remoteDir := s.localDir, s.remoteDir
	if err := s.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, directory := range []string{localDir, remoteDir} {
		if _, err := os.Stat(directory); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("resource %s remains: %v", directory, err)
		}
	}
	if err := s.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestFakeSSHFailedOpeningReapsMaster(t *testing.T) {
	p, request := fakeProvider(t)
	p.config.Directory = filepath.Join(t.TempDir(), "missing")
	var socket string
	var mu sync.Mutex
	p.run = func(ctx context.Context, options process.Options) (process.Result, error) {
		mu.Lock()
		for i, arg := range options.Args {
			if arg == "-S" {
				socket = options.Args[i+1]
				break
			}
		}
		mu.Unlock()
		return process.Run(ctx, options)
	}
	if _, err := p.Open(t.Context(), request); err == nil {
		t.Fatal("opened nonexistent remote directory")
	}
	if _, err := os.Stat(filepath.Dir(socket)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("socket directory leaked: %v", err)
	}
}

func TestFakeSSHKeepsEnvironmentOutOfArguments(t *testing.T) {
	p, request := fakeProvider(t)
	var mu sync.Mutex
	var arguments []string
	p.run = func(ctx context.Context, options process.Options) (process.Result, error) {
		mu.Lock()
		arguments = append(arguments, options.Args...)
		mu.Unlock()
		return process.Run(ctx, options)
	}
	opened, err := p.Open(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	s := opened.(*session)
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	result, err := s.Run(t.Context(), process.Options{Command: "sh", Args: []string{"-c", `printf %s "$RELEASE"`}, Env: map[string]string{"RELEASE": "it's $secret"}, StdoutPolicy: process.OutputCapture})
	if err != nil || result.Stdout != "it's $secret" {
		t.Fatalf("result = %#v, %v", result, err)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, arg := range arguments {
		if strings.Contains(arg, "secret") {
			t.Fatalf("environment value exposed in ssh arguments: %q", arg)
		}
	}
}

func TestFakeSSHFileSystemMissingParent(t *testing.T) {
	p, request := fakeProvider(t)
	opened, err := p.Open(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	s := opened.(*session)
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	err = s.WriteFile(t.Context(), "missing/file", []byte("data"), executor.WriteOptions{Mode: 0o600})
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("write into missing parent = %v", err)
	}
}
