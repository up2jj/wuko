package stepadapter_test

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/up2jj/wuko/executor"
	"github.com/up2jj/wuko/plugin/plugintest"
	"github.com/up2jj/wuko/plugin/sdk"
	"github.com/up2jj/wuko/plugin/stepadapter"
	"github.com/up2jj/wuko/process"
	"github.com/up2jj/wuko/ptyinteract"
	"github.com/up2jj/wuko/step"
	"golang.org/x/term"
)

// hostSession runs commands on the host and exposes a task graph, a capability the protocol
// itself does not carry.
type hostSession struct {
	process.LocalExecutor
	executor.LocalFileSystem
}

func (hostSession) Close(context.Context) error { return nil }
func (hostSession) RunTask(_ context.Context, request executor.TaskRequest) (process.Result, error) {
	return process.Result{Stdout: "task " + request.Name}, nil
}

type hostProvider struct{}

func (hostProvider) Open(context.Context, executor.Request) (executor.Session, error) {
	return hostSession{}, nil
}

type taskRunner struct{ name string }

func (runner taskRunner) Run(ctx context.Context, request step.Request) (step.Result, error) {
	tasks, ok := request.Executor.(executor.TaskRunner)
	if !ok || !request.InsideExecutor {
		return step.Result{Outputs: map[string]any{"ran": "outside"}}, nil
	}
	result, err := tasks.RunTask(ctx, executor.TaskRequest{Name: runner.name})
	return step.Result{Outputs: map[string]any{"ran": result.Stdout}}, err
}

func newNativePlugin(t *testing.T) (executor.Session, step.Runner) {
	t.Helper()
	plugin, err := sdk.New("native")
	if err != nil {
		t.Fatal(err)
	}
	if err := stepadapter.RegisterExecutor(plugin, "host", stepadapter.ExecutorRegistration{
		Builder:    func(map[string]any) (executor.Provider, error) { return hostProvider{}, nil },
		FileSystem: true, Stdin: true, TTY: true,
	}); err != nil {
		t.Fatal(err)
	}
	outputs := step.ClosedObject(map[string]step.OutputSchema{"ran": step.Scalar()})
	if err := stepadapter.Register(plugin, "task", stepadapter.Registration{
		Builder: func(raw map[string]any) (step.Runner, error) {
			name, _ := raw["name"].(string)
			return taskRunner{name: name}, nil
		},
		Outputs: &outputs, Executor: true,
	}); err != nil {
		t.Fatal(err)
	}
	manager := plugintest.Connect(t, plugin)
	provider, err := manager.ResolveExecutor(t.Context(), "native.host", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	session, err := provider.Open(t.Context(), executor.Request{RunDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close(context.Background()) })
	runner, err := manager.ResolveStep(t.Context(), "native.task", map[string]any{"name": "build"})
	if err != nil {
		t.Fatal(err)
	}
	schema, declared, err := manager.ResolveOutputSchema(t.Context(), "native.task")
	if err != nil || !declared || schema.Open || schema.Fields["ran"].Kind != step.OutputScalar {
		t.Fatalf("output schema = %+v, %v, %v", schema, declared, err)
	}
	return session, runner
}

func environment() map[string]string {
	return map[string]string{"PATH": os.Getenv("PATH"), "TERM": "dumb"}
}

func TestRegisterExecutorRunsCommands(t *testing.T) {
	session, _ := newNativePlugin(t)
	result, err := session.Run(t.Context(), process.Options{Command: "sh", Args: []string{"-c", "cat; exit 3"}, Env: environment(), Stdin: strings.NewReader("piped"), StdoutPolicy: process.OutputCapture})
	var exited *process.ExitError
	if !errors.As(err, &exited) || exited.Code != 3 || result.Stdout != "piped" {
		t.Fatalf("Run() = %+v, %v; want piped output and exit code 3", result, err)
	}
}

func TestRegisterExecutorBridgesTTYRuns(t *testing.T) {
	session, _ := newNativePlugin(t)
	plan, err := ptyinteract.Compile([]ptyinteract.Spec{{HasExpect: true, Expect: "name:", Send: "bob", Newline: true}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := session.Run(t.Context(), process.Options{
		Command: "sh", Args: []string{"-c", `test -t 0 || exit 9; stty size; printf 'name:'; IFS= read -r name; printf 'hello=%s\n' "$name"`},
		Env: environment(), TTY: true, Interactions: plan, StdoutPolicy: process.OutputCapture, CaptureLimit: 1 << 20,
	})
	if err != nil {
		t.Fatalf("Run() = %+v, %v", result, err)
	}
	for _, want := range []string{"24 80", "hello=bob"} {
		if !strings.Contains(result.Stdout, want) {
			t.Fatalf("TTY output = %q, want %q", result.Stdout, want)
		}
	}
}

func TestRegisterExecutorExposesTheSessionFileSystem(t *testing.T) {
	session, _ := newNativePlugin(t)
	filesystem, err := executor.FileSystemFor(session)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "file")
	if err := filesystem.WriteFile(t.Context(), path, []byte("data"), executor.WriteOptions{Mode: 0o600}); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "data" {
		t.Fatalf("written file = %q, %v", data, err)
	}
}

func TestRegisterHandsTheNativeSessionToItsSteps(t *testing.T) {
	session, runner := newNativePlugin(t)
	if _, ok := runner.(step.ExecutorAware); !ok {
		t.Fatal("the step is not executor-aware")
	}
	inside, err := runner.Run(t.Context(), step.Request{StepID: "task", InsideExecutor: true, Executor: session})
	if err != nil || inside.Outputs["ran"] != "task build" {
		t.Fatalf("Run() inside the executor = %#v, %v", inside.Outputs, err)
	}
	outside, err := runner.Run(t.Context(), step.Request{StepID: "task", Executor: process.LocalExecutor{}})
	if err != nil || outside.Outputs["ran"] != "outside" {
		t.Fatalf("Run() outside executors = %#v, %v", outside.Outputs, err)
	}
}

func TestRegisterRequiresTheTerminalCallbackForTerminalSteps(t *testing.T) {
	plugin, err := sdk.New("native")
	if err != nil {
		t.Fatal(err)
	}
	err = stepadapter.Register(plugin, "attach", stepadapter.Registration{
		Builder:  func(map[string]any) (step.Runner, error) { return taskRunner{}, nil },
		Terminal: func(map[string]any) (bool, bool) { return true, true },
	})
	if err == nil {
		t.Fatal("Register() accepted a terminal step without sdk.HostTerminal")
	}
}

// attachRunner reads its interactive stdin up to "q", as an attached session would.
type attachRunner struct{}

func (attachRunner) Run(_ context.Context, request step.Request) (step.Result, error) {
	file, ok := request.Stdin.(*os.File)
	if !request.Interactive || !ok {
		return step.Result{Outputs: map[string]any{"typed": "<not interactive>"}}, nil
	}
	typed, err := bufio.NewReader(file).ReadString('q')
	return step.Result{Outputs: map[string]any{"typed": typed}}, err
}

func TestRegisterLendsTheTerminalToNativeRunners(t *testing.T) {
	plugin, err := sdk.New("native")
	if err != nil {
		t.Fatal(err)
	}
	if err := stepadapter.Register(plugin, "attach", stepadapter.Registration{
		Builder:  func(map[string]any) (step.Runner, error) { return attachRunner{}, nil },
		Terminal: func(config map[string]any) (bool, bool) { return config["tty"] == true, true },
	}, sdk.WithHostCallbacks(sdk.HostTerminal)); err != nil {
		t.Fatal(err)
	}
	manager := plugintest.Connect(t, plugin)
	master, terminal, err := pty.Open()
	if err != nil {
		t.Skipf("no pseudo-terminal: %v", err)
	}
	defer master.Close()
	defer terminal.Close()
	go func() { _, _ = io.Copy(io.Discard, master) }()
	for _, test := range []struct {
		name        string
		config      map[string]any
		interactive bool
		want        string
	}{
		{name: "tty", config: map[string]any{"tty": true}, interactive: true, want: "abq"},
		{name: "no tty", config: map[string]any{}, interactive: true, want: "<not interactive>"},
		{name: "not interactive", config: map[string]any{"tty": true}, want: "<not interactive>"},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner, err := manager.ResolveStep(t.Context(), "native.attach", test.config)
			if err != nil {
				t.Fatal(err)
			}
			if test.want == "abq" {
				go func() {
					time.Sleep(100 * time.Millisecond)
					_, _ = master.Write([]byte("abq"))
				}()
			}
			result, err := runner.Run(t.Context(), step.Request{StepID: "attach", Interactive: test.interactive, Stdin: terminal})
			if err != nil || result.Outputs["typed"] != test.want {
				t.Fatalf("Run() = %#v, %v; want %q", result.Outputs, err, test.want)
			}
		})
	}
}

