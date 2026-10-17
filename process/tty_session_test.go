//go:build darwin || linux

package process

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/up2jj/wuko/ptyinteract"
)

// scriptedTTY is a remote session that prompts once, echoes the answer, and exits with a code.
type scriptedTTY struct {
	output    *io.PipeReader
	writer    *io.PipeWriter
	exited    chan error
	mu        sync.Mutex
	input     bytes.Buffer
	sensitive bytes.Buffer
	code      int
}

func newScriptedTTY(code int) *scriptedTTY {
	reader, writer := io.Pipe()
	session := &scriptedTTY{output: reader, writer: writer, exited: make(chan error, 1), code: code}
	go func() { _, _ = writer.Write([]byte("token:")) }()
	return session
}

func (s *scriptedTTY) Read(data []byte) (int, error) { return s.output.Read(data) }
func (s *scriptedTTY) Write(data []byte) (int, error) {
	s.mu.Lock()
	s.input.Write(data)
	s.mu.Unlock()
	return len(data), nil
}
func (s *scriptedTTY) WriteSensitive(data []byte) error {
	s.mu.Lock()
	s.sensitive.Write(data)
	s.mu.Unlock()
	// The remote end echoes the secret; the host must redact it from what it captures.
	go func() {
		_, _ = s.writer.Write(append([]byte("echo="), data...))
		_ = s.writer.Close()
		s.exited <- &ExitError{Command: "remote", Code: s.code}
	}()
	return nil
}
func (s *scriptedTTY) Resize(TerminalSize) error { return nil }
func (s *scriptedTTY) Exited() <-chan error      { return s.exited }
func (s *scriptedTTY) Terminate() error {
	_ = s.writer.Close()
	return errors.New("terminated")
}
func (s *scriptedTTY) CloseOutput() error { return s.output.Close() }

func TestRunTTYSessionDrivesRemoteInteractions(t *testing.T) {
	plan, err := ptyinteract.Compile([]ptyinteract.Spec{{HasExpect: true, Expect: "token:", Send: "hunter2", Sensitive: true}})
	if err != nil {
		t.Fatal(err)
	}
	session := newScriptedTTY(3)
	result, err := RunTTYSession(t.Context(), session, Options{Command: "remote", TTY: true, Interactions: plan})
	var exitErr *ExitError
	if !errors.As(err, &exitErr) || exitErr.Code != 3 || result.ExitCode != 3 {
		t.Fatalf("RunTTYSession() = %+v, %v; want exit code 3", result, err)
	}
	if got := session.sensitive.String(); got != "hunter2" {
		t.Fatalf("sensitive input = %q, want hunter2", got)
	}
	if strings.Contains(result.Stdout, "hunter2") || !strings.Contains(result.Stdout, "token:") {
		t.Fatalf("captured output = %q, want the prompt with the secret redacted", result.Stdout)
	}
}

func TestTTYStartSizeWithoutHandoffIsDefault(t *testing.T) {
	plan, err := ptyinteract.Compile([]ptyinteract.Spec{{Send: "x"}})
	if err != nil {
		t.Fatal(err)
	}
	size, err := TTYStartSize(Options{TTY: true, Interactions: plan})
	if err != nil || size != (TerminalSize{Rows: 24, Cols: 80}) {
		t.Fatalf("TTYStartSize() = %+v, %v", size, err)
	}
	if _, err := TTYStartSize(Options{TTY: true, Stdin: strings.NewReader("")}); err == nil {
		t.Fatal("TTYStartSize() accepted a handoff without a terminal")
	}
}
