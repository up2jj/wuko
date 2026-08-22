package watch

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/up2jj/wuko/step"
)

func TestWatchRejectsInvalidConfiguration(t *testing.T) {
	tests := []struct {
		name string
		raw  map[string]any
		want string
	}{
		{name: "missing patterns", raw: map[string]any{}, want: "at least one"},
		{name: "empty root", raw: map[string]any{"root": "", "patterns": []any{"**"}}, want: "root must not be empty"},
		{name: "blank root", raw: map[string]any{"root": "  ", "patterns": []any{"**"}}, want: "root must not be empty"},
		{name: "empty pattern", raw: map[string]any{"patterns": []any{""}}, want: "must not be empty"},
		{name: "malformed pattern", raw: map[string]any{"patterns": []any{"[abc"}}, want: "invalid pattern"},
		{name: "absolute pattern", raw: map[string]any{"patterns": []any{"/tmp/*.go"}}, want: "relative to root"},
		{name: "windows absolute pattern", raw: map[string]any{"patterns": []any{`C:\tmp\*.go`}}, want: "relative to root"},
		{name: "parent pattern", raw: map[string]any{"patterns": []any{"../*.go"}}, want: "parent directory"},
		{name: "empty events", raw: map[string]any{"patterns": []any{"**"}, "events": []any{}}, want: "at least one"},
		{name: "unknown event", raw: map[string]any{"patterns": []any{"**"}, "events": []any{"chmod"}}, want: "create, modify, rename, or remove"},
		{name: "unknown field", raw: map[string]any{"patterns": []any{"**"}, "unknown": true}, want: "field unknown"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := New(test.raw)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestWatchDefaultsAndNormalizesEvents(t *testing.T) {
	runner, err := New(map[string]any{"patterns": []any{"**"}})
	if err != nil {
		t.Fatal(err)
	}
	got := runner.(*Runner)
	if got.config.Root != "." || !slices.Equal(got.config.Events, []string{"create", "modify", "rename", "remove"}) {
		t.Fatalf("config = %#v", got.config)
	}

	runner, err = New(map[string]any{
		"patterns": []any{"**"},
		"events":   []any{"remove", "create", "remove", "modify", "create"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := runner.(*Runner).config.Events; !slices.Equal(got, []string{"create", "modify", "remove"}) {
		t.Fatalf("events = %v", got)
	}
}

func TestWatchAcceptsTemplatesDuringStaticValidation(t *testing.T) {
	runner, err := New(map[string]any{
		"root": "{{ .vars.root }}", "patterns": []any{"{{ .vars.pattern }}"}, "events": []any{"{{ .vars.event }}"},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.Run(t.Context(), step.Request{RunDir: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "unresolved template") {
		t.Fatalf("error = %v", err)
	}
}

func TestWatchReturnsFirstMatchingCompositeEvent(t *testing.T) {
	root := t.TempDir()
	watcher := newFakeWatcher()
	watcher.events <- fsnotify.Event{Name: filepath.Join(root, "notes.txt"), Op: fsnotify.Create}
	watcher.events <- fsnotify.Event{Name: filepath.Join(root, ".hidden.go"), Op: fsnotify.Write}
	watcher.events <- fsnotify.Event{Name: filepath.Join(root, "main.go"), Op: fsnotify.Chmod}
	watcher.events <- fsnotify.Event{Name: filepath.Join(root, "main.go"), Op: fsnotify.Create | fsnotify.Write | fsnotify.Chmod}
	runner := configuredRunner(t, watcher, map[string]any{
		"patterns": []any{"**/*.go"}, "events": []any{"modify", "create"},
	})

	result, err := runner.Run(t.Context(), step.Request{RunDir: root})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outputs["root"] != root || result.Outputs["path"] != "main.go" {
		t.Fatalf("outputs = %#v", result.Outputs)
	}
	operations := result.Outputs["operations"].([]any)
	if !slices.Equal(operations, []any{"create", "modify"}) {
		t.Fatalf("operations = %v", operations)
	}
	if !watcher.isClosed() {
		t.Fatal("watcher was not closed")
	}
}

func TestWatchMatchesExplicitHiddenPattern(t *testing.T) {
	root := t.TempDir()
	watcher := newFakeWatcher()
	watcher.events <- fsnotify.Event{Name: filepath.Join(root, ".config", "settings.yaml"), Op: fsnotify.Write}
	runner := configuredRunner(t, watcher, map[string]any{"patterns": []any{".config/**/*.yaml"}})
	result, err := runner.Run(t.Context(), step.Request{RunDir: root})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outputs["path"] != ".config/settings.yaml" {
		t.Fatalf("outputs = %#v", result.Outputs)
	}
}

func TestWatchRegistersExistingTreeWithoutFollowingSymlinks(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "src", "nested")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "linked")); err != nil {
		t.Fatal(err)
	}
	watcher := newFakeWatcher()
	watcher.events <- fsnotify.Event{Name: filepath.Join(nested, "main.go"), Op: fsnotify.Write}
	runner := configuredRunner(t, watcher, map[string]any{"patterns": []any{"**/*.go"}})
	if _, err := runner.Run(t.Context(), step.Request{RunDir: root}); err != nil {
		t.Fatal(err)
	}
	added := watcher.addedPaths()
	for _, want := range []string{root, filepath.Join(root, "src"), nested} {
		if !slices.Contains(added, want) {
			t.Fatalf("added = %v, missing %s", added, want)
		}
	}
	if slices.Contains(added, outside) || slices.Contains(added, filepath.Join(root, "linked")) {
		t.Fatalf("followed symlink: %v", added)
	}
}

func TestWatchRegistersCreatedDirectoryTree(t *testing.T) {
	root := t.TempDir()
	watcher := newFakeWatcher()
	runner := configuredRunner(t, watcher, map[string]any{"patterns": []any{"**/*.go"}})
	done := make(chan error, 1)
	go func() {
		_, err := runner.Run(t.Context(), step.Request{RunDir: root})
		done <- err
	}()

	if added := <-watcher.added; added != root {
		t.Fatalf("first added directory = %s, want %s", added, root)
	}
	created := filepath.Join(root, "created")
	nested := filepath.Join(created, "nested")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	watcher.events <- fsnotify.Event{Name: created, Op: fsnotify.Create}
	watcher.events <- fsnotify.Event{Name: filepath.Join(nested, "main.go"), Op: fsnotify.Write}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	added := watcher.addedPaths()
	if !slices.Contains(added, created) || !slices.Contains(added, nested) {
		t.Fatalf("added = %v", added)
	}
}

func TestWatchPropagatesWatcherFailures(t *testing.T) {
	root := t.TempDir()
	wantErr := errors.New("watch overflow")
	watcher := newFakeWatcher()
	watcher.errs <- wantErr
	runner := configuredRunner(t, watcher, map[string]any{"patterns": []any{"**"}})
	_, err := runner.Run(t.Context(), step.Request{RunDir: root})
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v", err)
	}

	watcher = newFakeWatcher()
	close(watcher.events)
	runner = configuredRunner(t, watcher, map[string]any{"patterns": []any{"**"}})
	if _, err := runner.Run(t.Context(), step.Request{RunDir: root}); err == nil || !strings.Contains(err.Error(), "event channel closed") {
		t.Fatalf("event channel error = %v", err)
	}

	watcher = newFakeWatcher()
	close(watcher.errs)
	runner = configuredRunner(t, watcher, map[string]any{"patterns": []any{"**"}})
	if _, err := runner.Run(t.Context(), step.Request{RunDir: root}); err == nil || !strings.Contains(err.Error(), "error channel closed") {
		t.Fatalf("error channel error = %v", err)
	}

	wantErr = errors.New("close failed")
	watcher = newFakeWatcher()
	watcher.closeErr = wantErr
	watcher.events <- fsnotify.Event{Name: filepath.Join(root, "matched"), Op: fsnotify.Create}
	runner = configuredRunner(t, watcher, map[string]any{"patterns": []any{"**"}})
	result, err := runner.Run(t.Context(), step.Request{RunDir: root})
	if !errors.Is(err, wantErr) || result.Outputs != nil {
		t.Fatalf("close result = %#v, error = %v", result, err)
	}
}

func TestWatchHonorsCancellationAndSetupErrors(t *testing.T) {
	root := t.TempDir()
	watcher := newFakeWatcher()
	ctx, cancel := context.WithCancel(t.Context())
	runner := configuredRunner(t, watcher, map[string]any{"patterns": []any{"**"}})
	done := make(chan error, 1)
	go func() {
		_, err := runner.Run(ctx, step.Request{RunDir: root})
		done <- err
	}()
	<-watcher.added
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error = %v", err)
	}
	if !watcher.isClosed() {
		t.Fatal("watcher was not closed after cancellation")
	}

	wantErr := errors.New("add failed")
	watcher = newFakeWatcher()
	watcher.addErr = wantErr
	runner = configuredRunner(t, watcher, map[string]any{"patterns": []any{"**"}})
	if _, err := runner.Run(t.Context(), step.Request{RunDir: root}); !errors.Is(err, wantErr) {
		t.Fatalf("add error = %v", err)
	}
}

func TestWatchRejectsInvalidRuntimeRoot(t *testing.T) {
	runDir := t.TempDir()
	file := filepath.Join(runDir, "file")
	writeTestFile(t, file, "content")
	symlink := filepath.Join(runDir, "linked")
	if err := os.Symlink(t.TempDir(), symlink); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		root string
		want string
	}{
		{name: "missing", root: "missing", want: "inspecting watch root"},
		{name: "file", root: "file", want: "not a directory"},
		{name: "directory symlink", root: "linked", want: "not a directory"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runnerValue, err := New(map[string]any{"root": test.root, "patterns": []any{"**"}})
			if err != nil {
				t.Fatal(err)
			}
			_, err = runnerValue.Run(t.Context(), step.Request{RunDir: runDir})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestWatchRealFilesystemEvents(t *testing.T) {
	tests := []struct {
		name      string
		operation string
		prepare   func(*testing.T, string)
		trigger   func(*testing.T, string)
	}{
		{
			name: "create", operation: "create",
			trigger: func(t *testing.T, target string) { writeTestFile(t, target, "created") },
		},
		{
			name: "modify", operation: "modify",
			prepare: func(t *testing.T, target string) { writeTestFile(t, target, "before") },
			trigger: func(t *testing.T, target string) { writeTestFile(t, target, "after") },
		},
		{
			name: "rename", operation: "rename",
			prepare: func(t *testing.T, target string) { writeTestFile(t, target, "rename") },
			trigger: func(t *testing.T, target string) {
				if err := os.Rename(target, target+".moved"); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "remove", operation: "remove",
			prepare: func(t *testing.T, target string) { writeTestFile(t, target, "remove") },
			trigger: func(t *testing.T, target string) {
				if err := os.Remove(target); err != nil {
					t.Fatal(err)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			target := filepath.Join(root, "target.txt")
			if test.prepare != nil {
				test.prepare(t, target)
			}
			ready := make(chan struct{})
			runnerValue, err := New(map[string]any{
				"patterns": []any{"target.txt"}, "events": []any{test.operation},
			})
			if err != nil {
				t.Fatal(err)
			}
			runner := runnerValue.(*Runner)
			runner.newWatcher = func() (eventWatcher, error) {
				watcher, err := fsnotify.NewWatcher()
				if err != nil {
					return nil, err
				}
				return &readyWatcher{nativeWatcher: nativeWatcher{Watcher: watcher}, ready: ready}, nil
			}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			results := make(chan struct {
				result step.Result
				err    error
			}, 1)
			go func() {
				result, err := runner.Run(ctx, step.Request{RunDir: root})
				results <- struct {
					result step.Result
					err    error
				}{result: result, err: err}
			}()
			<-ready
			test.trigger(t, target)
			got := <-results
			if got.err != nil {
				t.Fatal(got.err)
			}
			if got.result.Outputs["path"] != "target.txt" || !slices.Equal(got.result.Outputs["operations"].([]any), []any{test.operation}) {
				t.Fatalf("outputs = %#v", got.result.Outputs)
			}
		})
	}
}

func configuredRunner(t *testing.T, watcher eventWatcher, raw map[string]any) *Runner {
	t.Helper()
	runner, err := New(raw)
	if err != nil {
		t.Fatal(err)
	}
	configured := runner.(*Runner)
	configured.newWatcher = func() (eventWatcher, error) { return watcher, nil }
	return configured
}

func writeTestFile(t *testing.T, name, content string) {
	t.Helper()
	if err := os.WriteFile(name, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

type fakeWatcher struct {
	events   chan fsnotify.Event
	errs     chan error
	added    chan string
	addErr   error
	closeErr error

	mu     sync.Mutex
	paths  []string
	closed bool
}

func newFakeWatcher() *fakeWatcher {
	return &fakeWatcher{
		events: make(chan fsnotify.Event, 16), errs: make(chan error, 4), added: make(chan string, 16),
	}
}

func (w *fakeWatcher) Add(name string) error {
	if w.addErr != nil {
		return w.addErr
	}
	w.mu.Lock()
	w.paths = append(w.paths, name)
	w.mu.Unlock()
	w.added <- name
	return nil
}

func (w *fakeWatcher) Close() error {
	w.mu.Lock()
	w.closed = true
	w.mu.Unlock()
	return w.closeErr
}

func (w *fakeWatcher) EventChannel() <-chan fsnotify.Event { return w.events }
func (w *fakeWatcher) ErrorChannel() <-chan error          { return w.errs }

func (w *fakeWatcher) addedPaths() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Clone(w.paths)
}

func (w *fakeWatcher) isClosed() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.closed
}

type readyWatcher struct {
	nativeWatcher
	ready chan struct{}
	once  sync.Once
}

func (w *readyWatcher) Add(name string) error {
	if err := w.nativeWatcher.Add(name); err != nil {
		return err
	}
	w.once.Do(func() { close(w.ready) })
	return nil
}
