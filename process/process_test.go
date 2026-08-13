//go:build darwin || linux

package process

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
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
			Env: map[string]string{}, Stdout: ready,
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
