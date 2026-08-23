//go:build darwin || linux

package process

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/user"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
	"golang.org/x/term"
)

func TestRunCapturesOutputAndExitStatus(t *testing.T) {
	result, err := Run(t.Context(), Options{
		Command: "sh", Args: []string{"-c", "printf out; printf err >&2; exit 7"},
		Env: map[string]string{}, Stdout: io.Discard, Stderr: io.Discard,
	})
	var exitErr *ExitError
	if !errors.As(err, &exitErr) || exitErr.Code != 7 {
		t.Fatalf("error = %v", err)
	}
	if result.Stdout != "out" || result.Stderr != "err" || result.ExitCode != 7 {
		t.Fatalf("result = %#v", result)
	}
}

func TestRunHonorsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := Run(ctx, Options{Command: "sh", Args: []string{"-c", "exit 0"}, Env: map[string]string{}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
}

func TestRunAsCurrentUser(t *testing.T) {
	current, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	result, err := Run(t.Context(), Options{
		Command: "sh", Args: []string{"-c", "id -u"}, Env: testEnvironment(), User: current.Uid,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(result.Stdout); got != strconv.Itoa(os.Geteuid()) {
		t.Fatalf("effective user ID = %q, want %d", got, os.Geteuid())
	}
}

func TestRunRejectsUnknownUser(t *testing.T) {
	_, err := Run(t.Context(), Options{
		Command: "sh", Args: []string{"-c", "exit 0"}, Env: map[string]string{},
		User: "wuko-user-that-does-not-exist-4f58bb1d",
	})
	if err == nil || !strings.Contains(err.Error(), `looking up user "wuko-user-that-does-not-exist-4f58bb1d"`) {
		t.Fatalf("error = %v", err)
	}
}

func TestNumericID(t *testing.T) {
	tests := []struct {
		value string
		want  bool
	}{
		{value: "0", want: true},
		{value: "501", want: true},
		{value: "-2", want: true},
		{value: ""},
		{value: "-"},
		{value: "12a"},
	}
	for _, tt := range tests {
		t.Run(tt.value, func(t *testing.T) {
			if got := numericID(tt.value); got != tt.want {
				t.Fatalf("numericID(%q) = %v, want %v", tt.value, got, tt.want)
			}
		})
	}
}

func TestParseUserID(t *testing.T) {
	tests := []struct {
		value   string
		want    uint32
		wantErr bool
	}{
		{value: "0", want: 0},
		{value: "501", want: 501},
		{value: "-2", want: ^uint32(1)},
		{value: "4294967295", want: ^uint32(0)},
		{value: "4294967296", wantErr: true},
		{value: "invalid", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.value, func(t *testing.T) {
			got, err := parseUserID("user", tt.value)
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseUserID() error = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("parseUserID() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestRunKillsStubbornProcessGroupOnCancellation(t *testing.T) {
	ready := &processReadyWriter{ready: make(chan [2]int, 1)}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		_, err := Run(ctx, Options{
			Command: "sh",
			Args: []string{"-c", `
trap '' TERM
sh -c 'trap "" TERM; while :; do sleep 10; done' &
printf '%d %d\n' "$$" "$!"
wait
`},
			Env: testEnvironment(), Stdout: ready,
		})
		done <- err
	}()

	var processIDs [2]int
	select {
	case processIDs = <-ready.ready:
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("timed out waiting for stubborn process group")
	}
	t.Cleanup(func() { _ = syscall.Kill(-processIDs[0], syscall.SIGKILL) })
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run() error = %v, want context.Canceled", err)
		}
	case <-time.After(2*terminationGracePeriod + time.Second):
		t.Fatal("Run did not return after cancellation escalation")
	}
	if waitForProcessGroupExit(processIDs[0], time.Second) {
		t.Fatalf("process group %d is still alive after Run returned", processIDs[0])
	}
}

func TestRunBoundsCapturedOutput(t *testing.T) {
	result, err := Run(t.Context(), Options{
		Command: "sh", Args: []string{"-c", "printf 123456; printf abcdef >&2"},
		Env: map[string]string{}, Stdout: io.Discard, Stderr: io.Discard, CaptureLimit: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Stdout != "1234" || result.Stderr != "abcd" || !result.StdoutTruncated || !result.StderrTruncated {
		t.Fatalf("result = %#v", result)
	}
}

func TestRunTTYStreamsInputMergesOutputAndRestoresTerminal(t *testing.T) {
	terminal, input := testTerminal(t, 24, 80)
	before, err := term.GetState(int(terminal.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	output := &readyOutput{ready: make(chan struct{})}
	done := make(chan struct {
		result Result
		err    error
	}, 1)
	go func() {
		result, runErr := Run(t.Context(), Options{
			Command: "sh", Args: []string{"-c", `
test -t 0 && test -t 1 && test -t 2 || exit 9
printf 'ready:'
IFS= read -r value
printf 'got=%s\n' "$value"
printf 'diagnostic\n' >&2
stty size
exit 7
`},
			Env: testEnvironment(), Stdin: terminal, Stdout: output, Stderr: io.Discard, TTY: true, CaptureLimit: 1 << 20,
		})
		done <- struct {
			result Result
			err    error
		}{result, runErr}
	}()

	select {
	case <-output.ready:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for TTY command")
	}
	if err := pty.Setsize(input, &pty.Winsize{Rows: 33, Cols: 91}); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Kill(os.Getpid(), syscall.SIGWINCH); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if _, err := input.WriteString("Ada\n"); err != nil {
		t.Fatal(err)
	}

	completed := <-done
	var exitErr *ExitError
	if !errors.As(completed.err, &exitErr) || exitErr.Code != 7 {
		t.Fatalf("Run() error = %v", completed.err)
	}
	if completed.result.ExitCode != 7 || completed.result.Stderr != "" || completed.result.StderrTruncated {
		t.Fatalf("result = %#v", completed.result)
	}
	for _, want := range []string{"got=Ada", "diagnostic", "33 91"} {
		if !strings.Contains(completed.result.Stdout, want) || !strings.Contains(output.String(), want) {
			t.Fatalf("TTY output = %q, want %q", completed.result.Stdout, want)
		}
	}
	after, err := term.GetState(int(terminal.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("terminal state was not restored")
	}
}

func TestRunTTYBoundsCaptureWithoutLimitingStream(t *testing.T) {
	terminal, _ := testTerminal(t, 24, 80)
	var streamed bytes.Buffer
	result, err := Run(t.Context(), Options{
		Command: "sh", Args: []string{"-c", "awk 'BEGIN { for (i = 0; i < 4096; i++) print \"0123456789abcdef\" }'; printf 'END-MARKER'"},
		Env: testEnvironment(), Stdin: terminal, Stdout: &streamed, TTY: true, CaptureLimit: 1024,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Stdout) != 1024 || !result.StdoutTruncated {
		t.Fatalf("captured bytes = %d, truncated = %v", len(result.Stdout), result.StdoutTruncated)
	}
	if streamed.Len() <= len(result.Stdout) {
		t.Fatalf("streamed bytes = %d, captured bytes = %d", streamed.Len(), len(result.Stdout))
	}
	if !strings.Contains(streamed.String(), "END-MARKER") {
		t.Fatal("live stream did not drain final PTY output")
	}
}

func TestRunTTYRejectsNonTerminalInput(t *testing.T) {
	_, err := Run(t.Context(), Options{Command: "sh", Env: map[string]string{}, Stdin: strings.NewReader("input"), TTY: true})
	if err == nil || !strings.Contains(err.Error(), "file-backed terminal stdin") {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestRunTTYCancellationRestoresTerminal(t *testing.T) {
	terminal, input := testTerminal(t, 24, 80)
	before, err := term.GetState(int(terminal.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	output := &readyOutput{ready: make(chan struct{})}
	done := make(chan error, 1)
	go func() {
		_, runErr := Run(ctx, Options{
			Command: "sh", Args: []string{"-c", "printf 'ready:'; sleep 30"},
			Env: testEnvironment(), Stdin: terminal, Stdout: output, TTY: true,
		})
		done <- runErr
	}()
	select {
	case <-output.ready:
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("timed out waiting for TTY command")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run() error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("TTY command did not stop after cancellation")
	}
	after, err := term.GetState(int(terminal.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("terminal state was not restored after cancellation")
	}
	if _, err := input.WriteString("unused"); err != nil {
		t.Fatalf("outer terminal was closed: %v", err)
	}
}

func testTerminal(t *testing.T, rows, cols uint16) (*os.File, *os.File) {
	t.Helper()
	input, terminal, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = input.Close()
		_ = terminal.Close()
	})
	if err := pty.Setsize(input, &pty.Winsize{Rows: rows, Cols: cols}); err != nil {
		t.Fatal(err)
	}
	return terminal, input
}

func testEnvironment() map[string]string {
	return map[string]string{"PATH": os.Getenv("PATH")}
}

type readyOutput struct {
	mu    sync.Mutex
	data  bytes.Buffer
	once  sync.Once
	ready chan struct{}
}

func (output *readyOutput) Write(data []byte) (int, error) {
	output.mu.Lock()
	defer output.mu.Unlock()
	length := len(data)
	_, _ = output.data.Write(data)
	if strings.Contains(output.data.String(), "ready:") {
		output.once.Do(func() { close(output.ready) })
	}
	return length, nil
}

func (output *readyOutput) String() string {
	output.mu.Lock()
	defer output.mu.Unlock()
	return output.data.String()
}

type processReadyWriter struct {
	mu    sync.Mutex
	data  bytes.Buffer
	once  sync.Once
	ready chan [2]int
}

func (writer *processReadyWriter) Write(data []byte) (int, error) {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	length := len(data)
	_, _ = writer.data.Write(data)
	line, _, found := strings.Cut(writer.data.String(), "\n")
	if !found {
		return length, nil
	}
	fields := strings.Fields(line)
	if len(fields) != 2 {
		return length, nil
	}
	parent, parentErr := strconv.Atoi(fields[0])
	child, childErr := strconv.Atoi(fields[1])
	if parentErr == nil && childErr == nil {
		writer.once.Do(func() { writer.ready <- [2]int{parent, child} })
	}
	return length, nil
}

func waitForProcessGroupExit(processID int, timeout time.Duration) bool {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if !processGroupAlive(processID) {
			return false
		}
		select {
		case <-deadline.C:
			return true
		case <-ticker.C:
		}
	}
}