// syncBuffer is a writer the test can poll while a run streams into it.
type syncBuffer struct {
	mu   sync.Mutex
	data bytes.Buffer
}

func (buffer *syncBuffer) Write(data []byte) (int, error) {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.data.Write(data)
}

func (buffer *syncBuffer) String() string {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.data.String()
}

// The user's terminal is a PTY pair here: what is typed into its master must reach a shell
// running on the plugin's own PTY, a resize must follow it there, and the terminal must come
// back in the mode it started in.
func TestRegisterExecutorHandsTheUsersTerminalToTTYRuns(t *testing.T) {
	session, _ := newNativePlugin(t)
	master, terminal, err := pty.Open()
	if err != nil {
		t.Skipf("no pseudo-terminal: %v", err)
	}
	defer master.Close()
	defer terminal.Close()
	go func() { _, _ = io.Copy(io.Discard, master) }()
	if err := pty.Setsize(master, &pty.Winsize{Rows: 30, Cols: 100}); err != nil {
		t.Fatal(err)
	}
	before, err := term.GetState(int(terminal.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	var output syncBuffer
	go func() {
		deadline := time.Now().Add(10 * time.Second)
		for !strings.Contains(output.String(), "30 100") {
			if time.Now().After(deadline) {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		_ = pty.Setsize(master, &pty.Winsize{Rows: 40, Cols: 120})
		_ = syscall.Kill(os.Getpid(), syscall.SIGWINCH)
		time.Sleep(200 * time.Millisecond)
		_, _ = master.Write([]byte("bob\r"))
	}()
	result, err := session.Run(t.Context(), process.Options{
		Command: "sh", Args: []string{"-c", `stty size; IFS= read -r name; stty size; printf 'hello=%s\n' "$name"`},
		Env: environment(), TTY: true, Stdin: terminal, Stdout: &output, CaptureLimit: 1 << 20,
	})
	if err != nil {
		t.Fatalf("Run() = %+v, %v", result, err)
	}
	for _, want := range []string{"30 100", "40 120", "hello=bob"} {
		if !strings.Contains(result.Stdout, want) {
			t.Fatalf("TTY output = %q, want %q", result.Stdout, want)
		}
	}
	after, err := term.GetState(int(terminal.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("the terminal mode was not restored")
	}
}
