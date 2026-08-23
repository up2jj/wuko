package logwait

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/up2jj/wuko/step"
)

func TestNewValidatesConfiguration(t *testing.T) {
	tests := []struct {
		name string
		raw  map[string]any
		want string
	}{
		{name: "missing path", raw: map[string]any{"pattern": "ready"}, want: "path is required"},
		{name: "missing pattern", raw: map[string]any{"path": "app.log"}, want: "pattern is required"},
		{name: "invalid pattern", raw: map[string]any{"path": "app.log", "pattern": "["}, want: "compiling pattern"},
		{name: "duplicate capture", raw: map[string]any{"path": "app.log", "pattern": `(?P<value>a)(?P<value>b)`}, want: "duplicate named capture"},
		{name: "invalid max bytes", raw: map[string]any{"path": "app.log", "pattern": "ready", "max_bytes": "0B"}, want: "positive size"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := New(tt.raw)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("New() error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

func TestLogWaitMatchesExistingContentAndNamedCaptures(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "app.log")
	if err := os.WriteFile(path, []byte("starting\nready id=42\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := newRunner(t, map[string]any{
		"path": path, "pattern": `ready id=(?P<id>\d+)`,
	})

	result, err := runner.Run(t.Context(), request(root))
	if err != nil {
		t.Fatal(err)
	}
	if result.Outputs["match"] != "ready id=42" {
		t.Fatalf("match = %#v", result.Outputs["match"])
	}
	captures := result.Outputs["captures"].(map[string]string)
	if captures["id"] != "42" {
		t.Fatalf("captures = %#v", captures)
	}
}

func TestLogWaitFollowsAppends(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "app.log")
	if err := os.WriteFile(path, []byte("starting\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runner, ready := newRunnerWithReadyWatcher(t, map[string]any{"path": path, "pattern": "ready"})
	resultCh := runAsync(t, runner, request(root))
	<-ready
	appendFile(t, path, "ready\n")

	result := receiveResult(t, resultCh)
	if result.Outputs["match"] != "ready" {
		t.Fatalf("match = %#v", result.Outputs["match"])
	}
}

func TestLogWaitWaitsForCreation(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "app.log")
	runner, ready := newRunnerWithReadyWatcher(t, map[string]any{"path": path, "pattern": "ready"})
	resultCh := runAsync(t, runner, request(root))
	<-ready
	if err := os.WriteFile(path, []byte("ready\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	result := receiveResult(t, resultCh)
	if result.Outputs["match"] != "ready" {
		t.Fatalf("match = %#v", result.Outputs["match"])
	}
}

func TestLogWaitResetsAfterTruncationAndReplacement(t *testing.T) {
	tests := []struct {
		name   string
		update func(*testing.T, string)
	}{
		{
			name: "truncation",
			update: func(t *testing.T, path string) {
				if err := os.Truncate(path, 0); err != nil {
					t.Fatal(err)
				}
				appendFile(t, path, "ready\n")
			},
		},
		{
			name: "replacement",
			update: func(t *testing.T, path string) {
				temporary := path + ".new"
				if err := os.WriteFile(temporary, []byte("ready\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(temporary, path); err != nil {
					t.Fatal(err)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "app.log")
			if err := os.WriteFile(path, []byte("old content\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			runner, ready := newRunnerWithReadyWatcher(t, map[string]any{"path": path, "pattern": "ready"})
			resultCh := runAsync(t, runner, request(root))
			<-ready
			tt.update(t, path)

			result := receiveResult(t, resultCh)
			if result.Outputs["match"] != "ready" {
				t.Fatalf("match = %#v", result.Outputs["match"])
			}
		})
	}
}

func TestLogWaitFailsWhenMaxBytesIsExceeded(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "app.log")
	if err := os.WriteFile(path, []byte("long content"), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := newRunner(t, map[string]any{"path": path, "pattern": "ready", "max_bytes": "4B"})
	_, err := runner.Run(t.Context(), request(root))
	if err == nil || !strings.Contains(err.Error(), "exceeded max_bytes") {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestLogWaitStopsOnCancellation(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "app.log")
	runner, ready := newRunnerWithReadyWatcher(t, map[string]any{"path": path, "pattern": "ready"})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	resultCh := make(chan error, 1)
	go func() {
		_, err := runner.Run(ctx, request(root))
		resultCh <- err
	}()
	<-ready
	cancel()

	select {
	case err := <-resultCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run() error = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run() did not stop after cancellation")
	}
}

func newRunner(t *testing.T, raw map[string]any) step.Runner {
	t.Helper()
	runner, err := New(raw)
	if err != nil {
		t.Fatal(err)
	}
	return runner
}

func newRunnerWithReadyWatcher(t *testing.T, raw map[string]any) (*Runner, <-chan struct{}) {
	t.Helper()
	runner, err := New(raw)
	if err != nil {
		t.Fatal(err)
	}
	concrete := runner.(*Runner)
	ready := make(chan struct{})
	concrete.newWatcher = func() (eventWatcher, error) {
		watcher, err := fsnotify.NewWatcher()
		if err != nil {
			return nil, err
		}
		return &readyWatcher{eventWatcher: nativeWatcher{Watcher: watcher}, ready: ready}, nil
	}
	return concrete, ready
}

type readyWatcher struct {
	eventWatcher
	ready chan<- struct{}
}

func (w *readyWatcher) Add(path string) error {
	err := w.eventWatcher.Add(path)
	if err == nil {
		close(w.ready)
	}
	return err
}

func request(root string) step.Request { return step.Request{RunDir: root} }

func runAsync(t *testing.T, runner step.Runner, request step.Request) <-chan step.Result {
	t.Helper()
	resultCh := make(chan step.Result, 1)
	go func() {
		result, err := runner.Run(t.Context(), request)
		if err != nil {
			t.Errorf("Run() error = %v", err)
			return
		}
		resultCh <- result
	}()
	return resultCh
}

func receiveResult(t *testing.T, resultCh <-chan step.Result) step.Result {
	t.Helper()
	select {
	case result := <-resultCh:
		return result
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for log_wait result")
		return step.Result{}
	}
}

func appendFile(t *testing.T, path, content string) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(content); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}
