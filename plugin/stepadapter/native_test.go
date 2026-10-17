package stepadapter_test

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
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
